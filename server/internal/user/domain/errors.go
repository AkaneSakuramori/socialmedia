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
)
