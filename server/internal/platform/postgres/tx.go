package postgres

import (
	"context"

	"github.com/AkaneSakuramori/socialmedia/server/pkg/tx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pgxTx adapts a pgx.Tx to the dependency-free tx.Tx contract. pgx.Row and
// pgx.Rows already satisfy tx.Row/tx.Rows structurally; only Exec needs
// wrapping because pgx returns a CommandTag rather than a row count.
type pgxTx struct {
	tx pgx.Tx
}

func (t *pgxTx) Commit(ctx context.Context) error   { return t.tx.Commit(ctx) }
func (t *pgxTx) Rollback(ctx context.Context) error { return t.tx.Rollback(ctx) }
func (t *pgxTx) QueryRow(ctx context.Context, q string, args ...any) tx.Row {
	return t.tx.QueryRow(ctx, q, args...)
}
func (t *pgxTx) Query(ctx context.Context, q string, args ...any) (tx.Rows, error) {
	return t.tx.Query(ctx, q, args...)
}
func (t *pgxTx) Exec(ctx context.Context, q string, args ...any) (int64, error) {
	tag, err := t.tx.Exec(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// Beginner opens transactions from the pool. Repositories receive these
// transactions through the domain port (tx.Beginner) at the composition root.
type Beginner struct {
	pool *pgxpool.Pool
}

// NewBeginner builds the transaction opener over a pgx pool.
func NewBeginner(pool *pgxpool.Pool) *Beginner { return &Beginner{pool: pool} }

// Begin opens a transaction.
func (b *Beginner) Begin(ctx context.Context) (tx.Tx, error) {
	t, err := b.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &pgxTx{tx: t}, nil
}

// Querier adapts the connection pool to the dependency-free tx.Querier read
// surface for pre-transaction and post-commit reads (the pool's Query/QueryRow
// satisfy the interface except for the concrete pgx.Row/pgx.Rows result
// types). Application services hold writes inside a tx.Tx instead — a write
// transaction must never take a second pool connection, or a bounded pool
// deadlocks (the tx holds row locks while it waits for a connection the
// queued writers hold).
type Querier struct {
	pool *pgxpool.Pool
}

// NewQuerier builds the pool-backed read surface over a pgx pool.
func NewQuerier(pool *pgxpool.Pool) *Querier { return &Querier{pool: pool} }

func (q *Querier) Query(ctx context.Context, sql string, args ...any) (tx.Rows, error) {
	return q.pool.Query(ctx, sql, args...)
}

func (q *Querier) QueryRow(ctx context.Context, sql string, args ...any) tx.Row {
	return q.pool.QueryRow(ctx, sql, args...)
}
