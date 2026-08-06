package security

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/internal/auth/domain"
	"github.com/golang-jwt/jwt/v5"
)

// TokenFactory issues Ed25519-signed JWT access tokens and opaque refresh
// tokens (ARCHITECTURE.md §10.2, SECURITY_SPEC.md JWT-1/JWT-2, REFR-1/REFR-2).
type TokenFactory struct {
	signingKey ed25519.PrivateKey
	issuer     string
	audience   string
	accessTTL  time.Duration
	refreshTTL time.Duration
}

// TokenConfig carries the signing key and token policies for a TokenFactory.
type TokenConfig struct {
	SigningKey ed25519.PrivateKey
	Issuer     string
	Audience   string
	AccessTTL  time.Duration
	RefreshTTL time.Duration
}

// NewTokenFactory validates configuration and builds the factory.
func NewTokenFactory(cfg TokenConfig) (*TokenFactory, error) {
	if len(cfg.SigningKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("token: ed25519 private key must be %d bytes, got %d", ed25519.PrivateKeySize, len(cfg.SigningKey))
	}
	if cfg.Issuer == "" || cfg.Audience == "" {
		return nil, errors.New("token: issuer and audience are required")
	}
	if cfg.AccessTTL <= 0 || cfg.RefreshTTL <= 0 {
		return nil, errors.New("token: TTLs must be positive")
	}
	if cfg.AccessTTL >= cfg.RefreshTTL {
		return nil, errors.New("token: access TTL must be shorter than refresh TTL")
	}
	return &TokenFactory{
		signingKey: cfg.SigningKey,
		issuer:     cfg.Issuer,
		audience:   cfg.Audience,
		accessTTL:  cfg.AccessTTL,
		refreshTTL: cfg.RefreshTTL,
	}, nil
}

// accessClaims carries the JWT-2 required claims. sub holds the user id as a
// string (API.md §2.2: ids serialize as strings). ver is the user's global
// token version (SESS-6): gateways reject tokens whose ver is older than the
// account's current users.token_version (JWT-5).
type accessClaims struct {
	SessionID    int64    `json:"sid"`
	DeviceID     string   `json:"dev"`
	Scopes       []string `json:"scopes"`
	TokenVersion int64    `json:"ver"`
	jwt.RegisteredClaims
}

// IssuePair mints an access JWT and an opaque refresh token for a session
// (ARCHITECTURE.md §10.2). The raw refresh token is returned once; callers
// persist only domain.HashOpaqueToken(refreshToken) (REFR-2).
func (t *TokenFactory) IssuePair(_ context.Context, sessionID, userID int64, deviceID string, tokenVersion int64, now time.Time) (domain.TokenPair, error) {
	jti, err := newJTI()
	if err != nil {
		return domain.TokenPair{}, fmt.Errorf("token: generate jti: %w", err)
	}

	claims := accessClaims{
		SessionID:    sessionID,
		DeviceID:     deviceID,
		Scopes:       []string{"user"},
		TokenVersion: tokenVersion,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   fmt.Sprintf("%d", userID),
			Issuer:    t.issuer,
			Audience:  jwt.ClaimStrings{t.audience},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(t.accessTTL)),
			ID:        jti,
		},
	}
	access, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims).SignedString(t.signingKey)
	if err != nil {
		return domain.TokenPair{}, fmt.Errorf("token: sign access token: %w", err)
	}

	refresh, err := newRefreshToken()
	if err != nil {
		return domain.TokenPair{}, fmt.Errorf("token: generate refresh token: %w", err)
	}

	return domain.TokenPair{
		AccessToken:      access,
		RefreshToken:     refresh,
		JTI:              jti,
		AccessExpiresAt:  now.Add(t.accessTTL),
		RefreshExpiresAt: now.Add(t.refreshTTL),
	}, nil
}

// AccessClaims is the validated claim set returned by VerifyAccess.
type AccessClaims struct {
	UserID    int64
	SessionID int64
	DeviceID  string
	Scopes    []string
	JTI       string
	// TokenVersion is the ver claim — the user's global token version at
	// issuance. Gateways compare it against users.token_version (JWT-5).
	TokenVersion int64
	ExpiresAt    time.Time
}

// VerifyAccess validates signature, method, issuer, audience, and expiry, and
// returns the claims. This is the gateway trust boundary check
// (SECURITY_SPEC.md JWT-5); session-validity and token version are checked
// separately.
func (t *TokenFactory) VerifyAccess(token string) (*AccessClaims, error) {
	claims := &accessClaims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(*jwt.Token) (any, error) {
		return t.signingKey.Public(), nil
	},
		jwt.WithValidMethods([]string{jwt.SigningMethodEdDSA.Alg()}),
		jwt.WithIssuer(t.issuer),
		jwt.WithAudience(t.audience),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return nil, fmt.Errorf("token: parse: %w", err)
	}
	if !parsed.Valid {
		return nil, errors.New("token: invalid")
	}

	uid, err := parseSubject(claims.Subject)
	if err != nil {
		return nil, fmt.Errorf("token: subject: %w", err)
	}
	return &AccessClaims{
		UserID:       uid,
		SessionID:    claims.SessionID,
		DeviceID:     claims.DeviceID,
		Scopes:       claims.Scopes,
		JTI:          claims.ID,
		TokenVersion: claims.TokenVersion,
		ExpiresAt:    claims.ExpiresAt.Time,
	}, nil
}

func parseSubject(sub string) (int64, error) {
	var id int64
	if _, err := fmt.Sscanf(sub, "%d", &id); err != nil || id <= 0 {
		return 0, errors.New("malformed user id")
	}
	return id, nil
}

// newJTI mints a random 128-bit token identifier for blacklist tracking.
func newJTI() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "jt_" + hex.EncodeToString(b), nil
}

// newRefreshToken mints a 256-bit opaque refresh token (REFR-1: high-entropy,
// random — never a JWT, never parsed).
func newRefreshToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
