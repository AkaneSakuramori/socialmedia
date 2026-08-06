package security

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/internal/auth/domain"
)

func newTestFactory(t *testing.T) (*TokenFactory, TokenConfig) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey error: %v", err)
	}
	cfg := TokenConfig{
		SigningKey: priv,
		Issuer:     "https://api.socialmedia.example",
		Audience:   "inchat-api",
		AccessTTL:  15 * time.Minute,
		RefreshTTL: 30 * 24 * time.Hour,
	}
	f, err := NewTokenFactory(cfg)
	if err != nil {
		t.Fatalf("NewTokenFactory error: %v", err)
	}
	return f, cfg
}

func TestNewTokenFactoryValidation(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	base := TokenConfig{SigningKey: priv, Issuer: "iss", Audience: "aud", AccessTTL: time.Minute, RefreshTTL: time.Hour}

	tests := []struct {
		name   string
		mutate func(*TokenConfig)
	}{
		{name: "bad key size", mutate: func(c *TokenConfig) { c.SigningKey = make([]byte, 8) }},
		{name: "empty issuer", mutate: func(c *TokenConfig) { c.Issuer = "" }},
		{name: "empty audience", mutate: func(c *TokenConfig) { c.Audience = "" }},
		{name: "zero access ttl", mutate: func(c *TokenConfig) { c.AccessTTL = 0 }},
		{name: "access >= refresh", mutate: func(c *TokenConfig) { c.AccessTTL = time.Hour; c.RefreshTTL = time.Minute }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base
			tt.mutate(&cfg)
			if _, err := NewTokenFactory(cfg); err == nil {
				t.Error("NewTokenFactory expected error")
			}
		})
	}
}

func TestIssuePairAndVerifyAccess(t *testing.T) {
	f, _ := newTestFactory(t)
	now := time.Now()

	pair, err := f.IssuePair(t.Context(), 7001, 1001, "d-abc", now)
	if err != nil {
		t.Fatalf("IssuePair error: %v", err)
	}

	// Access token is a three-segment JWT.
	if parts := strings.Split(pair.AccessToken, "."); len(parts) != 3 {
		t.Fatalf("access token has %d segments, want 3", len(parts))
	}
	if !pair.AccessExpiresAt.Equal(now.Add(15 * time.Minute)) {
		t.Errorf("AccessExpiresAt = %v, want %v", pair.AccessExpiresAt, now.Add(15*time.Minute))
	}
	if pair.JTI == "" {
		t.Error("JTI must be present")
	}

	// Refresh token is a 43-char base64url opaque value.
	if !domain.IsOpaqueTokenShape(pair.RefreshToken) {
		t.Errorf("refresh token %q is not a valid opaque token", pair.RefreshToken)
	}
	if !pair.RefreshExpiresAt.Equal(now.Add(30 * 24 * time.Hour)) {
		t.Errorf("RefreshExpiresAt = %v", pair.RefreshExpiresAt)
	}

	claims, err := f.VerifyAccess(pair.AccessToken)
	if err != nil {
		t.Fatalf("VerifyAccess error: %v", err)
	}
	if claims.UserID != 1001 || claims.SessionID != 7001 || claims.DeviceID != "d-abc" {
		t.Errorf("claims = %+v, want user 1001 session 7001 device d-abc", claims)
	}
	if len(claims.Scopes) == 0 || claims.Scopes[0] != "user" {
		t.Errorf("scopes = %v, want [user]", claims.Scopes)
	}
	if claims.JTI != pair.JTI {
		t.Errorf("claims.JTI = %q, want %q", claims.JTI, pair.JTI)
	}
	if !claims.ExpiresAt.Equal(now.Add(15 * time.Minute).Truncate(time.Second)) {
		t.Errorf("claims.ExpiresAt = %v", claims.ExpiresAt)
	}
}

func TestVerifyAccessRejectsWrongKey(t *testing.T) {
	f, _ := newTestFactory(t)
	_, otherKey, _ := ed25519.GenerateKey(rand.Reader)
	other, err := NewTokenFactory(TokenConfig{
		SigningKey: otherKey, Issuer: "iss", Audience: "aud",
		AccessTTL: time.Minute, RefreshTTL: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewTokenFactory error: %v", err)
	}
	pair, _ := f.IssuePair(t.Context(), 1, 1, "d", time.Now())
	if _, err := other.VerifyAccess(pair.AccessToken); err == nil {
		t.Fatal("VerifyAccess with a different key should fail")
	}
}

func TestVerifyAccessRejectsExpired(t *testing.T) {
	f, _ := newTestFactory(t)
	now := time.Now()
	pair, _ := f.IssuePair(t.Context(), 1, 1, "d", now)
	if _, err := f.VerifyAccess(pair.AccessToken); err != nil {
		t.Fatalf("VerifyAccess on fresh token error: %v", err)
	}
	// A token issued 16 minutes ago is already expired against its own exp.
	pair, _ = f.IssuePair(t.Context(), 1, 1, "d", now.Add(-16*time.Minute))
	if _, err := f.VerifyAccess(pair.AccessToken); err == nil {
		t.Fatal("VerifyAccess on expired token should fail")
	}
}

func TestVerifyAccessRejectsWrongIssuer(t *testing.T) {
	f, _ := newTestFactory(t)
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	other, err := NewTokenFactory(TokenConfig{
		SigningKey: priv, Issuer: "https://evil.example", Audience: "inchat-api",
		AccessTTL: time.Minute, RefreshTTL: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewTokenFactory error: %v", err)
	}
	pair, _ := other.IssuePair(t.Context(), 1, 1, "d", time.Now())
	if _, err := f.VerifyAccess(pair.AccessToken); err == nil {
		t.Fatal("VerifyAccess with a different issuer should fail")
	}
}

func TestVerifyAccessRejectsTampered(t *testing.T) {
	f, _ := newTestFactory(t)
	pair, _ := f.IssuePair(t.Context(), 1, 1, "d", time.Now())
	parts := strings.Split(pair.AccessToken, ".")
	parts[2] = strings.Repeat("A", len(parts[2]))
	tampered := strings.Join(parts, ".")
	if _, err := f.VerifyAccess(tampered); err == nil {
		t.Fatal("VerifyAccess on tampered token should fail")
	}
}

func TestIssuePairProducesDistinctRefreshTokens(t *testing.T) {
	f, _ := newTestFactory(t)
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	a, _ := f.IssuePair(t.Context(), 1, 1, "d", now)
	b, _ := f.IssuePair(t.Context(), 1, 1, "d", now)
	if a.RefreshToken == b.RefreshToken {
		t.Fatal("two issued refresh tokens must differ")
	}
	if a.JTI == b.JTI {
		t.Fatal("two issued access tokens must have distinct jti")
	}
}
