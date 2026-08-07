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
	// ErrRefreshTokenInvalid means the presented refresh token is malformed,
	// unknown, expired, or belongs to a revoked session (API.md §4.4 →
	// 401 REFRESH_TOKEN_INVALID).
	ErrRefreshTokenInvalid = errors.New("auth: refresh token invalid")
	// ErrRefreshTokenReuse means a rotated-out token was presented — a theft
	// signal. All sessions for the user have been revoked (REFR-5 → 410
	// REFRESH_TOKEN_REUSE).
	ErrRefreshTokenReuse = errors.New("auth: refresh token reuse detected")
	// ErrSessionNotOwned means the session exists but belongs to another user.
	// A caller may only administer its own sessions (SECURITY_SPEC.md SESS-3,
	// API.md §4.8 → 403 NOT_SESSION_OWNER).
	ErrSessionNotOwned = errors.New("auth: session belongs to another user")
	// ErrRecoveryTokenInvalid means a recovery/verification token is malformed,
	// unknown, expired, already used, or for the wrong purpose (REC-6 → 400/410).
	ErrRecoveryTokenInvalid = errors.New("auth: recovery token invalid")
	// ErrStepUpRequired means the risk hook escalated the authentication and a
	// re-confirmation is required before the session is usable (AUTH-9/AUTH-11
	// → 403 STEP_UP_REQUIRED).
	ErrStepUpRequired = errors.New("auth: step-up re-confirmation required")
	// ErrAccountAlreadyDeleted means deletion was already requested (API.md §5.5
	// → 409 already scheduled).
	ErrAccountAlreadyDeleted = errors.New("auth: account already deleted")
	// ErrAccountRestoreExpired means the deletion grace window has passed and
	// the account can no longer be restored (DATABASE.md §4.1 retention).
	ErrAccountRestoreExpired = errors.New("auth: account restore window expired")
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
