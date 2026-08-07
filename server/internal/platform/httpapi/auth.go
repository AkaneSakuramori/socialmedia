package httpapi

import (
	"context"
	"net/http"
	"strings"

	userdomain "github.com/AkaneSakuramori/socialmedia/server/internal/user/domain"
)

// Authenticator validates an access token and returns the account behind it.
// The auth application service implements it; the middleware never re-implements
// token logic (SECURITY_SPEC.md JWT-5).
type Authenticator interface {
	Authenticate(ctx context.Context, token, deviceID string) (*userdomain.User, error)
}

// Principal is the authenticated caller bound to the request context.
type Principal struct {
	User     *userdomain.User
	DeviceID string
}

// UserID returns the authenticated user's id (the common handler need).
func (p Principal) UserID() int64 { return p.User.ID }

type principalKey struct{}

// WithPrincipal binds the authenticated principal to ctx.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom reads the authenticated principal from ctx.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// RequireAuth is the bearer-token gateway middleware. It rejects requests
// without a Bearer token (401 UNAUTHORIZED), without the X-Device-Id header
// (400), or whose token fails gateway validation (401 TOKEN_EXPIRED /
// TOKEN_REVOKED / SESSION_REVOKED, 403 ACCOUNT_SUSPENDED / ACCOUNT_DELETED).
// On success it binds the Principal to the request context.
func RequireAuth(auth Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := bearerToken(r)
			if token == "" {
				WriteError(w, r, unauthorized("missing access token"))
				return
			}
			deviceID := r.Header.Get("X-Device-Id")
			if deviceID == "" {
				WriteError(w, r, validationErr("X-Device-Id", "required"))
				return
			}
			user, err := auth.Authenticate(r.Context(), token, deviceID)
			if err != nil {
				WriteError(w, r, err)
				return
			}
			ctx := WithPrincipal(r.Context(), Principal{User: user, DeviceID: deviceID})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// bearerToken extracts the token from an Authorization: Bearer <token> header.
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return ""
	}
	return parts[1]
}
