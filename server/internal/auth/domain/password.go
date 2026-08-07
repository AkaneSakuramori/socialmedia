package domain

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
)

// passwordMinLen is the registration floor per SECURITY_SPEC.md PASS-2.
const passwordMinLen = 8

// PasswordHash is a verifiable password hash encoded as a PHC string
// (SECURITY_SPEC.md PASS-1: Argon2id/bcrypt, unique salt per user). It is
// stored inside user_credentials.credential_data as {"hash": "<phc>"}.
type PasswordHash struct{ value string }

// NewPasswordHash wraps an already-computed PHC string.
func NewPasswordHash(phc string) PasswordHash { return PasswordHash{value: phc} }

func (h PasswordHash) String() string { return h.value }

// IsZero reports whether the hash is unset.
func (h PasswordHash) IsZero() bool { return h.value == "" }

// ValidatePassword enforces the strength policy (PASS-2): minimum length, no
// match against the identifier, no trivially-weak values. It returns
// *ValidationError{Field: "password"} on failure.
func ValidatePassword(password, identifier string) error {
	if len(password) < passwordMinLen {
		return &ValidationError{Field: "password", Reason: "too_short"}
	}
	if len(password) > 1024 {
		return &ValidationError{Field: "password", Reason: "too_long"}
	}
	lower := strings.ToLower(password)
	if identifier != "" && strings.Contains(lower, strings.ToLower(identifier)) {
		return &ValidationError{Field: "password", Reason: "contains_identifier"}
	}
	switch strings.ToLower(password) {
	case "password", "password123", "12345678", "qwertyui", "letmein1":
		return &ValidationError{Field: "password", Reason: "too_common"}
	}
	return nil
}

// HashOpaqueToken returns the SHA-256 hex digest of an opaque token
// (SECURITY_SPEC.md REFR-2: refresh tokens are stored only as hashes).
func HashOpaqueToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// OpaqueTokenLen is the base64url (unpadded) length of a 32-byte token.
const OpaqueTokenLen = 43

// IsOpaqueTokenShape reports whether a value has the expected shape of an
// issued refresh token, used to reject malformed input before lookup.
func IsOpaqueTokenShape(token string) bool {
	if len(token) != OpaqueTokenLen {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil
}
