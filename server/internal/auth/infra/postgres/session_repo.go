// Package postgres implements the auth session repository over PostgreSQL
// (DATABASE.md §4.4). Row locking and compare-and-swap rotation give the
// refresh flow its concurrency safety (SECURITY_SPEC.md REFR-4/REFR-5).
package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/internal/auth/domain"
	"github.com/AkaneSakuramori/socialmedia/server/pkg/tx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SessionRepo is the pgx-backed domain.SessionRepository.
type SessionRepo struct {
	pool *pgxpool.Pool
}

// NewSessionRepo builds the repository over the shared pool.
func NewSessionRepo(pool *pgxpool.Pool) *SessionRepo {
	return &SessionRepo{pool: pool}
}

// sessionColumns is the SELECT projection shared by every lookup.
const sessionColumns = `
	id, user_id, device_id, device_name, platform, app_version, push_token,
	refresh_token_family, refresh_token_hash, refresh_token_previous_hash,
	ip_address::text, user_agent, last_active_at, state, created_at, updated_at,
	refresh_expires_at`

// Create inserts a session within the given transaction.
func (r *SessionRepo) Create(ctx context.Context, dbtx tx.Tx, s *domain.Session) error {
	_, err := dbtx.Exec(ctx, `
		INSERT INTO user_sessions (
			id, user_id, device_id, device_name, platform, app_version, push_token,
			refresh_token_family, refresh_token_hash, refresh_token_previous_hash,
			ip_address, user_agent, last_active_at, state, refresh_expires_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::inet,$12,$13,$14,$15)`,
		s.ID, s.UserID, s.Device.DeviceID, nullIfEmpty(s.Device.DeviceName), nullIfEmpty(s.Device.Platform),
		nullIfEmpty(s.Device.AppVersion), s.PushToken, s.RefreshTokenFamily,
		strOrNil(s.RefreshTokenHash), strOrNil(s.RefreshTokenPreviousHash),
		nullIfEmpty(s.IPAddress), s.UserAgent, s.LastActiveAt, s.State,
		nullTime(s.RefreshExpiresAt),
	)
	return err
}

// FindByDeviceID returns the session row for a user+device (any state).
func (r *SessionRepo) FindByDeviceID(ctx context.Context, userID int64, deviceID string) (*domain.Session, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+sessionColumns+`
		FROM user_sessions WHERE user_id = $1 AND device_id = $2`,
		userID, deviceID)
	return scanSession(row)
}

// Update persists the mutable session fields.
func (r *SessionRepo) Update(ctx context.Context, dbtx tx.Tx, s *domain.Session) error {
	_, err := dbtx.Exec(ctx, `
		UPDATE user_sessions SET
			device_name = $2, platform = $3, app_version = $4, push_token = $5,
			refresh_token_family = $6, refresh_token_hash = $7,
			refresh_token_previous_hash = $8, refresh_expires_at = $9,
			ip_address = $10::inet, user_agent = $11, last_active_at = $12,
			state = $13, updated_at = now()
		WHERE id = $1`,
		s.ID, nullIfEmpty(s.Device.DeviceName), nullIfEmpty(s.Device.Platform),
		nullIfEmpty(s.Device.AppVersion), s.PushToken, s.RefreshTokenFamily,
		strOrNil(s.RefreshTokenHash), strOrNil(s.RefreshTokenPreviousHash),
		nullTime(s.RefreshExpiresAt), nullIfEmpty(s.IPAddress), s.UserAgent,
		s.LastActiveAt, s.State)
	return err
}

// FindByHash returns the session whose current refresh-token hash matches,
// locking the row so concurrent rotations serialize (REFR-4 single-use).
func (r *SessionRepo) FindByHash(ctx context.Context, dbtx tx.Tx, hash string) (*domain.Session, error) {
	row := dbtx.QueryRow(ctx, `
		SELECT `+sessionColumns+`
		FROM user_sessions WHERE refresh_token_hash = $1
		FOR UPDATE`,
		hash)
	return scanSession(row)
}

// FindByPreviousHash returns the session whose rotated-out hash matches.
func (r *SessionRepo) FindByPreviousHash(ctx context.Context, dbtx tx.Tx, hash string) (*domain.Session, error) {
	row := dbtx.QueryRow(ctx, `
		SELECT `+sessionColumns+`
		FROM user_sessions WHERE refresh_token_previous_hash = $1
		FOR UPDATE`,
		hash)
	return scanSession(row)
}

// Rotate atomically moves a session from its current refresh token to the
// session's new token (compare-and-swap on the presented hash). It returns
// ErrSessionNotFound when no row still holds presentedHash, i.e. a concurrent
// rotation already consumed it.
func (r *SessionRepo) Rotate(ctx context.Context, dbtx tx.Tx, s *domain.Session, presentedHash string) error {
	n, err := dbtx.Exec(ctx, `
		UPDATE user_sessions SET
			refresh_token_previous_hash = $2,
			refresh_token_hash = $3,
			refresh_token_family = refresh_token_family + 1,
			refresh_expires_at = $4,
			last_active_at = $5,
			updated_at = now()
		WHERE id = $1 AND refresh_token_hash = $2 AND state = 'active'`,
		s.ID, presentedHash, strOrNil(s.RefreshTokenHash), nullTime(s.RefreshExpiresAt), s.LastActiveAt)
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrSessionNotFound
	}
	return nil
}

// RevokeAllByUserID revokes every active session of the user (REFR-5).
func (r *SessionRepo) RevokeAllByUserID(ctx context.Context, dbtx tx.Tx, userID int64) error {
	_, err := dbtx.Exec(ctx, `
		UPDATE user_sessions SET state = 'revoked', updated_at = now()
		WHERE user_id = $1 AND state <> 'revoked'`,
		userID)
	return err
}

// scanSession maps a row to the domain Session.
func scanSession(row tx.Row) (*domain.Session, error) {
	var s domain.Session
	var deviceName, platform, appVersion, pushToken, ip, ua *string
	var previousHash, refreshHash *string
	var refreshExpiresAt *time.Time
	err := row.Scan(
		&s.ID, &s.UserID, &s.Device.DeviceID, &deviceName, &platform, &appVersion,
		&pushToken, &s.RefreshTokenFamily, &refreshHash, &previousHash,
		&ip, &ua, &s.LastActiveAt, &s.State, &s.CreatedAt, &s.UpdatedAt,
		&refreshExpiresAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrSessionNotFound
		}
		return nil, err
	}
	s.Device.DeviceName = deviceName
	s.Device.Platform = platform
	s.Device.AppVersion = appVersion
	s.PushToken = pushToken
	s.IPAddress = ip
	s.UserAgent = ua
	if refreshHash != nil {
		s.RefreshTokenHash = *refreshHash
	}
	if previousHash != nil {
		s.RefreshTokenPreviousHash = *previousHash
	}
	if refreshExpiresAt != nil {
		s.RefreshExpiresAt = *refreshExpiresAt
	}
	return &s, nil
}

func nullIfEmpty(p *string) *string {
	if p != nil && *p == "" {
		return nil
	}
	return p
}

// strOrNil passes a string through as-is, mapping empty values to NULL.
func strOrNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}
