package config

import (
	"strings"
	"testing"
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
		"APP_ENV":          "staging",
		"APP_HTTP_PORT":    "9090",
		"APP_PG_DSN":       "postgres://user:pass@db:5432/inchat",
		"APP_PG_MAX_CONNS": "25",
		"APP_REDIS_ADDR":   "redis:6379",
		"APP_REDIS_DB":     "3",
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
}

func TestLoadFailsFast(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{name: "invalid env", env: map[string]string{"APP_ENV": "local"}},
		{name: "missing DSN in prod", env: map[string]string{"APP_ENV": "prod", "APP_PG_DSN": ""}},
		{name: "invalid port", env: map[string]string{"APP_HTTP_PORT": "notaport"}},
		{name: "bad dsn", env: map[string]string{"APP_PG_DSN": "not-a-dsn"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setEnv(t, map[string]string{
				"APP_ENV":       tt.env["APP_ENV"],
				"APP_HTTP_PORT": tt.env["APP_HTTP_PORT"],
				"APP_PG_DSN":    tt.env["APP_PG_DSN"],
			})
			if _, err := Load(); err == nil {
				t.Fatal("Load() expected error, got nil")
			}
		})
	}
	clearEnv(t, "APP_ENV", "APP_HTTP_PORT", "APP_PG_DSN")
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
