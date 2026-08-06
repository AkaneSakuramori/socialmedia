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
	UserID          int64
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
}

type service struct {
	deps Deps
}

// New builds the auth application service (constructor injection only).
func New(deps Deps) Service { return &service{deps: deps} }

// now is a thin indirection over the injected clock for readability.
func (s *service) now() time.Time { return s.deps.Clock.Now() }
