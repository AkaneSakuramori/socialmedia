package domain

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

// TokenPurpose is the purpose of a single-use recovery/verification token.
type TokenPurpose string

const (
	// PurposePasswordReset marks a forgot-password recovery token (REC-1:
	// identifier verification).
	PurposePasswordReset TokenPurpose = "password_reset"
	// PurposeEmailChange marks a pending email-verification token.
	PurposeEmailChange TokenPurpose = "email_change"
	// PurposePhoneChange marks a pending phone-verification token.
	PurposePhoneChange TokenPurpose = "phone_change"
)

// AuthToken is a single-use recovery/verification token. Only TokenHash is
// persisted (the REFR-2 pattern: opaque tokens are stored as SHA-256 hashes).
// Data carries the pending change (e.g. {"email": "..."}) for identifier-change
// purposes. Consume is atomic and TTL-bounded (SECURITY_SPEC.md REC-6).
type AuthToken struct {
	ID        int64
	UserID    int64
	Purpose   TokenPurpose
	TokenHash string
	Data      []byte // JSONB payload
	ExpiresAt time.Time
	UsedAt    *time.Time
	CreatedAt time.Time
}

// pendingValue reads a string field from the token's JSONB data payload.
func (t *AuthToken) pendingValue(key string) (string, error) {
	var m map[string]string
	if len(t.Data) == 0 {
		return "", fmt.Errorf("auth: token data empty")
	}
	if err := json.Unmarshal(t.Data, &m); err != nil {
		return "", fmt.Errorf("auth: token data corrupt: %w", err)
	}
	v := m[key]
	if v == "" {
		return "", fmt.Errorf("auth: token data missing %q", key)
	}
	return v, nil
}

// PendingEmail returns the verified-but-pending email for an email_change
// token.
func (t *AuthToken) PendingEmail() (string, error) { return t.pendingValue("email") }

// PendingPhone returns the verified-but-pending phone for a phone_change token.
func (t *AuthToken) PendingPhone() (string, error) { return t.pendingValue("phone") }

// GenerateOpaqueToken mints a 256-bit opaque token (43-char base64url, the same
// shape as refresh tokens, REFR-1) for reset/verification purposes. The raw
// value is handed to the delivery layer once; only HashOpaqueToken is stored.
func GenerateOpaqueToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
