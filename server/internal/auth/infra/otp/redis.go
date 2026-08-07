// Package otp implements the OTPVerifier port over Redis (OTP-1: 6 digits,
// single-use, 300 s TTL, consumed atomically). Codes are stored hashed, never
// plaintext, and consumed with an atomic GETDEL so a code can verify at most
// once even under concurrent attempts.
package otp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/internal/auth/domain"
	"github.com/redis/go-redis/v9"
)

// keyPrefix scopes OTP keys so they never collide with other Redis users.
const keyPrefix = "auth:otp:"

// codeTTL is the OTP lifetime (OTP-1: 300 s).
const codeTTL = 5 * time.Minute

// Verifier is the Redis-backed domain.OTPVerifier.
type Verifier struct {
	client *redis.Client
}

// New builds the verifier over the shared Redis client.
func New(client *redis.Client) *Verifier {
	return &Verifier{client: client}
}

// Verify consumes the code for the identifier (atomic GETDEL). It returns
// ErrOTPInvalid when the stored code does not match, and ErrOTPExpired when the
// code is no longer present (TTL passed or already consumed).
func (v *Verifier) Verify(ctx context.Context, ident domain.Identifier, code string) error {
	key := keyPrefix + string(ident.Type) + ":" + ident.Value
	stored, err := v.client.GetDel(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return domain.ErrOTPExpired
	}
	if err != nil {
		return err
	}
	if !constantTimeEqual(stored, hashCode(code)) {
		return domain.ErrOTPInvalid
	}
	return nil
}

// Store persists a hashed code for the identifier for codeTTL (used by the OTP
// delivery flow, which is wired by the auth milestone; kept here so the
// verifier has its write side).
func (v *Verifier) Store(ctx context.Context, ident domain.Identifier, code string) error {
	key := keyPrefix + string(ident.Type) + ":" + ident.Value
	return v.client.Set(ctx, key, hashCode(code), codeTTL).Err()
}

// hashCode derives the at-rest form of a code.
func hashCode(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// constantTimeEqual compares two hex strings without leaking the match result
// via timing. Lengths are public (both are SHA-256 hex), so the guard only
// protects against an early-exit on prefix mismatch.
func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := 0; i < len(a); i++ {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}
