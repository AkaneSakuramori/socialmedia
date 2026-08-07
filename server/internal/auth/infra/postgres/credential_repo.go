package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/AkaneSakuramori/socialmedia/server/internal/auth/domain"
	"github.com/AkaneSakuramori/socialmedia/server/pkg/tx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CredentialRepo is the pgx-backed domain.CredentialRepository (DATABASE.md
// §4.3). Password material lives in user_credentials.credential_data as
// {"hash": "<argon2id phc>"}.
type CredentialRepo struct {
	pool *pgxpool.Pool
}

// NewCredentialRepo builds the repository over the shared pool.
func NewCredentialRepo(pool *pgxpool.Pool) *CredentialRepo {
	return &CredentialRepo{pool: pool}
}

// Create inserts a credential within the given transaction.
func (r *CredentialRepo) Create(ctx context.Context, dbtx tx.Tx, c *domain.Credential) error {
	_, err := dbtx.Exec(ctx, `
		INSERT INTO user_credentials (id, user_id, method, provider, credential_data, revoked_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $7)`,
		c.ID, c.UserID, string(c.Method), c.Provider, c.Data, c.RevokedAt, c.CreatedAt)
	if err != nil {
		return fmt.Errorf("credential: insert: %w", err)
	}
	return nil
}

// FindPassword returns the user's current non-revoked password credential.
func (r *CredentialRepo) FindPassword(ctx context.Context, userID int64) (*domain.Credential, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, user_id, method, provider, credential_data, revoked_at, created_at, updated_at
		FROM user_credentials
		WHERE user_id = $1 AND method = 'password' AND revoked_at IS NULL
		ORDER BY created_at DESC LIMIT 1`, userID)
	c, err := scanCredential(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrInvalidCredentials
	}
	if err != nil {
		return nil, fmt.Errorf("credential: find password: %w", err)
	}
	return c, nil
}

// ReplacePassword atomically replaces the user's password hash in place
// (password change/reset).
func (r *CredentialRepo) ReplacePassword(ctx context.Context, dbtx tx.Tx, userID int64, hash domain.PasswordHash) error {
	data, err := json.Marshal(domain.PasswordCredentialData{Hash: hash.String()})
	if err != nil {
		return fmt.Errorf("credential: marshal password data: %w", err)
	}
	_, err = dbtx.Exec(ctx, `
		UPDATE user_credentials SET credential_data = $2, revoked_at = NULL, updated_at = now()
		WHERE user_id = $1 AND method = 'password'`,
		userID, data)
	if err != nil {
		return fmt.Errorf("credential: replace password: %w", err)
	}
	return nil
}

// scanCredential decodes one credential row.
func scanCredential(row pgx.Row) (*domain.Credential, error) {
	var c domain.Credential
	if err := row.Scan(&c.ID, &c.UserID, &c.Method, &c.Provider, &c.Data, &c.RevokedAt, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	return &c, nil
}
