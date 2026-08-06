package application

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/AkaneSakuramori/socialmedia/server/internal/auth/domain"
	"github.com/AkaneSakuramori/socialmedia/server/pkg/tx"
)

// Device & session administration (API.md §4.5–§4.8, ARCHITECTURE.md §11.2,
// SECURITY_SPEC.md SESS-3..SESS-9). Every operation is scoped to the caller's
// user id taken from the access token — never from a request body (SESS-3).
// Reads and writes use the same session registry (PG is the source of truth).

// ListSessions implements Service.ListSessions (API.md §4.7).
func (s *service) ListSessions(ctx context.Context, cmd ListSessionsCommand) ([]SessionInfo, error) {
	sessions, err := s.deps.Sessions.ListByUser(ctx, cmd.UserID)
	if err != nil {
		return nil, fmt.Errorf("auth: list sessions: %w", err)
	}
	out := make([]SessionInfo, 0, len(sessions))
	for _, sess := range sessions {
		out = append(out, SessionInfo{
			ID:           sess.ID,
			DeviceID:     sess.Device.DeviceID,
			DeviceName:   sess.Device.DeviceName,
			Platform:     sess.Device.Platform,
			AppVersion:   sess.Device.AppVersion,
			LastActiveAt: sess.LastActiveAt,
			CreatedAt:    sess.CreatedAt,
			Current:      sess.ID == cmd.CurrentSessionID,
		})
	}
	return out, nil
}

// RenameSession implements Service.RenameSession (device-management screen).
func (s *service) RenameSession(ctx context.Context, cmd RenameSessionCommand) error {
	if err := domain.ValidateDeviceName(cmd.DeviceName); err != nil {
		return err
	}

	dbtx, err := s.deps.TxBeginner.Begin(ctx)
	if err != nil {
		return fmt.Errorf("auth: begin tx: %w", err)
	}
	defer dbtx.Rollback(ctx) // no-op once committed

	session, err := s.lockOwnSession(ctx, dbtx, cmd.UserID, cmd.SessionID)
	if err != nil {
		return err
	}
	if err := s.deps.Sessions.Rename(ctx, dbtx, cmd.UserID, cmd.SessionID, cmd.DeviceName); err != nil {
		return fmt.Errorf("auth: rename session: %w", err)
	}
	if err := dbtx.Commit(ctx); err != nil {
		return fmt.Errorf("auth: commit rename: %w", err)
	}

	s.audit(ctx, &cmd.UserID, "auth.session_renamed", &cmd.SessionID, nil,
		map[string]string{"session_id": strconv.FormatInt(cmd.SessionID, 10), "device_id": session.Device.DeviceID})
	return nil
}

// Logout implements Service.Logout (API.md §4.5): revoke the caller's own
// session. The session identity comes from the token, never a body field.
func (s *service) Logout(ctx context.Context, cmd LogoutCommand) error {
	dbtx, err := s.deps.TxBeginner.Begin(ctx)
	if err != nil {
		return fmt.Errorf("auth: begin tx: %w", err)
	}
	defer dbtx.Rollback(ctx) // no-op once committed

	if err := s.deps.Sessions.RevokeByID(ctx, dbtx, cmd.UserID, cmd.SessionID); err != nil {
		if errors.Is(err, domain.ErrSessionNotFound) {
			return domain.ErrSessionNotFound
		}
		return fmt.Errorf("auth: revoke session: %w", err)
	}
	if err := dbtx.Commit(ctx); err != nil {
		return fmt.Errorf("auth: commit logout: %w", err)
	}

	s.audit(ctx, &cmd.UserID, "auth.logout", &cmd.SessionID, nil,
		map[string]string{"session_id": strconv.FormatInt(cmd.SessionID, 10)})
	return nil
}

// LogoutSession implements Service.LogoutSession (API.md §4.8): revoke a
// specific device. Ownership is enforced: a foreign session is 403, an
// unknown one 404.
func (s *service) LogoutSession(ctx context.Context, cmd LogoutSessionCommand) error {
	dbtx, err := s.deps.TxBeginner.Begin(ctx)
	if err != nil {
		return fmt.Errorf("auth: begin tx: %w", err)
	}
	defer dbtx.Rollback(ctx) // no-op once committed

	target, err := s.lockOwnSession(ctx, dbtx, cmd.UserID, cmd.SessionID)
	if err != nil {
		return err
	}
	if err := s.deps.Sessions.RevokeByID(ctx, dbtx, cmd.UserID, cmd.SessionID); err != nil {
		if errors.Is(err, domain.ErrSessionNotFound) {
			// Already revoked by a concurrent request — idempotent success.
			_ = dbtx.Commit(ctx)
			s.audit(ctx, &cmd.UserID, "auth.session_revoked", &cmd.SessionID, nil,
				map[string]string{"session_id": strconv.FormatInt(cmd.SessionID, 10), "device_id": target.Device.DeviceID})
			return nil
		}
		return fmt.Errorf("auth: revoke session: %w", err)
	}
	if err := dbtx.Commit(ctx); err != nil {
		return fmt.Errorf("auth: commit revocation: %w", err)
	}

	s.audit(ctx, &cmd.UserID, "auth.session_revoked", &cmd.SessionID, nil,
		map[string]string{"session_id": strconv.FormatInt(cmd.SessionID, 10), "device_id": target.Device.DeviceID})
	return nil
}

// LogoutOtherSessions implements Service.LogoutOtherSessions: revoke every
// active session of the user except the caller's current one.
func (s *service) LogoutOtherSessions(ctx context.Context, cmd LogoutOtherSessionsCommand) error {
	dbtx, err := s.deps.TxBeginner.Begin(ctx)
	if err != nil {
		return fmt.Errorf("auth: begin tx: %w", err)
	}
	defer dbtx.Rollback(ctx) // no-op once committed

	if err := s.deps.Sessions.RevokeOthersByUserID(ctx, dbtx, cmd.UserID, cmd.SessionID); err != nil {
		return fmt.Errorf("auth: revoke other sessions: %w", err)
	}
	if err := dbtx.Commit(ctx); err != nil {
		return fmt.Errorf("auth: commit revocation: %w", err)
	}

	s.audit(ctx, &cmd.UserID, "auth.logout_others", &cmd.SessionID, nil,
		map[string]string{"session_id": strconv.FormatInt(cmd.SessionID, 10)})
	return nil
}

// LogoutAll implements Service.LogoutAll (API.md §4.6, SESS-6): revoke every
// session and bump the user's global token version — atomically — so all
// outstanding access tokens (which carry the old `ver` claim) stop validating
// at the gateways. The caller's own session is revoked too ("sign out
// everywhere").
func (s *service) LogoutAll(ctx context.Context, cmd LogoutAllCommand) error {
	dbtx, err := s.deps.TxBeginner.Begin(ctx)
	if err != nil {
		return fmt.Errorf("auth: begin tx: %w", err)
	}
	defer dbtx.Rollback(ctx) // no-op once committed

	if _, err := s.deps.Users.BumpTokenVersion(ctx, dbtx, cmd.UserID); err != nil {
		return fmt.Errorf("auth: bump token version: %w", err)
	}
	if err := s.deps.Sessions.RevokeAllByUserID(ctx, dbtx, cmd.UserID); err != nil {
		return fmt.Errorf("auth: revoke all sessions: %w", err)
	}
	if err := dbtx.Commit(ctx); err != nil {
		return fmt.Errorf("auth: commit logout-all: %w", err)
	}

	s.audit(ctx, &cmd.UserID, "auth.logout_all", &cmd.UserID, nil, nil)
	return nil
}

// ExpireIdleSessions implements Service.ExpireIdleSessions (SESS-9, REFR-6).
// Runs the sliding idle + refresh-window sweep; intended for a scheduled job.
func (s *service) ExpireIdleSessions(ctx context.Context) (int64, error) {
	n, err := s.deps.Sessions.ExpireIdle(ctx, s.now(), s.deps.SessionIdleTimeout)
	if err != nil {
		return 0, fmt.Errorf("auth: expire idle sessions: %w", err)
	}
	if n > 0 {
		s.audit(ctx, nil, "auth.sessions_expired", nil, nil,
			map[string]string{"count": strconv.FormatInt(n, 10)})
	}
	return n, nil
}

// PurgeRevokedSessions implements Service.PurgeRevokedSessions
// (DATABASE.md §4.4 retention). Runs the revoked/expired purge; intended for
// a scheduled job.
func (s *service) PurgeRevokedSessions(ctx context.Context) (int64, error) {
	cutoff := s.now().Add(-s.deps.SessionRetention)
	n, err := s.deps.Sessions.Purge(ctx, cutoff)
	if err != nil {
		return 0, fmt.Errorf("auth: purge sessions: %w", err)
	}
	if n > 0 {
		s.audit(ctx, nil, "auth.sessions_purged", nil, nil,
			map[string]string{"count": strconv.FormatInt(n, 10)})
	}
	return n, nil
}

// lockOwnSession loads a session under lock and enforces ownership (SESS-3).
// It returns ErrSessionNotOwned when the session belongs to another user and
// ErrSessionNotFound when it does not exist.
func (s *service) lockOwnSession(ctx context.Context, dbtx tx.Tx, userID, sessionID int64) (*domain.Session, error) {
	session, err := s.deps.Sessions.FindByID(ctx, dbtx, sessionID)
	if err != nil {
		if errors.Is(err, domain.ErrSessionNotFound) {
			return nil, domain.ErrSessionNotFound
		}
		return nil, fmt.Errorf("auth: find session: %w", err)
	}
	if session.UserID != userID {
		return nil, domain.ErrSessionNotOwned
	}
	return session, nil
}
