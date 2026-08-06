// Package postgres provides the pgx connection pool wrapper for the platform
// (ENGINEERING.md §2, §6). The pool is created at the composition root, pinged
// at startup, and closed during graceful shutdown. Domain repositories sit
// behind this package; domain code never imports the driver directly.
package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Open creates a pgx pool from a DSN. It does not fail if the database is
// temporarily unreachable — callers should Ping at startup and rely on the
// readiness check for runtime status.
func Open(ctx context.Context, dsn string, maxConns int32) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse postgres dsn: %w", err)
	}
	cfg.MaxConns = maxConns

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}
	return pool, nil
}

// Ping checks database connectivity with a timeout.
func Ping(ctx context.Context, pool *pgxpool.Pool) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return pool.Ping(ctx)
}
