// Package application implements the auth module's use-cases (ENGINEERING.md
// §7, §10). The exported Service interface is the only entry point other
// modules (delivery, or user/chat applications) may call. Services hold small
// ports from the domain layer, injected at the composition root; no I/O and no
// concrete adapters live here.
package application

import (
	"context"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/internal/auth/domain"
	userdomain "github.com/AkaneSakuramori/socialmedia/server/internal/user/domain"
	"github.com/AkaneSakuramori/socialmedia/server/pkg/clock"
	"github.com/AkaneSakuramori/socialmedia/server/pkg/tx"
)

// Service is the exported auth application service interface. Other modules and
// the delivery layer depend on this, never on the concrete service.
type Service interface {
	// Register creates a new account, verifies the OTP, and returns the first
	// session and token pair (API.md §4.1).
	Register(ctx context.Context, cmd RegisterCommand) (*RegisterResult, error)
	// Login authenticates an existing user (password or OTP) and returns the
	// session bound to the device with a fresh token pair (API.md §4.3).
	// Failed attempts are throttled per identifier (AUTH-5).
	Login(ctx context.Context, cmd LoginCommand) (*LoginResult, error)
}

// LoginCommand is the validated input for authentication (API.md §4.3).
// Password is used when Method == password; OTPCode when Method == otp.
type LoginCommand struct {
	IdentifierType domain.IdentifierType
	Identifier     string
	Method         domain.LoginMethod
	Password       string
	OTPCode        string
	Device         domain.DeviceInfo
	IPAddress      *string
	UserAgent      *string
}

// LoginResult is the outcome of authentication: the account, its device
// session, and the issued token pair (same shape as API.md §4.1/§4.3).
type LoginResult struct {
	User      userdomain.User
	Session   domain.Session
	TokenPair domain.TokenPair
}

// RegisterCommand is the validated input for account creation (API.md §4.1).
// Password is optional: passkey/OTP-only accounts are allowed by the spec.
type RegisterCommand struct {
	IdentifierType domain.IdentifierType
	Identifier     string
	OTPCode        string
	DisplayName    string
	Username       *string
	Password       *string
	Device         domain.DeviceInfo
	IPAddress      *string
	UserAgent      *string
}

// RegisterResult is the outcome of account creation: the account, its first
// session, and the issued token pair.
type RegisterResult struct {
	User      userdomain.User
	Session   domain.Session
	TokenPair domain.TokenPair
}

// Deps is the constructor-injected dependency set for the auth service.
type Deps struct {
	Users       userdomain.UserRepository
	Credentials domain.CredentialRepository
	Sessions    domain.SessionRepository
	Hasher      domain.PasswordHasher
	Tokens      domain.TokenIssuer
	OTP         domain.OTPVerifier
	Throttle    domain.LoginThrottle
	Policy      domain.LoginPolicy
	Audit       domain.AuditLogger
	IDs         domain.IDGenerator
	TxBeginner  tx.Beginner
	Clock       clock.Clock
}

type service struct {
	deps Deps
}

// New builds the auth application service (constructor injection only).
func New(deps Deps) Service { return &service{deps: deps} }

// now is a thin indirection over the injected clock for readability.
func (s *service) now() time.Time { return s.deps.Clock.Now() }
