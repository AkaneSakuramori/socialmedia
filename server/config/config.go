// Package config provides the typed, validated process configuration.
//
// Configuration follows ENGINEERING.md §11: one Config struct per process,
// assembled from environment variables (12-factor) with local-dev defaults.
// Every field is validated at startup and a misconfigured server must not start.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the validated configuration for the api-server process.
type Config struct {
	// AppEnv selects the environment bundle: dev | staging | prod.
	AppEnv string
	// HTTPPort is the listen port for the HTTP server.
	HTTPPort string
	// ReadHeaderTimeout bounds the time to read request headers (slowloris guard).
	ReadHeaderTimeout time.Duration
	// ShutdownTimeout bounds the graceful shutdown drain.
	ShutdownTimeout time.Duration

	// PGDSN is the PostgreSQL connection string.
	PGDSN string
	// PGMaxConns is the maximum size of the pgx connection pool.
	PGMaxConns int32

	// RedisAddr is the Redis host:port.
	RedisAddr string
	// RedisPassword authenticates the Redis connection (empty when unset).
	RedisPassword string
	// RedisDB is the selected Redis logical database.
	RedisDB int

	// JWTIssuer is the issuer claim of issued access tokens.
	JWTIssuer string
	// JWTAudience is the audience claim of issued access tokens.
	JWTAudience string
	// JWTPrivateKey is the base64-encoded Ed25519 seed used to sign access
	// tokens. Empty in dev means "generate an ephemeral key at startup";
	// required (non-empty) in staging/prod.
	JWTPrivateKey string
	// AccessTokenTTL is how long an access token remains valid before refresh.
	AccessTokenTTL time.Duration
	// RefreshTokenTTL is the sliding lifetime of a session's refresh token
	// (SECURITY_SPEC.md REFR-6: 30–90 days).
	RefreshTokenTTL time.Duration

	// Argon2Memory, Argon2Time, Argon2Threads configure the Argon2id password
	// KDF (SECURITY_SPEC.md PASS-1). Memory is in KiB.
	Argon2Memory  int
	Argon2Time    int
	Argon2Threads int
	// IDGenNodeID is this instance's idgen node id (0–1023); each instance in a
	// deployment must use a distinct value.
	IDGenNodeID int

	// LoginMaxFailures is the consecutive-failure threshold before an
	// identifier locks (SECURITY_SPEC.md AUTH-5).
	LoginMaxFailures int
	// LoginLockoutDuration is how long an identifier stays locked (AUTH-5).
	LoginLockoutDuration time.Duration
	// SessionIdleTimeout is how long a session may stay inactive before it
	// expires (SECURITY_SPEC.md SESS-9 sliding idle timeout). Must be <=
	// RefreshTokenTTL so the sliding window cannot outlive the refresh TTL.
	SessionIdleTimeout time.Duration
	// SessionRetention is how long revoked/expired session rows are kept before
	// purge (DATABASE.md §4.4 retention: 90 days).
	SessionRetention time.Duration
}

// Defaults for local development. Deployed environments override via env vars.
const (
	defaultHTTPPort          = "8080"
	defaultReadHeaderTimeout = 5 * time.Second
	defaultShutdownTimeout   = 15 * time.Second
	defaultPGMaxConns        = 10
	defaultRedisAddr         = "localhost:6379"
	defaultRedisDB           = 0

	// localPGDSN matches infra/docker/docker-compose.yml. Only used when
	// APP_ENV=dev and APP_PG_DSN is unset.
	localPGDSN = "postgres://app:app_password@localhost:5432/inchat?sslmode=disable"

	defaultJWTIssuer        = "https://api.socialmedia.example"
	defaultJWTAudience      = "inchat-api"
	defaultAccessTokenTTL   = 15 * time.Minute
	defaultRefreshTokenTTL  = 30 * 24 * time.Hour
	defaultArgon2Memory     = 64 * 1024 // 64 MiB (OWASP-recommended floor)
	defaultArgon2Time       = 3
	defaultArgon2Threads    = 4
	defaultLoginMaxFailures = 5
	defaultLoginLockout     = 5 * time.Minute
	defaultSessionIdle      = 30 * 24 * time.Hour
	defaultSessionRetention = 90 * 24 * time.Hour
)

// Load reads configuration from the environment, applies defaults, and
// validates it. It returns an error describing every invalid field, so a
// misconfigured server fails fast before binding anything.
func Load() (Config, error) {
	cfg := Config{
		AppEnv:               getEnv("APP_ENV", "dev"),
		HTTPPort:             getEnv("APP_HTTP_PORT", defaultHTTPPort),
		ReadHeaderTimeout:    getDuration("APP_HTTP_READ_HEADER_TIMEOUT", defaultReadHeaderTimeout),
		ShutdownTimeout:      getDuration("APP_SHUTDOWN_TIMEOUT", defaultShutdownTimeout),
		PGDSN:                os.Getenv("APP_PG_DSN"),
		PGMaxConns:           int32(getInt("APP_PG_MAX_CONNS", defaultPGMaxConns)),
		RedisAddr:            getEnv("APP_REDIS_ADDR", defaultRedisAddr),
		RedisPassword:        os.Getenv("APP_REDIS_PASSWORD"),
		RedisDB:              getInt("APP_REDIS_DB", defaultRedisDB),
		JWTIssuer:            getEnv("APP_JWT_ISSUER", defaultJWTIssuer),
		JWTAudience:          getEnv("APP_JWT_AUDIENCE", defaultJWTAudience),
		JWTPrivateKey:        os.Getenv("APP_JWT_PRIVATE_KEY"),
		AccessTokenTTL:       getDuration("APP_ACCESS_TOKEN_TTL", defaultAccessTokenTTL),
		RefreshTokenTTL:      getDuration("APP_REFRESH_TOKEN_TTL", defaultRefreshTokenTTL),
		Argon2Memory:         getInt("APP_ARGON2_MEMORY_KIB", defaultArgon2Memory),
		Argon2Time:           getInt("APP_ARGON2_TIME", defaultArgon2Time),
		Argon2Threads:        getInt("APP_ARGON2_THREADS", defaultArgon2Threads),
		IDGenNodeID:          getInt("APP_IDGEN_NODE_ID", 0),
		LoginMaxFailures:     getInt("APP_LOGIN_MAX_FAILURES", defaultLoginMaxFailures),
		LoginLockoutDuration: getDuration("APP_LOGIN_LOCKOUT_DURATION", defaultLoginLockout),
		SessionIdleTimeout:   getDuration("APP_SESSION_IDLE_TIMEOUT", defaultSessionIdle),
		SessionRetention:     getDuration("APP_SESSION_RETENTION", defaultSessionRetention),
	}

	// Local dev convenience: a default DSN matching the compose stack.
	if cfg.PGDSN == "" {
		if cfg.AppEnv == "dev" {
			cfg.PGDSN = localPGDSN
		} else {
			return Config{}, fmt.Errorf("APP_PG_DSN is required when APP_ENV=%q", cfg.AppEnv)
		}
	}

	// Signing keys must never be silently generated in a real environment.
	if cfg.AppEnv != "dev" && cfg.JWTPrivateKey == "" {
		return Config{}, fmt.Errorf("APP_JWT_PRIVATE_KEY is required when APP_ENV=%q", cfg.AppEnv)
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate checks every field and returns an aggregate error of all failures.
func (c Config) Validate() error {
	var errs []string

	switch c.AppEnv {
	case "dev", "staging", "prod":
	default:
		errs = append(errs, fmt.Sprintf("APP_ENV must be one of dev|staging|prod, got %q", c.AppEnv))
	}

	if p, err := strconv.Atoi(c.HTTPPort); err != nil || p < 1 || p > 65535 {
		errs = append(errs, fmt.Sprintf("APP_HTTP_PORT must be a valid TCP port, got %q", c.HTTPPort))
	}
	if c.ReadHeaderTimeout <= 0 {
		errs = append(errs, "APP_HTTP_READ_HEADER_TIMEOUT must be > 0")
	}
	if c.ShutdownTimeout <= 0 {
		errs = append(errs, "APP_SHUTDOWN_TIMEOUT must be > 0")
	}

	if u, err := url.Parse(c.PGDSN); err != nil || u.Scheme == "" {
		errs = append(errs, "APP_PG_DSN must be a valid postgres:// connection string")
	}
	if c.PGMaxConns < 1 {
		errs = append(errs, "APP_PG_MAX_CONNS must be >= 1")
	}
	if c.RedisAddr == "" {
		errs = append(errs, "APP_REDIS_ADDR must not be empty")
	}

	if c.JWTIssuer == "" {
		errs = append(errs, "APP_JWT_ISSUER must not be empty")
	}
	if c.JWTAudience == "" {
		errs = append(errs, "APP_JWT_AUDIENCE must not be empty")
	}
	if c.AccessTokenTTL <= 0 {
		errs = append(errs, "APP_ACCESS_TOKEN_TTL must be > 0")
	}
	if c.RefreshTokenTTL <= 0 {
		errs = append(errs, "APP_REFRESH_TOKEN_TTL must be > 0")
	}
	if c.AccessTokenTTL >= c.RefreshTokenTTL {
		errs = append(errs, "APP_ACCESS_TOKEN_TTL must be shorter than APP_REFRESH_TOKEN_TTL")
	}
	if c.Argon2Memory < 8*c.Argon2Threads {
		errs = append(errs, "APP_ARGON2_MEMORY_KIB must be >= 8 * APP_ARGON2_THREADS (Argon2 minimum)")
	}
	if c.Argon2Time < 1 {
		errs = append(errs, "APP_ARGON2_TIME must be >= 1")
	}
	if c.Argon2Threads < 1 || c.Argon2Threads > 255 {
		errs = append(errs, "APP_ARGON2_THREADS must be in [1, 255]")
	}
	if c.IDGenNodeID < 0 || c.IDGenNodeID > 1023 {
		errs = append(errs, "APP_IDGEN_NODE_ID must be in [0, 1023]")
	}
	if c.LoginMaxFailures < 1 {
		errs = append(errs, "APP_LOGIN_MAX_FAILURES must be >= 1")
	}
	if c.LoginLockoutDuration <= 0 {
		errs = append(errs, "APP_LOGIN_LOCKOUT_DURATION must be > 0")
	}
	if c.SessionIdleTimeout <= 0 {
		errs = append(errs, "APP_SESSION_IDLE_TIMEOUT must be > 0")
	}
	if c.SessionIdleTimeout > c.RefreshTokenTTL {
		errs = append(errs, "APP_SESSION_IDLE_TIMEOUT must not exceed APP_REFRESH_TOKEN_TTL (sliding window)")
	}
	if c.SessionRetention <= 0 {
		errs = append(errs, "APP_SESSION_RETENTION must be > 0")
	}

	if len(errs) > 0 {
		return fmt.Errorf("invalid configuration:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

// String returns a redacted representation suitable for logging (ENGINEERING.md
// §11: sensitive config is never logged; DSNs log with the password masked).
func (c Config) String() string {
	dsn := c.PGDSN
	if u, err := url.Parse(dsn); err == nil {
		if u.User != nil {
			u.User = url.UserPassword(u.User.Username(), "xxxxx")
		}
		dsn = u.String()
	}
	pass := c.RedisPassword
	if pass != "" {
		pass = "xxxxx"
	}
	key := c.JWTPrivateKey
	if key != "" {
		key = "xxxxx"
	}
	return fmt.Sprintf(
		"{AppEnv:%s HTTPPort:%s ReadHeaderTimeout:%s ShutdownTimeout:%s PGDSN:%s PGMaxConns:%d RedisAddr:%s RedisPassword:%s RedisDB:%d JWTIssuer:%s JWTAudience:%s JWTPrivateKey:%s AccessTokenTTL:%s RefreshTokenTTL:%s Argon2:%d/%d/%d IDGenNodeID:%d LoginPolicy:%d/%s Sessions:%s/%s}",
		c.AppEnv, c.HTTPPort, c.ReadHeaderTimeout, c.ShutdownTimeout,
		dsn, c.PGMaxConns, c.RedisAddr, pass, c.RedisDB,
		c.JWTIssuer, c.JWTAudience, key, c.AccessTokenTTL, c.RefreshTokenTTL,
		c.Argon2Memory, c.Argon2Time, c.Argon2Threads, c.IDGenNodeID,
		c.LoginMaxFailures, c.LoginLockoutDuration,
		c.SessionIdleTimeout, c.SessionRetention,
	)
}

func getEnv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func getInt(key string, def int) int {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func getDuration(key string, def time.Duration) time.Duration {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
