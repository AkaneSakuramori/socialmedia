package config

import (
	"strings"
	"testing"
	"time"
)

func setEnv(t *testing.T, values map[string]string) {
	t.Helper()
	for k, v := range values {
		t.Setenv(k, v)
	}
}

func clearEnv(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		t.Setenv(k, "")
	}
}

func TestLoadDefaultsForDev(t *testing.T) {
	setEnv(t, map[string]string{
		"APP_ENV":        "dev",
		"APP_PG_DSN":     "",
		"APP_REDIS_ADDR": "",
	})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.AppEnv != "dev" {
		t.Errorf("AppEnv = %q, want dev", cfg.AppEnv)
	}
	if cfg.HTTPPort != "8080" {
		t.Errorf("HTTPPort = %q, want 8080", cfg.HTTPPort)
	}
	if cfg.PGDSN != localPGDSN {
		t.Errorf("PGDSN = %q, want local default %q", cfg.PGDSN, localPGDSN)
	}
	if cfg.RedisAddr != "localhost:6379" {
		t.Errorf("RedisAddr = %q, want localhost:6379", cfg.RedisAddr)
	}
}

func TestLoadReadsEnvironment(t *testing.T) {
	setEnv(t, map[string]string{
		"APP_ENV":               "staging",
		"APP_HTTP_PORT":         "9090",
		"APP_PG_DSN":            "postgres://user:pass@db:5432/inchat",
		"APP_PG_MAX_CONNS":      "25",
		"APP_REDIS_ADDR":        "redis:6379",
		"APP_REDIS_DB":          "3",
		"APP_JWT_PRIVATE_KEY":   "c2VjcmV0c2VlZA==",
		"APP_ACCESS_TOKEN_TTL":  "5m",
		"APP_REFRESH_TOKEN_TTL": "480h",
		"APP_ARGON2_MEMORY_KIB": "32768",
		"APP_ARGON2_TIME":       "2",
		"APP_ARGON2_THREADS":    "2",
		"APP_IDGEN_NODE_ID":     "12",
	})
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.AppEnv != "staging" || cfg.HTTPPort != "9090" || cfg.PGDSN != "postgres://user:pass@db:5432/inchat" {
		t.Errorf("unexpected config loaded: %s", cfg.String())
	}
	if cfg.PGMaxConns != 25 || cfg.RedisAddr != "redis:6379" || cfg.RedisDB != 3 {
		t.Errorf("unexpected config loaded: %s", cfg.String())
	}
	if cfg.AccessTokenTTL.String() != "5m0s" || cfg.RefreshTokenTTL.String() != "480h0m0s" {
		t.Errorf("unexpected token TTLs: %s", cfg.String())
	}
	if cfg.Argon2Memory != 32768 || cfg.Argon2Time != 2 || cfg.Argon2Threads != 2 || cfg.IDGenNodeID != 12 {
		t.Errorf("unexpected auth config: %s", cfg.String())
	}
}

func TestLoadFailsFast(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{name: "invalid env", env: map[string]string{"APP_ENV": "local"}},
		{name: "missing DSN in prod", env: map[string]string{"APP_ENV": "prod", "APP_PG_DSN": ""}},
		{name: "missing signing key in staging", env: map[string]string{"APP_ENV": "staging", "APP_PG_DSN": "postgres://u:p@db:5432/i"}},
		{name: "invalid port", env: map[string]string{"APP_HTTP_PORT": "notaport"}},
		{name: "bad dsn", env: map[string]string{"APP_PG_DSN": "not-a-dsn"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setEnv(t, map[string]string{
				"APP_ENV":        tt.env["APP_ENV"],
				"APP_HTTP_PORT":  tt.env["APP_HTTP_PORT"],
				"APP_PG_DSN":     tt.env["APP_PG_DSN"],
				"APP_REDIS_ADDR": "localhost:6379",
			})
			if _, err := Load(); err == nil {
				t.Fatal("Load() expected error, got nil")
			}
		})
	}
	clearEnv(t, "APP_ENV", "APP_HTTP_PORT", "APP_PG_DSN", "APP_REDIS_ADDR")
}

func TestValidateAuthFields(t *testing.T) {
	base := Config{AppEnv: "dev", HTTPPort: "8080", PGDSN: localPGDSN, RedisAddr: "localhost:6379"}

	tests := []struct {
		name   string
		mutate func(*Config)
		expect string
	}{
		{
			name: "access ttl >= refresh ttl",
			mutate: func(c *Config) {
				c.AccessTokenTTL = 30 * 24 * time.Hour
				c.RefreshTokenTTL = 15 * time.Minute
			},
			expect: "APP_ACCESS_TOKEN_TTL must be shorter",
		},
		{
			name: "access ttl zero",
			mutate: func(c *Config) {
				c.AccessTokenTTL = 0
				c.RefreshTokenTTL = 30 * 24 * time.Hour
			},
			expect: "APP_ACCESS_TOKEN_TTL must be > 0",
		},
		{
			name: "argon2 memory below minimum",
			mutate: func(c *Config) {
				c.Argon2Memory = 4
				c.Argon2Threads = 4
			},
			expect: "APP_ARGON2_MEMORY_KIB",
		},
		{
			name: "argon2 threads out of range",
			mutate: func(c *Config) {
				c.Argon2Threads = 256
			},
			expect: "APP_ARGON2_THREADS",
		},
		{
			name: "idgen node out of range",
			mutate: func(c *Config) {
				c.IDGenNodeID = 1024
			},
			expect: "APP_IDGEN_NODE_ID",
		},
		{
			name: "login max failures zero",
			mutate: func(c *Config) {
				c.LoginMaxFailures = 0
			},
			expect: "APP_LOGIN_MAX_FAILURES",
		},
		{
			name: "login lockout zero",
			mutate: func(c *Config) {
				c.LoginLockoutDuration = 0
			},
			expect: "APP_LOGIN_LOCKOUT_DURATION",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base
			tt.mutate(&cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatal("Validate() expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.expect) {
				t.Errorf("Validate() error missing %q: %s", tt.expect, err)
			}
		})
	}
}

func TestValidateAuthFieldsAcceptDefaults(t *testing.T) {
	cfg := Config{
		AppEnv:               "dev",
		HTTPPort:             "8080",
		ReadHeaderTimeout:    5 * time.Second,
		ShutdownTimeout:      15 * time.Second,
		PGDSN:                localPGDSN,
		PGMaxConns:           10,
		RedisAddr:            "localhost:6379",
		JWTIssuer:            "https://api.socialmedia.example",
		JWTAudience:          "inchat-api",
		AccessTokenTTL:       15 * time.Minute,
		RefreshTokenTTL:      30 * 24 * time.Hour,
		Argon2Memory:         64 * 1024,
		Argon2Time:           3,
		Argon2Threads:        4,
		LoginMaxFailures:     5,
		LoginLockoutDuration: 5 * time.Minute,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() with default auth fields error = %v", err)
	}
}

func TestValidateAggregatesAllErrors(t *testing.T) {
	cfg := Config{AppEnv: "bogus", HTTPPort: "x", PGDSN: "y", RedisAddr: ""}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() expected error")
	}
	msg := err.Error()
	for _, want := range []string{"APP_ENV", "APP_HTTP_PORT", "APP_PG_DSN", "APP_REDIS_ADDR"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Validate() error missing %q: %s", want, msg)
		}
	}
}

func TestStringRedactsSecrets(t *testing.T) {
	cfg := Config{
		AppEnv:        "staging",
		HTTPPort:      "8080",
		PGDSN:         "postgres://app:hunter2@db:5432/inchat?sslmode=disable",
		RedisPassword: "super-secret",
	}
	s := cfg.String()
	if strings.Contains(s, "hunter2") {
		t.Errorf("String() leaked DSN password: %s", s)
	}
	if strings.Contains(s, "super-secret") {
		t.Errorf("String() leaked redis password: %s", s)
	}
}
