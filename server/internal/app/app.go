// Package app is the composition root for the api-server process
// (ENGINEERING.md §10.2). It loads configuration, wires dependencies, and owns
// the process lifecycle: startup connectivity checks, graceful shutdown, and
// dependency teardown. No business logic lives here.
package app

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/AkaneSakuramori/socialmedia/server/config"
	authapp "github.com/AkaneSakuramori/socialmedia/server/internal/auth/application"
	authdomain "github.com/AkaneSakuramori/socialmedia/server/internal/auth/domain"
	authotp "github.com/AkaneSakuramori/socialmedia/server/internal/auth/infra/otp"
	authpostgres "github.com/AkaneSakuramori/socialmedia/server/internal/auth/infra/postgres"
	authsecurity "github.com/AkaneSakuramori/socialmedia/server/internal/auth/infra/security"
	auththrottle "github.com/AkaneSakuramori/socialmedia/server/internal/auth/infra/throttle"
	chatapp "github.com/AkaneSakuramori/socialmedia/server/internal/chat/application"
	chathttp "github.com/AkaneSakuramori/socialmedia/server/internal/chat/delivery/http"
	chatpostgres "github.com/AkaneSakuramori/socialmedia/server/internal/chat/infra/postgres"
	"github.com/AkaneSakuramori/socialmedia/server/internal/platform/health"
	"github.com/AkaneSakuramori/socialmedia/server/internal/platform/httpserver"
	"github.com/AkaneSakuramori/socialmedia/server/internal/platform/idgen"
	"github.com/AkaneSakuramori/socialmedia/server/internal/platform/observability"
	"github.com/AkaneSakuramori/socialmedia/server/internal/platform/postgres"
	"github.com/AkaneSakuramori/socialmedia/server/internal/platform/redis"
	"github.com/AkaneSakuramori/socialmedia/server/pkg/clock"
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

	// --- Domain wiring (ENGINEERING.md §10.2). ---------------------------
	ids, err := idgen.New(cfg.IDGenNodeID, idgen.DefaultEpoch)
	if err != nil {
		return fmt.Errorf("idgen: %w", err)
	}
	beginner := postgres.NewBeginner(pool)
	clk := clock.System()

	tokenFactory, err := loadTokenFactory(cfg, log)
	if err != nil {
		return err
	}
	hasher, err := authsecurity.NewArgon2ID(authsecurity.Argon2Params{
		Memory:      uint32(cfg.Argon2Memory),
		Iterations:  uint32(cfg.Argon2Time),
		Parallelism: uint8(cfg.Argon2Threads),
		SaltLen:     16,
		KeyLen:      32,
	})
	if err != nil {
		return fmt.Errorf("argon2: %w", err)
	}
	otpVerifier := authotp.New(redisClient)
	throttle := auththrottle.New(redisClient, authdomain.DefaultLoginPolicy())

	userRepo := authpostgres.NewUserRepo(pool)
	authSvc := authapp.New(authapp.Deps{
		Users:       userRepo,
		Credentials: authpostgres.NewCredentialRepo(pool),
		Sessions:    authpostgres.NewSessionRepo(pool),
		Hasher:      hasher,
		Tokens:      tokenFactory,
		OTP:         otpVerifier,
		Throttle:    throttle,
		Policy:      authdomain.DefaultLoginPolicy(),
		Audit:       authpostgres.NewAuditLog(pool, log),
		IDs:         ids,
		TxBeginner:  beginner,
		Clock:       clk,

		SessionIdleTimeout: cfg.SessionIdleTimeout,
		SessionRetention:   cfg.SessionRetention,

		AuthTokens:                 authpostgres.NewAuthTokenRepo(pool),
		LoginHistory:               authpostgres.NewLoginHistoryRepo(pool),
		Risk:                       authdomain.PermissiveRisk(),
		Notifier:                   authdomain.NoopNotifier(),
		Verifier:                   tokenFactory,
		PasswordResetTokenTTL:      cfg.PasswordResetTokenTTL,
		ChangeVerificationTokenTTL: cfg.ChangeVerificationTokenTTL,
		DeletionGracePeriod:        cfg.DeletionGracePeriod,
	})

	chatSvc := chatapp.New(chatapp.Deps{
		Conversations:  chatpostgres.NewConversationRepo(pool),
		Memberships:    chatpostgres.NewMembershipRepo(pool),
		Sequences:      chatpostgres.NewSequenceRepo(),
		Messages:       chatpostgres.NewMessageRepo(pool),
		Reactions:      chatpostgres.NewReactionRepo(pool),
		SequenceSource: chatpostgres.NewSequenceSource(pool, redisClient),
		ChangeLog:      chatpostgres.NewChangeLogRepo(pool),
		Users:          userRepo,
		IDs:            ids,
		TxBeginner:     beginner,
		Clock:          clk,
		DB:             postgres.NewQuerier(pool),
	})

	chatRoutes := chathttp.New(chatSvc, authSvc, redisClient)
	srv := httpserver.New(cfg, log, liveness, readiness, chatRoutes.Router())

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

// loadTokenFactory builds the JWT signer/verifier. In dev with no configured
// key it generates an ephemeral one so the process boots without setup;
// production requires APP_JWT_PRIVATE_KEY (config validates this).
func loadTokenFactory(cfg config.Config, log *slog.Logger) (*authsecurity.TokenFactory, error) {
	var key ed25519.PrivateKey
	switch {
	case cfg.JWTPrivateKey != "":
		seed, err := base64.StdEncoding.DecodeString(cfg.JWTPrivateKey)
		if err != nil {
			return nil, fmt.Errorf("decode JWT_PRIVATE_KEY: %w", err)
		}
		if len(seed) != ed25519.SeedSize {
			return nil, fmt.Errorf("JWT_PRIVATE_KEY must be %d bytes, got %d", ed25519.SeedSize, len(seed))
		}
		key = ed25519.NewKeyFromSeed(seed)
	case cfg.AppEnv == "dev":
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		key = priv
		log.Warn("no APP_JWT_PRIVATE_KEY configured; using ephemeral key (dev only)")
	default:
		return nil, errors.New("JWT_PRIVATE_KEY is required outside dev")
	}
	return authsecurity.NewTokenFactory(authsecurity.TokenConfig{
		SigningKey: key,
		Issuer:     cfg.JWTIssuer,
		Audience:   cfg.JWTAudience,
		AccessTTL:  cfg.AccessTokenTTL,
		RefreshTTL: cfg.RefreshTokenTTL,
	})
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
