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
}
