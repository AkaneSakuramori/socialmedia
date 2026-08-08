// Command minttoken mints an access JWT for the container-level E2E flow.
//
// It signs with the same APP_JWT_PRIVATE_KEY the api-server process was started
// with (base64 Ed25519 seed), reproducing the exact accessClaims wire shape the
// gateway verifier requires (SECURITY_SPEC.md JWT-2/JWT-5). It is an E2E test
// helper only; it is never part of the shipped binary surface.
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type accessClaims struct {
	SessionID    int64    `json:"sid"`
	DeviceID     string   `json:"dev"`
	Scopes       []string `json:"scopes"`
	TokenVersion int64    `json:"ver"`
	jwt.RegisteredClaims
}

func main() {
	seed, err := base64.StdEncoding.DecodeString(os.Getenv("APP_JWT_PRIVATE_KEY"))
	must(err, "decode APP_JWT_PRIVATE_KEY")
	if len(seed) != ed25519.SeedSize {
		must(fmt.Errorf("APP_JWT_PRIVATE_KEY must be %d bytes, got %d", ed25519.SeedSize, len(seed)), "seed")
	}
	priv := ed25519.NewKeyFromSeed(seed)

	now := time.Now()
	claims := accessClaims{
		SessionID:    mustInt(os.Getenv("E2E_SESSION_ID"), "E2E_SESSION_ID"),
		DeviceID:     os.Getenv("E2E_DEVICE_ID"),
		Scopes:       []string{"user"},
		TokenVersion: mustInt(os.Getenv("E2E_TOKEN_VERSION"), "E2E_TOKEN_VERSION"),
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   strconv.FormatInt(mustInt(os.Getenv("E2E_USER_ID"), "E2E_USER_ID"), 10),
			Issuer:    envOr("APP_JWT_ISSUER", "https://api.socialmedia.example"),
			Audience:  jwt.ClaimStrings{envOr("APP_JWT_AUDIENCE", "inchat-api")},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(15 * time.Minute)),
			ID:        "jt_e2e_" + strconv.FormatInt(now.UnixNano(), 36),
		},
	}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims).SignedString(priv)
	must(err, "sign access token")
	fmt.Println(tok)
}

func mustInt(raw, name string) int64 {
	n, err := strconv.ParseInt(raw, 10, 64)
	must(err, name)
	return n
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func must(err error, ctx string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "minttoken: %s: %v\n", ctx, err)
		os.Exit(1)
	}
}
