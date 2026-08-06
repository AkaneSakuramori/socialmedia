package application

import (
	"context"
	"fmt"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/internal/auth/domain"
	userdomain "github.com/AkaneSakuramori/socialmedia/server/internal/user/domain"
	"github.com/AkaneSakuramori/socialmedia/server/pkg/tx"
)

// Register implements Service.Register (API.md §4.1): verify the OTP, check
// uniqueness, then create the user, its optional password credential, and the
// first session with a token pair — all in one transaction
// (DATABASE.md §10: "single insert + session insert in one transaction").
func (s *service) Register(ctx context.Context, cmd RegisterCommand) (*RegisterResult, error) {
	ident, err := domain.NewIdentifier(cmd.IdentifierType, cmd.Identifier)
	if err != nil {
		return nil, err
	}
	display, err := userdomain.NewDisplayName(cmd.DisplayName)
	if err != nil {
		return nil, err
	}
	var username *userdomain.Username
	if cmd.Username != nil {
		u, err := userdomain.NewUsername(*cmd.Username)
		if err != nil {
			return nil, err
		}
		username = &u
	}

	var password *string
	if cmd.Password != nil {
		if err := domain.ValidatePassword(*cmd.Password, ident.Value); err != nil {
			return nil, err
		}
		p := *cmd.Password
		password = &p
	}

	// OTP is checked and consumed atomically before any account state exists
	// (API.md §4.1 security; SECURITY_SPEC.md OTP-1).
	if err := s.deps.OTP.Verify(ctx, ident, cmd.OTPCode); err != nil {
		return nil, err
	}

	// Uniqueness checks (API.md §4.1 → 409 IDENTIFIER_TAKEN / USERNAME_TAKEN).
	taken, err := s.identifierTaken(ctx, ident)
	if err != nil {
		return nil, err
	}
	if taken {
		return nil, userdomain.ErrIdentifierTaken
	}
	if username != nil {
		held, err := s.deps.Users.UsernameTaken(ctx, username.String())
		if err != nil {
			return nil, err
		}
		if held {
			return nil, userdomain.ErrUsernameTaken
		}
	}

	now := s.now()
	dbtx, err := s.deps.TxBeginner.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("auth: begin tx: %w", err)
	}
	defer dbtx.Rollback(ctx) // no-op once committed

	user, err := s.createUser(ctx, dbtx, ident, display, username, now)
	if err != nil {
		return nil, err
	}
	if password != nil {
		if err := s.createPasswordCredential(ctx, dbtx, user.ID, *password, now); err != nil {
			return nil, err
		}
	}

	session, pair, err := s.createSession(ctx, dbtx, user.ID, cmd.Device, cmd.IPAddress, cmd.UserAgent, now)
	if err != nil {
		return nil, err
	}

	if err := dbtx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("auth: commit registration: %w", err)
	}
	return &RegisterResult{User: *user, Session: *session, TokenPair: pair}, nil
}

// identifierTaken maps an identifier to the underlying user-repository check.
func (s *service) identifierTaken(ctx context.Context, ident domain.Identifier) (bool, error) {
	switch ident.Type {
	case domain.IdentifierPhone:
		return s.deps.Users.PhoneTaken(ctx, ident.Value)
	case domain.IdentifierEmail:
		return s.deps.Users.EmailTaken(ctx, ident.Value)
	default:
		return false, fmt.Errorf("auth: unsupported identifier type %q", ident.Type)
	}
}

func (s *service) createUser(ctx context.Context, dbtx tx.Tx, ident domain.Identifier, display userdomain.DisplayName, username *userdomain.Username, now time.Time) (*userdomain.User, error) {
	id, err := s.deps.IDs.NextID()
	if err != nil {
		return nil, fmt.Errorf("auth: user id: %w", err)
	}
	u := &userdomain.User{
		ID:                id,
		DisplayName:       display.String(),
		AccountState:      userdomain.AccountActive,
		PrimaryIdentifier: userdomain.PrimaryIdentifier(ident.Type),
	}
	if username != nil {
		un := username.String()
		u.Username = &un
	}
	switch ident.Type {
	case domain.IdentifierPhone:
		u.PhoneNumber = &ident.Value
	case domain.IdentifierEmail:
		u.Email = &ident.Value
	}
	return u, s.deps.Users.Create(ctx, dbtx, u)
}

func (s *service) createPasswordCredential(ctx context.Context, dbtx tx.Tx, userID int64, password string, now time.Time) error {
	hash, err := s.deps.Hasher.Hash(ctx, password)
	if err != nil {
		return fmt.Errorf("auth: hash password: %w", err)
	}
	credID, err := s.deps.IDs.NextID()
	if err != nil {
		return fmt.Errorf("auth: credential id: %w", err)
	}
	cred, err := domain.NewPasswordCredential(credID, userID, hash, now)
	if err != nil {
		return fmt.Errorf("auth: build credential: %w", err)
	}
	if err := s.deps.Credentials.Create(ctx, dbtx, cred); err != nil {
		return fmt.Errorf("auth: store credential: %w", err)
	}
	return nil
}

func (s *service) createSession(ctx context.Context, dbtx tx.Tx, userID int64, device domain.DeviceInfo, ip, userAgent *string, now time.Time) (*domain.Session, domain.TokenPair, error) {
	sessionID, err := s.deps.IDs.NextID()
	if err != nil {
		return nil, domain.TokenPair{}, fmt.Errorf("auth: session id: %w", err)
	}
	pair, err := s.deps.Tokens.IssuePair(ctx, sessionID, userID, device.DeviceID, now)
	if err != nil {
		return nil, domain.TokenPair{}, fmt.Errorf("auth: issue tokens: %w", err)
	}
	sess := &domain.Session{
		ID:               sessionID,
		UserID:           userID,
		Device:           device,
		RefreshTokenHash: domain.HashOpaqueToken(pair.RefreshToken),
		IPAddress:        ip,
		UserAgent:        userAgent,
		LastActiveAt:     now,
		State:            domain.SessionActive,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := s.deps.Sessions.Create(ctx, dbtx, sess); err != nil {
		return nil, domain.TokenPair{}, fmt.Errorf("auth: store session: %w", err)
	}
	return sess, pair, nil
}
