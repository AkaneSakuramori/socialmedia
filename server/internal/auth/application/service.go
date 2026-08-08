// Package application implements the auth module's use-cases (ENGINEERING.md
// §7, §10). The exported Service interface is the only entry point other
// modules (delivery, or user/chat applications) may call. Services hold small
// ports from the domain layer, injected at the composition root; no I/O and no
// concrete adapters live here.
package application

import (
	"context"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/internal/auth/domain"
	userdomain "github.com/AkaneSakuramori/socialmedia/server/internal/user/domain"
	"github.com/AkaneSakuramori/socialmedia/server/pkg/clock"
	"github.com/AkaneSakuramori/socialmedia/server/pkg/tx"
)

// Service is the exported auth application service interface. Other modules and
// the delivery layer depend on this, never on the concrete service.
type Service interface {
	// Register creates a new account, verifies the OTP, and returns the first
	// session and token pair (API.md §4.1).
	Register(ctx context.Context, cmd RegisterCommand) (*RegisterResult, error)
	// Login authenticates an existing user (password or OTP) and returns the
	// session bound to the device with a fresh token pair (API.md §4.3).
	// Failed attempts are throttled per identifier (AUTH-5).
	Login(ctx context.Context, cmd LoginCommand) (*LoginResult, error)
	// Refresh rotates the session's access and refresh tokens (API.md §4.4,
	// REFR-4). Presenting a rotated-out token revokes all sessions and
	// returns ErrRefreshTokenReuse (REFR-5).
	Refresh(ctx context.Context, cmd RefreshCommand) (*RefreshResult, error)
	// ListSessions returns the user's active devices for the device-management
	// screen (API.md §4.7, SECURITY_SPEC.md SESS-8/DEVM-2).
	ListSessions(ctx context.Context, cmd ListSessionsCommand) ([]SessionInfo, error)
	// RenameSession re-labels one of the user's active devices.
	RenameSession(ctx context.Context, cmd RenameSessionCommand) error
	// Logout revokes the caller's current session (API.md §4.5). The session
	// identity comes from the token, never from a body field (SESS-3).
	Logout(ctx context.Context, cmd LogoutCommand) error
	// LogoutSession revokes a specific device session (API.md §4.8); ownership
	// is enforced (403 not owner / 404 not found).
	LogoutSession(ctx context.Context, cmd LogoutSessionCommand) error
	// LogoutOtherSessions revokes every active session except the caller's
	// current one ("log out other devices").
	LogoutOtherSessions(ctx context.Context, cmd LogoutOtherSessionsCommand) error
	// LogoutAll revokes every session of the user and bumps the global token
	// version so all outstanding access tokens fail at the gateways
	// (API.md §4.6, SECURITY_SPEC.md SESS-6).
	LogoutAll(ctx context.Context, cmd LogoutAllCommand) error
	// ExpireIdleSessions runs the sliding idle/refresh-window expiry sweep
	// (SESS-9, REFR-6). It returns the number of sessions expired.
	ExpireIdleSessions(ctx context.Context) (int64, error)
	// PurgeRevokedSessions runs the retention purge for revoked/expired
	// sessions (DATABASE.md §4.4). It returns the number of rows deleted.
	PurgeRevokedSessions(ctx context.Context) (int64, error)

	// ---- Milestone 5: Account security, recovery & production hardening ----

	// RequestPasswordReset issues a single-use reset token for the account
	// behind the identifier (forgot-password). The response is uniform for
	// known and unknown identifiers (no enumeration). The plaintext token is
	// returned to the delivery layer for out-of-band delivery (email/SMS).
	RequestPasswordReset(ctx context.Context, cmd RequestPasswordResetCommand) (*RequestPasswordResetResult, error)
	// ResetPassword consumes a reset token, sets a new password, and suspends
	// every session of the account (REC-4: recovery revokes existing sessions).
	ResetPassword(ctx context.Context, cmd ResetPasswordCommand) error
	// ChangePassword verifies the current credential (AUTH-9 step-up), sets a
	// new password, and suspends every other session (PASS-4 / SESS-4).
	ChangePassword(ctx context.Context, cmd ChangePasswordCommand) error

	// RequestEmailChange starts a verified email change: after step-up it
	// issues a single-use confirmation token bound to the pending email.
	RequestEmailChange(ctx context.Context, cmd RequestEmailChangeCommand) (*RequestEmailChangeResult, error)
	// ConfirmEmailChange consumes the confirmation token and applies the new
	// email (email verification completion).
	ConfirmEmailChange(ctx context.Context, cmd ConfirmEmailChangeCommand) error
	// RequestPhoneChange starts a verified phone change.
	RequestPhoneChange(ctx context.Context, cmd RequestPhoneChangeCommand) (*RequestPhoneChangeResult, error)
	// ConfirmPhoneChange consumes the confirmation token and applies the new
	// phone (phone verification completion).
	ConfirmPhoneChange(ctx context.Context, cmd ConfirmPhoneChangeCommand) error

	// DeleteAccount soft-deletes the account (DATABASE.md §4.1: account_state
	// 'deleted' + deleted_at), revokes every session, and bumps the token
	// version. Requires step-up re-authentication (AUTH-9).
	DeleteAccount(ctx context.Context, cmd DeleteAccountCommand) error
	// RestoreAccount reactivates a soft-deleted account within the grace
	// window after identifier verification via OTP (REC-1).
	RestoreAccount(ctx context.Context, cmd RestoreAccountCommand) error
	// PurgeDeletedAccounts hard-deletes accounts whose grace period elapsed
	// (DATABASE.md §4.1 retention worker). Returns the number of accounts purged.
	PurgeDeletedAccounts(ctx context.Context) (int64, error)

	// ListLoginHistory returns the caller's own login history (login-history
	// security screen), newest first.
	ListLoginHistory(ctx context.Context, cmd ListLoginHistoryCommand) ([]LoginEventInfo, error)

	// Authenticate validates an access token at the gateway boundary and
	// returns the account behind it (SECURITY_SPEC.md JWT-5). It verifies the
	// JWT signature/expiry, then checks the account state, the token-version
	// freshness (SESS-6), and that the token's session is still active. The
	// delivery bearer middleware is the sole caller.
	Authenticate(ctx context.Context, token, deviceID string) (*userdomain.User, error)
	// AuthenticateClaims is Authenticate plus the validated claim set (user,
	// session, device). The WS gateway binds a socket to (user_id, session_id)
	// from these claims (API.md §16.1) and enforces per-session revocation.
	AuthenticateClaims(ctx context.Context, token, deviceID string) (*userdomain.User, *domain.AccessClaims, error)
}

// LoginCommand is the validated input for authentication (API.md §4.3).
// Password is used when Method == password; OTPCode when Method == otp.
type LoginCommand struct {
	IdentifierType domain.IdentifierType
	Identifier     string
	Method         domain.LoginMethod
	Password       string
	OTPCode        string
	Device         domain.DeviceInfo
	IPAddress      *string
	UserAgent      *string
}

// LoginResult is the outcome of authentication: the account, its device
// session, and the issued token pair (same shape as API.md §4.1/§4.3).
type LoginResult struct {
	User      userdomain.User
	Session   domain.Session
	TokenPair domain.TokenPair
}

// RefreshCommand is the input to token rotation (API.md §4.4). RefreshToken is
// the opaque token delivered via the X-Refresh-Token header, never in bodies.
type RefreshCommand struct {
	RefreshToken string
	IPAddress    *string
	UserAgent    *string
}

// RefreshResult is the rotated credential set (API.md §4.4 response shape:
// access_token, expires_in, refresh_token, session_id).
type RefreshResult struct {
	AccessToken  string
	RefreshToken string
	SessionID    int64
	ExpiresIn    int64 // seconds until the access token expires
}

// RegisterCommand is the validated input for account creation (API.md §4.1).
// Password is optional: passkey/OTP-only accounts are allowed by the spec.
type RegisterCommand struct {
	IdentifierType domain.IdentifierType
	Identifier     string
	OTPCode        string
	DisplayName    string
	Username       *string
	Password       *string
	Device         domain.DeviceInfo
	IPAddress      *string
	UserAgent      *string
}

// RegisterResult is the outcome of account creation: the account, its first
// session, and the issued token pair.
type RegisterResult struct {
	User      userdomain.User
	Session   domain.Session
	TokenPair domain.TokenPair
}

// SessionInfo is the device-management view of one session (API.md §4.7): the
// public surface, never the refresh-token hashes or push tokens.
type SessionInfo struct {
	ID           int64
	DeviceID     string
	DeviceName   *string
	Platform     *string
	AppVersion   *string
	LastActiveAt time.Time
	CreatedAt    time.Time
	// Current marks the session that made the request.
	Current bool
}

// ListSessionsCommand identifies the caller and its own session.
type ListSessionsCommand struct {
	UserID           int64
	CurrentSessionID int64
}

// RenameSessionCommand re-labels an active device (DATABASE.md §4.4
// device_name).
type RenameSessionCommand struct {
	UserID     int64
	SessionID  int64
	DeviceName string
}

// LogoutCommand revokes the caller's current session. UserID and SessionID
// both come from the access token (API.md §4.5, SESS-3).
type LogoutCommand struct {
	UserID    int64
	SessionID int64
}

// LogoutSessionCommand revokes a specific device session (API.md §4.8).
// SessionID is the target; ownership is enforced against UserID.
type LogoutSessionCommand struct {
	UserID    int64
	SessionID int64
}

// LogoutOtherSessionsCommand revokes every device except the caller's.
type LogoutOtherSessionsCommand struct {
	UserID    int64
	SessionID int64
}

// LogoutAllCommand revokes every device and bumps the global token version.
type LogoutAllCommand struct {
	UserID int64
}

// Reauth is the step-up re-confirmation payload for sensitive actions
// (SECURITY_SPEC.md AUTH-9). A current password or a fresh OTP re-confirms
// the principal; the chosen Method selects which.
type Reauth struct {
	Method   domain.LoginMethod // password | otp
	Password string
	OTPCode  string
}

// RequestPasswordResetCommand starts the forgot-password flow
// (SECURITY_SPEC.md §29 REC-1). The response is uniform regardless of whether
// the identifier exists, so the endpoint does not enumerate accounts.
type RequestPasswordResetCommand struct {
	IdentifierType domain.IdentifierType
	Identifier     string
	IPAddress      *string
	UserAgent      *string
}

// RequestPasswordResetResult carries the single-use reset token for the
// delivery layer. Token is empty when the identifier is unknown (no account
// to email); it must never be echoed in an API response.
type RequestPasswordResetResult struct {
	Token     string
	ExpiresIn int64
}

// ResetPasswordCommand completes a password reset (REC-1 identifier
// verification). Token is the single-use reset credential; NewPassword is
// validated against PASS-2.
type ResetPasswordCommand struct {
	Token       string
	NewPassword string
	IPAddress   *string
	UserAgent   *string
}

// ChangePasswordCommand changes the password of the authenticated caller,
// requiring step-up re-confirmation (AUTH-9). All other sessions are
// suspended (PASS-4).
type ChangePasswordCommand struct {
	UserID      int64
	SessionID   int64
	Reauth      Reauth
	NewPassword string
	IPAddress   *string
	UserAgent   *string
}

// RequestEmailChangeCommand begins an email change: step-up re-auth, then a
// verification token is issued for the new email (AUTH-9).
type RequestEmailChangeCommand struct {
	UserID    int64
	SessionID int64
	NewEmail  string
	Reauth    Reauth
	IPAddress *string
	UserAgent *string
}

// RequestEmailChangeResult carries the verification token for out-of-band
// delivery to the new email.
type RequestEmailChangeResult struct {
	Token     string
	ExpiresIn int64
}

// ConfirmEmailChangeCommand completes an email change by consuming the
// verification token.
type ConfirmEmailChangeCommand struct {
	Token     string
	IPAddress *string
	UserAgent *string
}

// RequestPhoneChangeCommand begins a phone change (AUTH-9 step-up).
type RequestPhoneChangeCommand struct {
	UserID    int64
	SessionID int64
	NewPhone  string
	Reauth    Reauth
	IPAddress *string
	UserAgent *string
}

// RequestPhoneChangeResult carries the verification token for out-of-band
// delivery to the new phone.
type RequestPhoneChangeResult struct {
	Token     string
	ExpiresIn int64
}

// ConfirmPhoneChangeCommand completes a phone change by consuming the
// verification token.
type ConfirmPhoneChangeCommand struct {
	Token     string
	IPAddress *string
	UserAgent *string
}

// DeleteAccountCommand soft-deletes the authenticated account (API.md §5.5),
// requiring step-up re-confirmation. All sessions are revoked and the global
// token version bumped; a grace period precedes the hard purge.
type DeleteAccountCommand struct {
	UserID    int64
	SessionID int64
	Reauth    Reauth
	IPAddress *string
	UserAgent *string
}

// RestoreAccountCommand reactivates a soft-deleted account within the grace
// period (DATABASE.md §4.1), gated by identifier verification via OTP
// (REC-1 hierarchy).
type RestoreAccountCommand struct {
	IdentifierType domain.IdentifierType
	Identifier     string
	OTPCode        string
	IPAddress      *string
	UserAgent      *string
}

// ListLoginHistoryCommand returns the caller's own login history (security
// review screen). Limit caps the page size.
type ListLoginHistoryCommand struct {
	UserID int64
	Limit  int
}

// LoginEventInfo is the public view of one login-history row.
type LoginEventInfo struct {
	ID         int64
	Method     string
	Success    bool
	NewDevice  bool
	DeviceID   string
	IPAddress  *string
	UserAgent  *string
	Identifier string
	CreatedAt  time.Time
}

// Deps is the constructor-injected dependency set for the auth service.
type Deps struct {
	Users       userdomain.UserRepository
	Credentials domain.CredentialRepository
	Sessions    domain.SessionRepository
	Hasher      domain.PasswordHasher
	Tokens      domain.TokenIssuer
	OTP         domain.OTPVerifier
	Throttle    domain.LoginThrottle
	Policy      domain.LoginPolicy
	Audit       domain.AuditLogger
	IDs         domain.IDGenerator
	TxBeginner  tx.Beginner
	Clock       clock.Clock
	// SessionIdleTimeout is the sliding idle window after which a session
	// expires (SESS-9); used by ExpireIdleSessions.
	SessionIdleTimeout time.Duration
	// SessionRetention is how long revoked/expired sessions are kept before
	// purge (DATABASE.md §4.4); used by PurgeRevokedSessions.
	SessionRetention time.Duration

	// Account-security dependencies (milestone 5).

	// AuthTokens stores single-use recovery/verification tokens (password
	// reset, email/phone change).
	AuthTokens domain.AuthTokenRepository
	// LoginHistory records per-login events for the security-review screen.
	LoginHistory domain.LoginHistoryRepository
	// Risk is the risk-based validation hook (AUTH-11). Inject
	// domain.PermissiveRisk() when richer signals are not yet wired.
	Risk domain.RiskEvaluator
	// Notifier surfaces security events to the account holder. Inject
	// domain.NoopNotifier() until the notification milestone.
	Notifier domain.SecurityNotifier
	// Verifier validates access tokens at the gateway (JWT-5). Inject the
	// concrete security.TokenFactory. Used by Authenticate.
	Verifier domain.TokenVerifier
	// PasswordResetTokenTTL is the lifetime of a password-reset token
	// (default 30m).
	PasswordResetTokenTTL time.Duration
	// ChangeVerificationTokenTTL is the lifetime of an email/phone-change
	// verification token (default 15m).
	ChangeVerificationTokenTTL time.Duration
	// DeletionGracePeriod is how long a soft-deleted account can be restored
	// before the hard purge (default 30d; API.md §5.5).
	DeletionGracePeriod time.Duration
}

type service struct {
	deps Deps
}

// New builds the auth application service (constructor injection only).
func New(deps Deps) Service { return &service{deps: deps} }

// now is a thin indirection over the injected clock for readability.
func (s *service) now() time.Time { return s.deps.Clock.Now() }
