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
