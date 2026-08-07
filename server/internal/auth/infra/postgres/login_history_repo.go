package postgres

import (
	"context"
	"fmt"

	"github.com/AkaneSakuramori/socialmedia/server/internal/auth/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

// LoginHistoryRepo is the pgx-backed domain.LoginHistoryRepository.
type LoginHistoryRepo struct {
	pool *pgxpool.Pool
}

// NewLoginHistoryRepo builds the repository over the shared pool.
func NewLoginHistoryRepo(pool *pgxpool.Pool) *LoginHistoryRepo {
	return &LoginHistoryRepo{pool: pool}
}

// Record appends a login event. Writes are best-effort (the auth flow must not
// depend on history success).
func (r *LoginHistoryRepo) Record(ctx context.Context, e domain.LoginEvent) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO login_history (
			user_id, identifier, method, success, new_device, device_id, ip_address, user_agent, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7::inet,$8,$9)`,
		e.UserID, e.Identifier, e.Method, e.Success, e.NewDevice,
		nullIfEmpty(strPtrOrNil(e.DeviceID)), nullIfEmpty(e.IPAddress), e.UserAgent, e.CreatedAt)
	return err
}

// ListByUser returns the user's login history, newest first.
func (r *LoginHistoryRepo) ListByUser(ctx context.Context, userID int64, limit int) ([]domain.LoginEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, user_id, identifier, method, success, new_device, device_id,
		       host(ip_address), user_agent, created_at
		FROM login_history WHERE user_id = $1
		ORDER BY created_at DESC, id DESC
		LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.LoginEvent
	for rows.Next() {
		var e domain.LoginEvent
		var deviceID *string
		var ip *string
		if err := rows.Scan(&e.ID, &e.UserID, &e.Identifier, &e.Method, &e.Success,
			&e.NewDevice, &deviceID, &ip, &e.UserAgent, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("login_history: scan: %w", err)
		}
		if deviceID != nil {
			e.DeviceID = *deviceID
		}
		e.IPAddress = ip
		out = append(out, e)
	}
	return out, rows.Err()
}

func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
