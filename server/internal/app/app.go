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
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

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
	"github.com/AkaneSakuramori/socialmedia/server/internal/realtime/delivery/ws"
	realtimeinfra "github.com/AkaneSakuramori/socialmedia/server/internal/realtime/infra"
	"github.com/AkaneSakuramori/socialmedia/server/internal/realtime/presence"
	"github.com/AkaneSakuramori/socialmedia/server/internal/realtime/typing"
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

	// Realtime gateway (API.md §16–18): the WS endpoint authenticates each
	// socket, binds it to (user_id, session_id), and drives C2S ops via the chat
	// service. Fan-out is log-dispatch: the outbox relay publishes committed
	// change_log rows to Redis; the dispatcher routes them to local sockets
	// (ARCHITECTURE.md §13.1). Presence and typing ride the same backplane as
	// ephemeral events (ARCHITECTURE.md §15/§16).
	changeLogRepo := chatpostgres.NewChangeLogRepo(pool)
	replayBuf := ws.NewReplayBuffer(ws.DefaultReplayConfig())

	pub := realtimeinfra.NewRedisPublisher(redisClient)
	presenceCfg := presence.DefaultConfig()
	presenceCfg.Instance = instanceID(cfg)
	presenceStore := presence.NewRedisStore(redisClient, presenceCfg)
	presenceSvc := presence.NewService(presenceStore, presenceCfg, ws.NewPresenceNotifier(pub, log), log)
	typingStore := typing.NewRedisStore(redisClient, typing.DefaultConfig())
	typingSvc := typing.NewService(typingStore, typing.DefaultConfig(), ws.NewTypingNotifier(pub, log), log)

	wsHub := ws.NewHub(ws.DefaultConfig(),
		ws.NewHandler(chatSvc, log).
			WithReplayer(replayBuf).
			WithHeadSource(changeLogRepo).
			WithPresence(presenceSvc).
			WithTyping(typingSvc),
		log)
	wsEndpoint := ws.NewEndpoint(wsHub, authSvc, changeLogRepo, log).
		WithPresence(presenceSvc).
		WithTyping(typingSvc)
	revokeWatcher := ws.NewSessionRevokeWatcher(wsHub, log)
	wsDispatcher := ws.NewDispatcher(wsHub, replayBuf, log)
	wsRelay := realtimeinfra.NewRelay(
		changeLogRepo,
		pub,
		realtimeinfra.DefaultRelayConfig(),
		log,
	)

	root := http.NewServeMux()
	root.Handle("/v1/", chatRoutes.Router())
	root.Handle("GET /v1/ws", wsEndpoint)

	srv := httpserver.New(cfg, log, liveness, readiness, root)

	// Session-revoke watcher: force-closes sockets bound to a revoked session
	// (API.md §18.19, code 4403). Best-effort; a missed signal is caught by the
	// next gateway token check. Supervised: an unexpected exit (e.g. a Redis
	// backplane blip that closed the subscription) is restarted with backoff.
	wctx, stopWatchers := context.WithCancel(context.Background())
	defer stopWatchers()
	go supervise(wctx, log, "session-revoke watcher", func(ctx context.Context) error {
		return revokeWatcher.Run(ctx, redisClient)
	})

	// Outbox relay + dispatcher: move committed change_log rows onto the Redis
	// backplane and route them to local sockets. The relay starts at the current
	// change_log head; pre-existing history is not re-published (sync backfills).
	// The dispatcher is supervised so a dropped subscription self-heals.
	go func() {
		if err := wsRelay.Run(wctx); err != nil {
			log.Error("realtime: outbox relay stopped", "error", err)
		}
	}()
	go supervise(wctx, log, "dispatcher", func(ctx context.Context) error {
		return wsDispatcher.Run(ctx, redisClient)
	})

	// Presence heartbeat sweeper: extends the presence TTL for every user with
	// live local connections (heartbeat-based presence expiration).
	if presenceSvc != nil {
		go func() {
			if err := presence.NewSweeper(presenceStore, wsHub, presenceCfg, log).Run(wctx); err != nil {
				log.Error("realtime: presence sweeper stopped", "error", err)
			}
		}()
	}

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

	// Drain the realtime gateway: broadcast server.shutdown, flush, then force-
	// close every socket (API.md §18.21, WS-8).
	wsHub.Shutdown(shutdownCtx, cfg.ShutdownTimeout)
	log.Info("realtime gateway stopped")

	// Defers close Redis, then the pool, in reverse order.
	log.Info("api-server stopped cleanly")
	return nil
}

// instanceID derives a stable per-process identity for the presence
// aggregation keys (multi-instance presence, ARCHITECTURE.md §15). Distinct
// processes must not share an id: node id where configured, hostname otherwise.
func instanceID(cfg config.Config) string {
	if cfg.IDGenNodeID != 0 {
		return "node-" + strconv.Itoa(cfg.IDGenNodeID)
	}
	if host, err := os.Hostname(); err == nil && host != "" {
		return host
	}
	return "default"
}

// supervise runs fn as a long-lived worker and restarts it with bounded
// backoff when it exits unexpectedly (dispatcher failure handling, §37.4). It
// returns when ctx is cancelled; a worker that exits without error on ctx
// cancellation is not restarted.
func supervise(ctx context.Context, log *slog.Logger, name string, fn func(context.Context) error) {
	backoff := 250 * time.Millisecond
	for {
		err := fn(ctx)
		if err == nil {
			return
		}
		log.Error("realtime: worker exited, restarting", "name", name, "error", err, "backoff", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > 5*time.Second {
			backoff = 5 * time.Second
		}
	}
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
