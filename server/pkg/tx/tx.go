// Package tx defines the transaction primitives shared by the domain layer
// (ENGINEERING.md §6.2, §23). Domains depend on these tiny interfaces instead
// of a concrete driver, keeping pgx/database/sql out of domain code. Infra
// adapters (internal/platform/postgres) implement them; application services
// orchestrate commits and rollbacks.
package tx

import "context"

// Tx is an in-progress database transaction. It must be committed or rolled
// back exactly once; a deferred Rollback after Commit is a harmless no-op.
type Tx interface {
	Commit(context.Context) error
	Rollback(context.Context) error
}

// Beginner opens transactions. It is the port the composition root injects so
// application services can run multi-aggregate use-cases atomically.
type Beginner interface {
	Begin(context.Context) (Tx, error)
}
