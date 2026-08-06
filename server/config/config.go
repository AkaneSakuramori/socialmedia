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
)

// Load reads configuration from the environment, applies defaults, and
// validates it. It returns an error describing every invalid field, so a
// misconfigured server fails fast before binding anything.
func Load() (Config, error) {
	cfg := Config{
		AppEnv:            getEnv("APP_ENV", "dev"),
		HTTPPort:          getEnv("APP_HTTP_PORT", defaultHTTPPort),
		ReadHeaderTimeout: getDuration("APP_HTTP_READ_HEADER_TIMEOUT", defaultReadHeaderTimeout),
		ShutdownTimeout:   getDuration("APP_SHUTDOWN_TIMEOUT", defaultShutdownTimeout),
		PGDSN:             os.Getenv("APP_PG_DSN"),
		PGMaxConns:        int32(getInt("APP_PG_MAX_CONNS", defaultPGMaxConns)),
		RedisAddr:         getEnv("APP_REDIS_ADDR", defaultRedisAddr),
		RedisPassword:     os.Getenv("APP_REDIS_PASSWORD"),
		RedisDB:           getInt("APP_REDIS_DB", defaultRedisDB),
	}

	// Local dev convenience: a default DSN matching the compose stack.
	if cfg.PGDSN == "" {
		if cfg.AppEnv == "dev" {
			cfg.PGDSN = localPGDSN
		} else {
			return Config{}, fmt.Errorf("APP_PG_DSN is required when APP_ENV=%q", cfg.AppEnv)
		}
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
	return fmt.Sprintf(
		"{AppEnv:%s HTTPPort:%s ReadHeaderTimeout:%s ShutdownTimeout:%s PGDSN:%s PGMaxConns:%d RedisAddr:%s RedisPassword:%s RedisDB:%d}",
		c.AppEnv, c.HTTPPort, c.ReadHeaderTimeout, c.ShutdownTimeout,
		dsn, c.PGMaxConns, c.RedisAddr, pass, c.RedisDB,
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
