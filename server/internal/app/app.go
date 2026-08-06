// Package app is the composition root for the api-server process
// (ENGINEERING.md §10.2). It loads configuration, wires dependencies, and owns
// the process lifecycle: startup connectivity checks, graceful shutdown, and
// dependency teardown. No business logic lives here.
package app

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/AkaneSakuramori/socialmedia/server/config"
	"github.com/AkaneSakuramori/socialmedia/server/internal/platform/health"
	"github.com/AkaneSakuramori/socialmedia/server/internal/platform/httpserver"
	"github.com/AkaneSakuramori/socialmedia/server/internal/platform/observability"
	"github.com/AkaneSakuramori/socialmedia/server/internal/platform/postgres"
	"github.com/AkaneSakuramori/socialmedia/server/internal/platform/redis"
)

// Version is injected at build time via -ldflags "-X .../internal/app.Version=...".
var Version = "dev"

// Run is the api-server entry point. It returns a non-nil error when the
// process must exit non-zero.
func Run(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := observability.NewLogger(cfg.AppEnv)
	log.Info("starting api-server", "version", Version, "env", cfg.AppEnv, "config", cfg.String())

	// Dependencies are created here and closed here (ENGINEERING.md §10.2).
	pool, err := postgres.Open(ctx, cfg.PGDSN, cfg.PGMaxConns)
	if err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	defer pool.Close()

	redisClient := redis.NewClient(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
	defer redisClient.Close()

	// Eager connectivity probe — non-fatal, readiness gates traffic.
	if err := postgres.Ping(ctx, pool); err != nil {
		log.Warn("postgres not reachable at startup", "error", err)
	} else {
		log.Info("postgres connection established")
	}
	if err := redis.Ping(ctx, redisClient); err != nil {
		log.Warn("redis not reachable at startup", "error", err)
	} else {
		log.Info("redis connection established")
	}

	reg := health.NewRegistry()
	reg.Register("postgres", health.CheckFunc(func(ctx context.Context) error {
		return postgres.Ping(ctx, pool)
	}))
	reg.Register("redis", health.CheckFunc(func(ctx context.Context) error {
		return redis.Ping(ctx, redisClient)
	}))

	liveness := health.Handler(log, reg, false)
	readiness := health.Handler(log, reg, true)
	srv := httpserver.New(cfg, log, liveness, readiness)

	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Info("http server listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !httpserver.IsServerClosed(err) {
			errCh <- fmt.Errorf("http server: %w", err)
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-sigCtx.Done():
		log.Info("shutdown signal received, draining in-flight requests")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := httpserver.Shutdown(shutdownCtx, srv); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	log.Info("http server stopped")

	// Defers close Redis, then the pool, in reverse order.
	log.Info("api-server stopped cleanly")
	return nil
}

// Healthcheck verifies runtime connectivity for container healthchecks
// (DEVOPS.md §5: /healthz + /readyz). Returns nil when all dependencies are
// reachable. It does not start the HTTP server.
func Healthcheck(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	pool, err := postgres.Open(ctx, cfg.PGDSN, cfg.PGMaxConns)
	if err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	defer pool.Close()
	redisClient := redis.NewClient(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
	defer redisClient.Close()

	if err := postgres.Ping(ctx, pool); err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	if err := redis.Ping(ctx, redisClient); err != nil {
		return fmt.Errorf("redis: %w", err)
	}
	return nil
}
