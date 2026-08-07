package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/internal/auth/domain"
	"github.com/AkaneSakuramori/socialmedia/server/pkg/tx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AuthTokenRepo is the pgx-backed domain.AuthTokenRepository.
type AuthTokenRepo struct {
	pool *pgxpool.Pool
}

// NewAuthTokenRepo builds the repository over the shared pool.
func NewAuthTokenRepo(pool *pgxpool.Pool) *AuthTokenRepo {
	return &AuthTokenRepo{pool: pool}
}

// Create inserts a recovery/verification token within the given transaction.
func (r *AuthTokenRepo) Create(ctx context.Context, dbtx tx.Tx, t *domain.AuthToken) error {
	data := t.Data
	if data == nil {
		data = []byte(`{}`) // column is NOT NULL DEFAULT '{}'
	}
	_, err := dbtx.Exec(ctx, `
		INSERT INTO auth_tokens (user_id, purpose, token_hash, data, expires_at, created_at)
		VALUES ($1,$2,$3,$4,$5,$6)`,
		t.UserID, t.Purpose, t.TokenHash, data, t.ExpiresAt, t.CreatedAt)
	return err
}

// Consume atomically claims an unused, unexpired token and marks it used. The
// UPDATE is the atomic gate, so two concurrent consumes of the same token can
// never both succeed (REC-6 single-use). Unknown, used, and expired hashes are
// reported identically as ErrRecoveryTokenInvalid (no state enumeration).
func (r *AuthTokenRepo) Consume(ctx context.Context, dbtx tx.Tx, tokenHash string) (*domain.AuthToken, error) {
	row := dbtx.QueryRow(ctx, `
		UPDATE auth_tokens SET used_at = now()
		WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()
		RETURNING id, user_id, purpose, token_hash, data, expires_at, used_at, created_at`,
		tokenHash)
	return scanAuthToken(row)
}

// scanAuthToken maps an auth_tokens row to the domain AuthToken.
func scanAuthToken(row tx.Row) (*domain.AuthToken, error) {
	var t domain.AuthToken
	var data []byte
	var usedAt *time.Time
	err := row.Scan(&t.ID, &t.UserID, &t.Purpose, &t.TokenHash, &data,
		&t.ExpiresAt, &usedAt, &t.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrRecoveryTokenInvalid
		}
		return nil, fmt.Errorf("auth_token: scan: %w", err)
	}
	t.Data = data
	t.UsedAt = usedAt
	return &t, nil
}

// marshalData encodes a string map as the token's JSONB payload.
func marshalData(m map[string]string) ([]byte, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("auth_token: marshal data: %w", err)
	}
	return b, nil
}
