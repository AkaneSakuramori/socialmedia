package application

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/internal/auth/domain"
	userdomain "github.com/AkaneSakuramori/socialmedia/server/internal/user/domain"
	"github.com/AkaneSakuramori/socialmedia/server/pkg/tx"
)

// Refresh rotates a long-lived session's access and refresh tokens
// (API.md §4.4, SECURITY_SPEC.md REFR-4). Presenting a rotated-out token is a
// theft signal (REFR-5): it returns ErrRefreshTokenReuse and revokes every
// session of the user. The rotation happens in one transaction under a row
// lock, so concurrent refreshes of the same token serialize: exactly one wins,
// the rest are treated as reuse.
func (s *service) Refresh(ctx context.Context, cmd RefreshCommand) (*RefreshResult, error) {
	// REFR-1/REFR-3: reject anything that is not a well-formed opaque token
	// before touching storage or logs.
	if !domain.IsOpaqueTokenShape(cmd.RefreshToken) {
		s.audit(ctx, nil, "auth.token_refresh_failed", nil, cmd.IPAddress,
			map[string]string{"reason": "malformed"})
		return nil, domain.ErrRefreshTokenInvalid
	}
	hash := domain.HashOpaqueToken(cmd.RefreshToken)
	now := s.now()

	dbtx, err := s.deps.TxBeginner.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("auth: begin tx: %w", err)
	}
	defer dbtx.Rollback(ctx) // no-op once committed

	session, err := s.deps.Sessions.FindByHash(ctx, dbtx, hash)
	if err != nil && !errors.Is(err, domain.ErrSessionNotFound) {
		return nil, fmt.Errorf("auth: find session by token: %w", err)
	}
	if errors.Is(err, domain.ErrSessionNotFound) {
		return nil, s.reuseOrInvalid(ctx, dbtx, hash, cmd)
	}

	// The token is the session's current one. Gate on state and expiry.
	switch session.State {
	case domain.SessionActive:
	case domain.SessionRevoked, domain.SessionExpired, domain.SessionSuspended:
		s.audit(ctx, &session.UserID, "auth.token_refresh_failed", &session.ID, cmd.IPAddress,
			map[string]string{"reason": "session_" + string(session.State)})
		return nil, domain.ErrRefreshTokenInvalid
	}
	if !session.RefreshExpiresAt.IsZero() && !now.Before(session.RefreshExpiresAt) {
		s.audit(ctx, &session.UserID, "auth.token_refresh_failed", &session.ID, cmd.IPAddress,
			map[string]string{"reason": "expired"})
		return nil, domain.ErrRefreshTokenInvalid
	}

	// AUTH-8: a suspended account must not keep authenticating; a deleted one
	// is treated as invalid (no state leak).
	user, err := s.deps.Users.FindByID(ctx, session.UserID)
	if err != nil && !errors.Is(err, userdomain.ErrUserNotFound) {
		return nil, fmt.Errorf("auth: find user: %w", err)
	}
	switch {
	case err != nil, user.AccountState == userdomain.AccountDeleted:
		s.audit(ctx, nil, "auth.token_refresh_failed", &session.ID, cmd.IPAddress,
			map[string]string{"reason": "account_unavailable"})
		return nil, domain.ErrRefreshTokenInvalid
	case user.AccountState == userdomain.AccountSuspended:
		s.audit(ctx, &user.ID, "auth.token_refresh_failed", &session.ID, cmd.IPAddress,
			map[string]string{"reason": "suspended"})
		return nil, domain.ErrAccountSuspended
	}

	pair, err := s.deps.Tokens.IssuePair(ctx, session.ID, session.UserID, session.Device.DeviceID, user.TokenVersion, now)
	if err != nil {
		return nil, fmt.Errorf("auth: issue tokens: %w", err)
	}

	// REFR-4: rotate — the presented token becomes the "previous" one, the
	// session moves to the fresh refresh token, and the sliding expiry
	// (REFR-6) extends. Rotate is a compare-and-swap on the current hash, so a
	// racing refresh cannot double-rotate.
	session.RefreshTokenPreviousHash = hash
	session.RefreshTokenHash = domain.HashOpaqueToken(pair.RefreshToken)
	session.RefreshTokenFamily++
	session.RefreshExpiresAt = pair.RefreshExpiresAt
	session.LastActiveAt = now
	session.UpdatedAt = now
	if err := s.deps.Sessions.Rotate(ctx, dbtx, session, hash); err != nil {
		if errors.Is(err, domain.ErrSessionNotFound) {
			// A concurrent rotation already replaced this token → reuse signal.
			return nil, s.reuseOrInvalid(ctx, dbtx, hash, cmd)
		}
		return nil, fmt.Errorf("auth: rotate session: %w", err)
	}

	if err := dbtx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("auth: commit refresh: %w", err)
	}

	s.audit(ctx, &session.UserID, "auth.token_refresh", &session.ID, cmd.IPAddress,
		map[string]string{"session_id": strconv.FormatInt(session.ID, 10)})

	expiresIn := int64(time.Until(pair.AccessExpiresAt).Seconds())
	if expiresIn < 0 {
		expiresIn = 0
	}
	return &RefreshResult{
		AccessToken:  pair.AccessToken,
		RefreshToken: pair.RefreshToken,
		SessionID:    session.ID,
		ExpiresIn:    expiresIn,
	}, nil
}

// reuseOrInvalid runs when the presented hash is not any session's current
// token. If it matches a session's previous hash, the token was rotated out —
// a theft signal: revoke all sessions of the user (REFR-5 → 410). Otherwise
// the token is unknown → 401.
func (s *service) reuseOrInvalid(ctx context.Context, dbtx tx.Tx, hash string, cmd RefreshCommand) error {
	reused, err := s.deps.Sessions.FindByPreviousHash(ctx, dbtx, hash)
	if err != nil && !errors.Is(err, domain.ErrSessionNotFound) {
		return fmt.Errorf("auth: find session by previous token: %w", err)
	}
	if errors.Is(err, domain.ErrSessionNotFound) {
		s.audit(ctx, nil, "auth.token_refresh_failed", nil, cmd.IPAddress,
			map[string]string{"reason": "unknown_token"})
		return domain.ErrRefreshTokenInvalid
	}

	if err := s.deps.Sessions.RevokeAllByUserID(ctx, dbtx, reused.UserID); err != nil {
		return fmt.Errorf("auth: revoke all sessions: %w", err)
	}
	if err := dbtx.Commit(ctx); err != nil {
		return fmt.Errorf("auth: commit revocation: %w", err)
	}
	s.audit(ctx, &reused.UserID, "auth.token_reuse", &reused.ID, cmd.IPAddress,
		map[string]string{"session_id": strconv.FormatInt(reused.ID, 10)})
	return domain.ErrRefreshTokenReuse
}
