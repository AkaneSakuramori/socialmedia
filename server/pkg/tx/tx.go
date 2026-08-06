// Package tx defines the transaction primitives shared by the domain layer
// (ENGINEERING.md §6.2, §23). Domains depend on these tiny interfaces instead
// of a concrete driver, keeping pgx/database/sql out of domain code. Infra
// adapters (internal/platform/postgres) implement them; application services
// orchestrate commits and rollbacks.
//
// Tx also exposes the query surface repositories need (Exec/QueryRow/Query).
// pgx.Row and pgx.Rows satisfy Row/Rows structurally, so adapters only wrap
// Exec's command tag.
package tx

import "context"

// Row is a single scanned result row (structurally compatible with pgx.Row).
type Row interface {
	Scan(dest ...any) error
}

// Rows is a scanned result set (structurally compatible with pgx.Rows).
type Rows interface {
	Row
	Next() bool
	Err() error
	Close()
}

// Tx is an in-progress database transaction. It must be committed or rolled
// back exactly once; a deferred Rollback after Commit is a harmless no-op.
type Tx interface {
	Commit(context.Context) error
	Rollback(context.Context) error
	// Exec runs a statement and returns the number of affected rows.
	Exec(ctx context.Context, sql string, args ...any) (int64, error)
	// QueryRow runs a query returning at most one row.
	QueryRow(ctx context.Context, sql string, args ...any) Row
	// Query runs a query returning a row set.
	Query(ctx context.Context, sql string, args ...any) (Rows, error)
}

// Beginner opens transactions. It is the port the composition root injects so
// application services can run multi-aggregate use-cases atomically.
type Beginner interface {
	Begin(context.Context) (Tx, error)
}
