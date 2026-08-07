package domain

import "errors"

// Domain error taxonomy (ENGINEERING.md §14.2). Sentinels for conditions
// callers branch on with errors.Is.
var (
	// ErrUserNotFound means no non-deleted account matches the lookup
	// (API.md §5.3: also used to hide existence of blocked users).
	ErrUserNotFound = errors.New("user: not found")
	// ErrIdentifierTaken means the phone/email already belongs to a non-deleted
	// account (API.md §4.1 → 409 IDENTIFIER_TAKEN).
	ErrIdentifierTaken = errors.New("user: identifier taken")
	// ErrUsernameTaken means the username is already held (API.md §4.1 → 409
	// USERNAME_TAKEN).
	ErrUsernameTaken = errors.New("user: username taken")
	// ErrAccountAlreadyDeleted means the account is already soft-deleted and a
	// deletion was already requested (API.md §5.5 → 409).
	ErrAccountAlreadyDeleted = errors.New("user: account already deleted")
	// ErrAccountRestoreExpired means the deletion grace window passed and the
	// account can no longer be restored (DATABASE.md §4.1 retention).
	ErrAccountRestoreExpired = errors.New("user: account restore window expired")
)
