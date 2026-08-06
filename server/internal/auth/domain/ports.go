package domain

import (
	"context"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/pkg/tx"
)

// CredentialRepository owns persistence for user_credentials (DATABASE.md §4.3).
type CredentialRepository interface {
	// Create inserts a credential within the given transaction.
	Create(ctx context.Context, dbtx tx.Tx, c *Credential) error
	// FindPassword returns the user's non-revoked password credential.
	FindPassword(ctx context.Context, userID int64) (*Credential, error)
}

// SessionRepository owns persistence for user_sessions (DATABASE.md §4.4).
type SessionRepository interface {
	// Create inserts a session within the given transaction.
	Create(ctx context.Context, dbtx tx.Tx, s *Session) error
	// FindByDeviceID returns the session row for a user+device (any state), or
	// ErrSessionNotFound when none exists. user_sessions enforces exactly one
	// row per (user_id, device_id), so login reuses the row rather than
	// inserting a duplicate (API.md §4.3 upsert/rotate).
	FindByDeviceID(ctx context.Context, userID int64, deviceID string) (*Session, error)
	// Update persists the mutable session fields (state, refresh family/hash,
	// last_active_at, ip, user_agent, push token).
	Update(ctx context.Context, dbtx tx.Tx, s *Session) error
}

// LoginMethod is a supported credential method for login (API.md §4.3).
type LoginMethod string

const (
	LoginMethodPassword LoginMethod = "password"
	LoginMethodOTP      LoginMethod = "otp"
	LoginMethodPasskey  LoginMethod = "passkey"
)

// LoginPolicy is the failed-login lockout policy (SECURITY_SPEC.md AUTH-5:
// 5 consecutive failures → 5-minute lockout). It is a domain rule shared by
// every credential method (PASS-6, OTP-3) and applied per identifier.
type LoginPolicy struct {
	MaxFailures     int
	LockoutDuration time.Duration
}

// DefaultLoginPolicy implements AUTH-5 exactly.
func DefaultLoginPolicy() LoginPolicy {
	return LoginPolicy{MaxFailures: 5, LockoutDuration: 5 * time.Minute}
}

// LoginThrottle tracks consecutive failed logins per identifier (AUTH-5).
// Implementations must be concurrency-safe; the identifier is the normalized
// value (E.164 / lowercased email) so all credential methods share one counter.
type LoginThrottle interface {
	// Failures returns the consecutive failure count for the identifier
	// (0 when none recorded or the window expired).
	Failures(ctx context.Context, identifier string) (int, error)
	// RecordFailure increments the consecutive-failure counter.
	RecordFailure(ctx context.Context, identifier string) error
	// Clear resets the counter after a successful login.
	Clear(ctx context.Context, identifier string) error
	// LockoutRemaining returns how long the identifier stays locked (0 when
	// not locked). "Locked" is defined as failures >= LoginPolicy.MaxFailures.
	LockoutRemaining(ctx context.Context, identifier string) (time.Duration, error)
}

// AuditEvent is a security-relevant event for the audit trail
// (SECURITY_SPEC.md AUTH-7, DATABASE.md §8.5).
type AuditEvent struct {
	ActorUserID *int64
	Action      string // e.g. "auth.login", "auth.login_failed"
	ResourceID  *int64 // e.g. the user id
	IPAddress   *string
	Details     map[string]string
}

// AuditLogger records security events. Adapters are best-effort: an audit
// outage must not break the business operation (AUTH-7).
type AuditLogger interface {
	Log(ctx context.Context, e AuditEvent) error
}

// PasswordHasher hashes and verifies passwords with a memory-hard KDF
// (SECURITY_SPEC.md PASS-1). Argon2id is preferred; bcrypt at an appropriate
// cost is acceptable.
type PasswordHasher interface {
	// Hash derives a salted PHC hash for a plaintext password.
	Hash(ctx context.Context, plaintext string) (PasswordHash, error)
	// Verify checks a plaintext password against a stored PHC hash using a
	// constant-time comparison. It returns false, nil for a mismatch.
	Verify(ctx context.Context, hash PasswordHash, plaintext string) (bool, error)
}

// TokenPair is the issued access + refresh credential for one session
// (ARCHITECTURE.md §10.2).
type TokenPair struct {
	AccessToken      string
	RefreshToken     string
	JTI              string
	AccessExpiresAt  time.Time
	RefreshExpiresAt time.Time
}

// TokenIssuer issues short-lived JWT access tokens and opaque refresh tokens.
type TokenIssuer interface {
	// IssuePair mints an access token bound to the session/device and a fresh
	// opaque refresh token for the given session.
	IssuePair(ctx context.Context, sessionID, userID int64, deviceID string, now time.Time) (TokenPair, error)
}

// OTPVerifier verifies a one-time passcode for an identifier (OTP-1: 6 digits,
// single-use, 300 s TTL, consumed atomically). The delivery layer never sees
// the code; the real implementation stores hashed codes in Redis.
type OTPVerifier interface {
	// Verify consumes the code for the identifier. It returns ErrOTPInvalid or
	// ErrOTPExpired on failure; the code is consumed on success only.
	Verify(ctx context.Context, identifier Identifier, code string) error
}

// IDGenerator mints snowflake-style 64-bit ids (internal/platform/idgen).
type IDGenerator interface {
	NextID() (int64, error)
}
