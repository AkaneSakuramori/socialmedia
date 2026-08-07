package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/internal/auth/domain"
	userdomain "github.com/AkaneSakuramori/socialmedia/server/internal/user/domain"
	"github.com/AkaneSakuramori/socialmedia/server/pkg/tx"
)

// reauth enforces step-up re-confirmation (AUTH-9) for sensitive actions. The
// caller must present either their current password or a fresh OTP bound to
// their primary identifier. A wrong credential returns ErrInvalidCredentials;
// a suspended/deleted account cannot re-verify.
func (s *service) reauth(ctx context.Context, user *userdomain.User, r Reauth) error {
	switch r.Method {
	case domain.LoginMethodPassword:
		cred, err := s.deps.Credentials.FindPassword(ctx, user.ID)
		if err != nil {
			if errors.Is(err, userdomain.ErrUserNotFound) {
				s.dummyVerify(ctx, r.Password)
				return domain.ErrInvalidCredentials
			}
			return fmt.Errorf("auth: find credential: %w", err)
		}
		hash, err := cred.PasswordHash()
		if err != nil {
			s.dummyVerify(ctx, r.Password)
			return domain.ErrInvalidCredentials
		}
		ok, err := s.deps.Hasher.Verify(ctx, hash, r.Password)
		if err != nil {
			return fmt.Errorf("auth: verify password: %w", err)
		}
		if !ok {
			return domain.ErrInvalidCredentials
		}
		return nil

	case domain.LoginMethodOTP:
		ident, err := primaryIdentifier(user)
		if err != nil {
			return err
		}
		if err := s.deps.OTP.Verify(ctx, ident, r.OTPCode); err != nil {
			if errors.Is(err, domain.ErrOTPInvalid) || errors.Is(err, domain.ErrOTPExpired) {
				return domain.ErrInvalidCredentials
			}
			return fmt.Errorf("auth: verify otp: %w", err)
		}
		return nil

	default:
		return &domain.ValidationError{Field: "reauth", Reason: "unsupported_method"}
	}
}

// primaryIdentifier builds the OTP verification identifier for the account's
// primary identifier (REC-1: identifier verification via OTP).
func primaryIdentifier(user *userdomain.User) (domain.Identifier, error) {
	switch user.PrimaryIdentifier {
	case userdomain.PrimaryEmail:
		if user.Email == nil {
			return domain.Identifier{}, &domain.ValidationError{Field: "identifier", Reason: "no_email"}
		}
		return domain.NewIdentifier(domain.IdentifierEmail, *user.Email)
	default:
		if user.PhoneNumber == nil {
			return domain.Identifier{}, &domain.ValidationError{Field: "identifier", Reason: "no_phone"}
		}
		return domain.NewIdentifier(domain.IdentifierPhone, *user.PhoneNumber)
	}
}

// issueAuthToken mints a single-use recovery/verification token, persists only
// its hash, and returns the plaintext for out-of-band delivery (REFR-2 pattern).
func (s *service) issueAuthToken(ctx context.Context, dbtx tx.Tx, userID int64, purpose domain.TokenPurpose, data []byte, ttl time.Duration, now time.Time) (string, time.Time, error) {
	raw, err := domain.GenerateOpaqueToken()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("auth: generate token: %w", err)
	}
	expiresAt := now.Add(ttl)
	tok := &domain.AuthToken{
		UserID:    userID,
		Purpose:   purpose,
		TokenHash: domain.HashOpaqueToken(raw),
		Data:      data,
		ExpiresAt: expiresAt,
		CreatedAt: now,
	}
	if err := s.deps.AuthTokens.Create(ctx, dbtx, tok); err != nil {
		return "", time.Time{}, fmt.Errorf("auth: store recovery token: %w", err)
	}
	return raw, expiresAt, nil
}

// setPassword hashes and stores a new password credential, replacing the
// existing one in place (DATABASE.md §4.3 UNIQUE(user_id, method, provider)).
func (s *service) setPassword(ctx context.Context, dbtx tx.Tx, userID int64, plaintext string) error {
	hash, err := s.deps.Hasher.Hash(ctx, plaintext)
	if err != nil {
		return fmt.Errorf("auth: hash password: %w", err)
	}
	return s.deps.Credentials.ReplacePassword(ctx, dbtx, userID, hash)
}

// expiresInSeconds returns seconds until t, clamped to >= 0.
func expiresInSeconds(now, t time.Time) int64 {
	d := t.Sub(now).Seconds()
	if d < 0 {
		return 0
	}
	return int64(d)
}

// notify surfaces a security event to the account holder. Best-effort
// (SECURITY_SPEC.md: a notification outage must not break the operation).
func (s *service) notify(ctx context.Context, userID int64, event string, details map[string]string) {
	if s.deps.Notifier == nil {
		return
	}
	_ = s.deps.Notifier.Notify(ctx, userID, event, details)
}
