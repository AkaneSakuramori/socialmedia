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

func (t *pgxTx) Commit(ctx context.Context) error    { return t.tx.Commit(ctx) }
func (t *pgxTx) Rollback(ctx context.Context) error  { return t.tx.Rollback(ctx) }
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
