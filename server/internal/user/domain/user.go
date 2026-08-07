// Package user holds the identity root aggregate of the platform
// (ARCHITECTURE.md §9.1, DATABASE.md §4.1). Pure domain: entities, value
// objects, and repository ports. It imports only stdlib and the dependency-free
// pkg packages (ENGINEERING.md §3.5).
package domain

import (
	"context"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/pkg/tx"
)

// AccountState is the lifecycle state of an account (DATABASE.md §4.1).
type AccountState string

const (
	AccountActive    AccountState = "active"
	AccountSuspended AccountState = "suspended"
	AccountDeleted   AccountState = "deleted"
)

// PrimaryIdentifier names which identifier is primary for an account.
type PrimaryIdentifier string

const (
	PrimaryPhone    PrimaryIdentifier = "phone"
	PrimaryEmail    PrimaryIdentifier = "email"
	PrimaryUsername PrimaryIdentifier = "username"
)

// User is the account aggregate root. Phone/email/username follow DATABASE.md:
// separate nullable unique columns; username is immutable after creation.
type User struct {
	ID                int64
	Username          *string
	DisplayName       string
	PhoneNumber       *string
	Email             *string
	AccountState      AccountState
	PrimaryIdentifier PrimaryIdentifier
	// TokenVersion is the account's global token version (users.token_version,
	// SECURITY_SPEC.md SESS-6). Sign-out-everywhere bumps it so outstanding
	// access tokens — which carry this value — fail at the gateway (JWT-5).
	TokenVersion int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// UserRepository owns persistence for the User aggregate (ENGINEERING.md §6).
// Transactional methods take a tx.Tx so use-cases span aggregates atomically.
type UserRepository interface {
	// Create inserts a new user within the given transaction.
	Create(ctx context.Context, dbtx tx.Tx, u *User) error
	// FindByID loads a user by id, or ErrUserNotFound.
	FindByID(ctx context.Context, id int64) (*User, error)
	// ListByIDs loads the live (non-deleted) users with the given ids. Unknown
	// ids are silently omitted; the caller decides whether that is an error.
	// Used by the chat module to resolve participant display names in bulk
	// (direct titles, group member previews).
	ListByIDs(ctx context.Context, ids []int64) ([]User, error)
	// FindByPhone loads an account by normalized E.164 phone.
	FindByPhone(ctx context.Context, phone string) (*User, error)
	// FindByEmail loads an account by normalized lowercase email.
	FindByEmail(ctx context.Context, email string) (*User, error)
	// PhoneTaken reports whether the normalized phone already belongs to any
	// non-deleted account (API.md §4.1 → 409 IDENTIFIER_TAKEN).
	PhoneTaken(ctx context.Context, phone string) (bool, error)
	// EmailTaken reports whether the normalized email already belongs to any
	// non-deleted account (API.md §4.1 → 409 IDENTIFIER_TAKEN).
	EmailTaken(ctx context.Context, email string) (bool, error)
	// UsernameTaken reports whether the username is held by a non-deleted
	// account (API.md §4.1 → 409 USERNAME_TAKEN).
	UsernameTaken(ctx context.Context, username string) (bool, error)
	// BumpTokenVersion atomically increments the account's global token
	// version and returns the new value (SESS-6: sign-out-everywhere). Callers
	// do this in the same transaction that revokes the sessions.
	BumpTokenVersion(ctx context.Context, dbtx tx.Tx, userID int64) (int64, error)
	// SetEmail atomically assigns a verified email to the account, returning
	// ErrIdentifierTaken when the value is already claimed by another account
	// (race-safe: the unique index is the final arbiter). Mirrors SetPhone.
	SetEmail(ctx context.Context, dbtx tx.Tx, userID int64, email string) error
	// SetPhone atomically assigns a verified E.164 phone to the account,
	// returning ErrIdentifierTaken on a concurrent claim.
	SetPhone(ctx context.Context, dbtx tx.Tx, userID int64, phone string) error
	// MarkDeleted soft-deletes the account (API.md §5.5: account_state='deleted'
	// + deleted_at), returning ErrAccountAlreadyDeleted when already deleted.
	MarkDeleted(ctx context.Context, dbtx tx.Tx, userID int64, deletedAt time.Time) error
	// Restore reactivates a deleted account whose deleted_at falls within the
	// grace window (deleted_at >= cutoff). Returns ErrAccountRestoreExpired
	// when the window has passed or the account is not deleted.
	Restore(ctx context.Context, dbtx tx.Tx, userID int64, graceCutoff time.Time) error
	// FindDeletedByPhone loads a deleted account by normalized E.164 phone
	// (account-recovery lookup), or ErrUserNotFound.
	FindDeletedByPhone(ctx context.Context, phone string) (*User, error)
	// FindDeletedByEmail loads a deleted account by normalized email, or
	// ErrUserNotFound.
	FindDeletedByEmail(ctx context.Context, email string) (*User, error)
	// PurgeDeleted hard-deletes accounts deleted before the cutoff and their
	// dependent rows (DATABASE.md §4.1 retention: soft delete → purge worker).
	// It returns the number of accounts removed.
	PurgeDeleted(ctx context.Context, cutoff time.Time) (int64, error)
}
