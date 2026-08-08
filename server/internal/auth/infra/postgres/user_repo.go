// Package postgres implements the auth session repository over PostgreSQL
// (DATABASE.md §4.4). Row locking and compare-and-swap rotation give the
// refresh flow its concurrency safety (SECURITY_SPEC.md REFR-4/REFR-5).
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	userdomain "github.com/AkaneSakuramori/socialmedia/server/internal/user/domain"
	"github.com/AkaneSakuramori/socialmedia/server/pkg/tx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// UserRepo is the pgx-backed userdomain.UserRepository (DATABASE.md §4.1).
type UserRepo struct {
	pool *pgxpool.Pool
}

// NewUserRepo builds the repository over the shared pool.
func NewUserRepo(pool *pgxpool.Pool) *UserRepo {
	return &UserRepo{pool: pool}
}

// userColumns is the SELECT projection shared by every user lookup.
const userColumns = `
	id, username, display_name, phone_number, email, account_state,
	primary_identifier, token_version, created_at, updated_at`

// userByID loads a user by id (any state).
func (r *UserRepo) userByID(ctx context.Context, id int64) (*userdomain.User, error) {
	return scanUser(r.pool.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1`, id))
}

// FindByID loads a user by id, or ErrUserNotFound.
func (r *UserRepo) FindByID(ctx context.Context, id int64) (*userdomain.User, error) {
	u, err := r.userByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if u.AccountState == userdomain.AccountDeleted {
		return nil, userdomain.ErrUserNotFound
	}
	return u, nil
}

// ListByIDs loads the live users with the given ids, omitting unknown or
// deleted accounts (used for bulk display-name resolution by the chat module).
func (r *UserRepo) ListByIDs(ctx context.Context, q tx.Querier, ids []int64) ([]userdomain.User, error) {
	if len(ids) == 0 {
		return []userdomain.User{}, nil
	}
	rows, err := q.Query(ctx,
		`SELECT `+userColumns+` FROM users
		 WHERE id = ANY($1) AND account_state <> 'deleted'`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]userdomain.User, 0, len(ids))
	for rows.Next() {
		var u userdomain.User
		if err := rows.Scan(
			&u.ID, &u.Username, &u.DisplayName, &u.PhoneNumber, &u.Email,
			&u.AccountState, &u.PrimaryIdentifier, &u.TokenVersion,
			&u.CreatedAt, &u.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("user: scan: %w", err)
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// findActiveOrSuspended loads a non-deleted account by column.
func (r *UserRepo) findActiveOrSuspended(ctx context.Context, column, value string) (*userdomain.User, error) {
	u, err := scanUser(r.pool.QueryRow(ctx,
		`SELECT `+userColumns+` FROM users WHERE `+column+` = $1 AND account_state <> 'deleted'`, value))
	if err != nil {
		return nil, err
	}
	return u, nil
}

// FindByPhone loads a non-deleted account by normalized E.164 phone.
func (r *UserRepo) FindByPhone(ctx context.Context, phone string) (*userdomain.User, error) {
	return r.findActiveOrSuspended(ctx, "phone_number", phone)
}

// FindByEmail loads a non-deleted account by normalized email.
func (r *UserRepo) FindByEmail(ctx context.Context, email string) (*userdomain.User, error) {
	return r.findActiveOrSuspended(ctx, "email", email)
}

// taken reports whether the column holds a non-deleted account.
func (r *UserRepo) taken(ctx context.Context, column, value string) (bool, error) {
	var one int
	err := r.pool.QueryRow(ctx,
		`SELECT 1 FROM users WHERE `+column+` = $1 AND account_state <> 'deleted' LIMIT 1`, value).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// PhoneTaken reports whether the normalized phone belongs to a live account.
func (r *UserRepo) PhoneTaken(ctx context.Context, phone string) (bool, error) {
	return r.taken(ctx, "phone_number", phone)
}

// EmailTaken reports whether the normalized email belongs to a live account.
func (r *UserRepo) EmailTaken(ctx context.Context, email string) (bool, error) {
	return r.taken(ctx, "email", email)
}

// UsernameTaken reports whether the username is held by a live account.
func (r *UserRepo) UsernameTaken(ctx context.Context, username string) (bool, error) {
	return r.taken(ctx, "username", username)
}

// Create inserts a new user within the given transaction.
func (r *UserRepo) Create(ctx context.Context, dbtx tx.Tx, u *userdomain.User) error {
	_, err := dbtx.Exec(ctx, `
		INSERT INTO users (
			id, username, display_name, phone_number, email, account_state,
			primary_identifier, token_version, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		u.ID, u.Username, u.DisplayName, u.PhoneNumber, u.Email,
		u.AccountState, u.PrimaryIdentifier, u.TokenVersion,
		u.CreatedAt, u.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return userdomain.ErrIdentifierTaken
		}
		return err
	}
	return nil
}

// BumpTokenVersion atomically increments the account's global token version.
func (r *UserRepo) BumpTokenVersion(ctx context.Context, dbtx tx.Tx, userID int64) (int64, error) {
	var v int64
	err := dbtx.QueryRow(ctx, `
		UPDATE users SET token_version = token_version + 1
		WHERE id = $1 RETURNING token_version`, userID).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, userdomain.ErrUserNotFound
	}
	if err != nil {
		return 0, err
	}
	return v, nil
}

// SetEmail atomically assigns a verified email (unique index arbitrates races).
func (r *UserRepo) SetEmail(ctx context.Context, dbtx tx.Tx, userID int64, email string) error {
	_, err := dbtx.Exec(ctx, `UPDATE users SET email = $2 WHERE id = $1`, userID, email)
	if isUniqueViolation(err) {
		return userdomain.ErrIdentifierTaken
	}
	return err
}

// SetPhone atomically assigns a verified E.164 phone.
func (r *UserRepo) SetPhone(ctx context.Context, dbtx tx.Tx, userID int64, phone string) error {
	_, err := dbtx.Exec(ctx, `UPDATE users SET phone_number = $2 WHERE id = $1`, userID, phone)
	if isUniqueViolation(err) {
		return userdomain.ErrIdentifierTaken
	}
	return err
}

// MarkDeleted soft-deletes the account (API.md §5.5).
func (r *UserRepo) MarkDeleted(ctx context.Context, dbtx tx.Tx, userID int64, deletedAt time.Time) error {
	n, err := dbtx.Exec(ctx, `
		UPDATE users SET account_state = 'deleted', deleted_at = $2
		WHERE id = $1 AND account_state <> 'deleted'`, userID, deletedAt)
	if err != nil {
		return err
	}
	if n == 0 {
		return userdomain.ErrAccountAlreadyDeleted
	}
	return nil
}

// Restore reactivates a deleted account inside its grace window.
func (r *UserRepo) Restore(ctx context.Context, dbtx tx.Tx, userID int64, graceCutoff time.Time) error {
	n, err := dbtx.Exec(ctx, `
		UPDATE users SET account_state = 'active', deleted_at = NULL
		WHERE id = $1 AND account_state = 'deleted' AND deleted_at >= $2`,
		userID, graceCutoff)
	if err != nil {
		return err
	}
	if n == 0 {
		return userdomain.ErrAccountRestoreExpired
	}
	return nil
}

// findDeleted loads a deleted account by column.
func (r *UserRepo) findDeleted(ctx context.Context, column, value string) (*userdomain.User, error) {
	return scanUser(r.pool.QueryRow(ctx,
		`SELECT `+userColumns+` FROM users WHERE `+column+` = $1 AND account_state = 'deleted'`, value))
}

// FindDeletedByPhone loads a deleted account by phone (recovery lookup).
func (r *UserRepo) FindDeletedByPhone(ctx context.Context, phone string) (*userdomain.User, error) {
	return r.findDeleted(ctx, "phone_number", phone)
}

// FindDeletedByEmail loads a deleted account by email (recovery lookup).
func (r *UserRepo) FindDeletedByEmail(ctx context.Context, email string) (*userdomain.User, error) {
	return r.findDeleted(ctx, "email", email)
}

// PurgeDeleted hard-deletes accounts deleted before the cutoff and their
// dependent rows, in one transaction (DATABASE.md §4.1 retention worker).
func (r *UserRepo) PurgeDeleted(ctx context.Context, cutoff time.Time) (int64, error) {
	dbtx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer dbtx.Rollback(ctx) // no-op once committed

	var ids []int64
	rows, err := dbtx.Query(ctx,
		`SELECT id FROM users WHERE account_state = 'deleted' AND deleted_at <= $1 FOR UPDATE`, cutoff)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	for _, id := range ids {
		if _, err := dbtx.Exec(ctx, `DELETE FROM auth_tokens WHERE user_id = $1`, id); err != nil {
			return 0, err
		}
		if _, err := dbtx.Exec(ctx, `DELETE FROM user_sessions WHERE user_id = $1`, id); err != nil {
			return 0, err
		}
		if _, err := dbtx.Exec(ctx, `DELETE FROM user_credentials WHERE user_id = $1`, id); err != nil {
			return 0, err
		}
		if _, err := dbtx.Exec(ctx, `DELETE FROM users WHERE id = $1`, id); err != nil {
			return 0, err
		}
	}

	if err := dbtx.Commit(ctx); err != nil {
		return 0, err
	}
	return int64(len(ids)), nil
}

// isUniqueViolation maps a PostgreSQL 23505 to a sentinel or surfaces it raw.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// scanUser maps a users row to the domain User.
func scanUser(row tx.Row) (*userdomain.User, error) {
	var u userdomain.User
	err := row.Scan(
		&u.ID, &u.Username, &u.DisplayName, &u.PhoneNumber, &u.Email,
		&u.AccountState, &u.PrimaryIdentifier, &u.TokenVersion,
		&u.CreatedAt, &u.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, userdomain.ErrUserNotFound
		}
		return nil, fmt.Errorf("user: scan: %w", err)
	}
	return &u, nil
}
