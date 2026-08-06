package domain

import (
	"errors"
	"fmt"
	"time"
)

// Domain errors for authentication (API.md §4.3, Appendix A). Delivery maps
// these to the wire contract exactly once at the boundary (ENGINEERING.md §14).

var (
	// ErrInvalidCredentials means the presented credential (password, OTP,
	// passkey) did not verify (API.md §4.3 → 401 INVALID_CREDENTIALS).
	ErrInvalidCredentials = errors.New("auth: invalid credentials")
	// ErrAccountSuspended means the account is suspended and cannot
	// authenticate (SECURITY_SPEC.md AUTH-8 → 403 ACCOUNT_SUSPENDED).
	ErrAccountSuspended = errors.New("auth: account suspended")
	// ErrUnsupportedLoginMethod means the requested credential method is not
	// implemented yet (passkey) or is unknown.
	ErrUnsupportedLoginMethod = errors.New("auth: unsupported login method")
	// ErrSessionNotFound means no session row matches the query.
	ErrSessionNotFound = errors.New("auth: session not found")
)

// AccountLockedError reports an active lockout (SECURITY_SPEC.md AUTH-5 →
// 423 ACCOUNT_LOCKED). Remaining carries the seconds the caller should wait
// (delivery surfaces it as Retry-After).
type AccountLockedError struct {
	Remaining time.Duration
}

// ErrAccountLocked is the sentinel for errors.Is matching.
var ErrAccountLocked = &AccountLockedError{}

func (e *AccountLockedError) Error() string {
	return fmt.Sprintf("auth: account locked, retry in %s", e.Remaining)
}
