package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/internal/auth/domain"
	userdomain "github.com/AkaneSakuramori/socialmedia/server/internal/user/domain"
)

// dummyPasswordHash is a real Argon2id PHC used only for timing equalization:
// when the identifier is unknown or has no password credential, we still run a
// full Verify against it so account existence is not leaked through response
// timing (OWASP A05:2021 / improper error handling, enumeration).
var dummyPasswordHash = domain.NewPasswordHash("$argon2id$v=19$m=65536,t=3,p=4$1BIeyDknCygKrbCaEV7SXA$VKBYrp2a8a2qRqXFvrzKHym4wKtYjowTz2Yx81UA30s")

// Login authenticates an existing user and returns the session bound to the
// device plus a fresh token pair (API.md §4.3). Failed attempts are throttled
// per identifier with lockout backoff (SECURITY_SPEC.md AUTH-5); the account
// must be active (AUTH-8).
func (s *service) Login(ctx context.Context, cmd LoginCommand) (*LoginResult, error) {
	ident, err := domain.NewIdentifier(cmd.IdentifierType, cmd.Identifier)
	if err != nil {
		return nil, err
	}
	switch cmd.Method {
	case domain.LoginMethodPassword, domain.LoginMethodOTP:
	case domain.LoginMethodPasskey:
		// WebAuthn login lands in a later milestone; fail closed for now.
		return nil, domain.ErrUnsupportedLoginMethod
	default:
		return nil, &domain.ValidationError{Field: "method", Reason: "unsupported"}
	}
	if err := domain.ValidateDeviceID(cmd.Device.DeviceID); err != nil {
		return nil, err
	}

	key := ident.Value

	// Lockout gate (AUTH-5): short-circuit before any credential work. The
	// failure counter expires with the lockout window, so Remaining > 0
	// exactly when the identifier is locked.
	remaining, err := s.deps.Throttle.LockoutRemaining(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("auth: lockout lookup: %w", err)
	}
	if remaining > 0 {
		s.audit(ctx, nil, "auth.login_locked", nil, cmd.IPAddress,
			map[string]string{"identifier": key, "method": string(cmd.Method)})
		return nil, &domain.AccountLockedError{Remaining: remaining}
	}

	user, err := s.findUser(ctx, ident)
	if err != nil {
		if !errors.Is(err, userdomain.ErrUserNotFound) {
			return nil, fmt.Errorf("auth: find user: %w", err)
		}
		s.dummyVerify(ctx, cmd.Password)
		if recErr := s.recordFailure(ctx, key); recErr != nil {
			return nil, recErr
		}
		s.audit(ctx, nil, "auth.login_failed", nil, cmd.IPAddress,
			map[string]string{"identifier": key, "method": string(cmd.Method)})
		return nil, domain.ErrInvalidCredentials
	}

	// Account state (AUTH-8). Deleted accounts are reported as invalid
	// credentials to avoid distinguishing them from never-existing accounts.
	switch user.AccountState {
	case userdomain.AccountSuspended:
		s.audit(ctx, &user.ID, "auth.login_blocked", &user.ID, cmd.IPAddress,
			map[string]string{"reason": "suspended"})
		return nil, domain.ErrAccountSuspended
	case userdomain.AccountDeleted:
		s.dummyVerify(ctx, cmd.Password)
		if recErr := s.recordFailure(ctx, key); recErr != nil {
			return nil, recErr
		}
		s.audit(ctx, nil, "auth.login_failed", nil, cmd.IPAddress,
			map[string]string{"identifier": key, "method": string(cmd.Method)})
		return nil, domain.ErrInvalidCredentials
	}

	ok, err := s.verifyCredential(ctx, user, ident, cmd)
	if err != nil {
		return nil, err
	}
	if !ok {
		if recErr := s.recordFailure(ctx, key); recErr != nil {
			return nil, recErr
		}
		s.audit(ctx, &user.ID, "auth.login_failed", &user.ID, cmd.IPAddress,
			map[string]string{"method": string(cmd.Method)})
		return nil, domain.ErrInvalidCredentials
	}

	// Success: clear the failure counter, then bind/rotate the device session.
	if err := s.deps.Throttle.Clear(ctx, key); err != nil {
		return nil, fmt.Errorf("auth: clear lockout: %w", err)
	}

	now := s.now()
	session, pair, err := s.upsertSession(ctx, user.ID, cmd.Device, cmd.IPAddress, cmd.UserAgent, now)
	if err != nil {
		return nil, err
	}

	s.audit(ctx, &user.ID, "auth.login", &user.ID, cmd.IPAddress,
		map[string]string{"method": string(cmd.Method), "device_id": cmd.Device.DeviceID})
	return &LoginResult{User: *user, Session: *session, TokenPair: pair}, nil
}

// findUser maps an identifier to the account lookup.
func (s *service) findUser(ctx context.Context, ident domain.Identifier) (*userdomain.User, error) {
	switch ident.Type {
	case domain.IdentifierPhone:
		return s.deps.Users.FindByPhone(ctx, ident.Value)
	case domain.IdentifierEmail:
		return s.deps.Users.FindByEmail(ctx, ident.Value)
	default:
		return nil, fmt.Errorf("auth: unsupported identifier type %q", ident.Type)
	}
}

// verifyCredential checks the presented credential for the account. It returns
// (false, nil) for a genuine credential mismatch (account unknown, no
// credential, wrong password/OTP) and an error only for infrastructure faults.
func (s *service) verifyCredential(ctx context.Context, user *userdomain.User, ident domain.Identifier, cmd LoginCommand) (bool, error) {
	switch cmd.Method {
	case domain.LoginMethodOTP:
		if err := s.deps.OTP.Verify(ctx, ident, cmd.OTPCode); err != nil {
			if errors.Is(err, domain.ErrOTPInvalid) || errors.Is(err, domain.ErrOTPExpired) {
				return false, nil
			}
			return false, fmt.Errorf("auth: verify otp: %w", err)
		}
		return true, nil

	case domain.LoginMethodPassword:
		cred, err := s.deps.Credentials.FindPassword(ctx, user.ID)
		if err != nil {
			if errors.Is(err, userdomain.ErrUserNotFound) {
				s.dummyVerify(ctx, cmd.Password)
				return false, nil
			}
			return false, fmt.Errorf("auth: find credential: %w", err)
		}
		hash, err := cred.PasswordHash()
		if err != nil {
			s.dummyVerify(ctx, cmd.Password)
			return false, nil
		}
		ok, err := s.deps.Hasher.Verify(ctx, hash, cmd.Password)
		if err != nil {
			return false, fmt.Errorf("auth: verify password: %w", err)
		}
		return ok, nil

	default:
		return false, domain.ErrUnsupportedLoginMethod
	}
}

// upsertSession binds a login to the device (API.md §4.3): reuse the existing
// (user_id, device_id) row by rotating its refresh token and family, or create
// a new one. Everything happens in one transaction (DATABASE.md §10).
func (s *service) upsertSession(ctx context.Context, userID int64, device domain.DeviceInfo, ip, userAgent *string, now time.Time) (*domain.Session, domain.TokenPair, error) {
	dbtx, err := s.deps.TxBeginner.Begin(ctx)
	if err != nil {
		return nil, domain.TokenPair{}, fmt.Errorf("auth: begin tx: %w", err)
	}
	defer dbtx.Rollback(ctx) // no-op once committed

	existing, err := s.deps.Sessions.FindByDeviceID(ctx, userID, device.DeviceID)
	if err != nil && !errors.Is(err, domain.ErrSessionNotFound) {
		return nil, domain.TokenPair{}, fmt.Errorf("auth: find session: %w", err)
	}

	var session *domain.Session
	if existing != nil {
		// Rotate in place: the old refresh token becomes the "previous" one
		// (detectable as reuse), the family bumps, metadata refreshes.
		session = existing
		session.RefreshTokenPreviousHash = session.RefreshTokenHash
		session.State = domain.SessionActive
		session.RefreshTokenFamily++
		session.Device = device
		session.IPAddress = ip
		session.UserAgent = userAgent
		session.LastActiveAt = now
		session.UpdatedAt = now
	} else {
		sessionID, err := s.deps.IDs.NextID()
		if err != nil {
			return nil, domain.TokenPair{}, fmt.Errorf("auth: session id: %w", err)
		}
		session = &domain.Session{
			ID:           sessionID,
			UserID:       userID,
			Device:       device,
			IPAddress:    ip,
			UserAgent:    userAgent,
			LastActiveAt: now,
			State:        domain.SessionActive,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
	}

	pair, err := s.deps.Tokens.IssuePair(ctx, session.ID, userID, device.DeviceID, now)
	if err != nil {
		return nil, domain.TokenPair{}, fmt.Errorf("auth: issue tokens: %w", err)
	}
	session.RefreshTokenHash = domain.HashOpaqueToken(pair.RefreshToken)
	session.RefreshExpiresAt = pair.RefreshExpiresAt

	if existing != nil {
		if err := s.deps.Sessions.Update(ctx, dbtx, session); err != nil {
			return nil, domain.TokenPair{}, fmt.Errorf("auth: update session: %w", err)
		}
	} else {
		if err := s.deps.Sessions.Create(ctx, dbtx, session); err != nil {
			return nil, domain.TokenPair{}, fmt.Errorf("auth: store session: %w", err)
		}
	}

	if err := dbtx.Commit(ctx); err != nil {
		return nil, domain.TokenPair{}, fmt.Errorf("auth: commit session: %w", err)
	}
	return session, pair, nil
}

// recordFailure increments the per-identifier failure counter. Lockout is a
// MUST (AUTH-5), so failures to record propagate.
func (s *service) recordFailure(ctx context.Context, identifier string) error {
	if err := s.deps.Throttle.RecordFailure(ctx, identifier); err != nil {
		return fmt.Errorf("auth: record failure: %w", err)
	}
	return nil
}

// dummyVerify burns a comparable Argon2id verification so unknown identifiers
// and missing credentials do not leak through timing (see dummyPasswordHash).
func (s *service) dummyVerify(ctx context.Context, plaintext string) {
	if plaintext == "" {
		return
	}
	_, _ = s.deps.Hasher.Verify(ctx, dummyPasswordHash, plaintext)
}

// audit records an authentication event. Best-effort per AUTH-7: an audit
// outage must not break authentication.
func (s *service) audit(ctx context.Context, userID *int64, action string, resourceID *int64, ip *string, details map[string]string) {
	if s.deps.Audit == nil {
		return
	}
	_ = s.deps.Audit.Log(ctx, domain.AuditEvent{
		ActorUserID: userID,
		Action:      action,
		ResourceID:  resourceID,
		IPAddress:   ip,
		Details:     details,
	})
}
