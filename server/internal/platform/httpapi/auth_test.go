package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AkaneSakuramori/socialmedia/server/internal/auth/domain"
	userdomain "github.com/AkaneSakuramori/socialmedia/server/internal/user/domain"
)

// fakeAuthenticator records the gateway inputs and returns a fixed result.
type fakeAuthenticator struct {
	user  *userdomain.User
	err   error
	token string
	dev   string
}

func (f *fakeAuthenticator) Authenticate(ctx context.Context, token, deviceID string) (*userdomain.User, error) {
	f.token = token
	f.dev = deviceID
	return f.user, f.err
}

func TestRequireAuthRejectsMissingToken(t *testing.T) {
	h := RequireAuth(&fakeAuthenticator{})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/conversations", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if code := errorCode(t, rec); code != "UNAUTHORIZED" {
		t.Errorf("code = %q, want UNAUTHORIZED", code)
	}
}

func TestRequireAuthRejectsMalformedBearer(t *testing.T) {
	h := RequireAuth(&fakeAuthenticator{})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	for _, header := range []string{"Basic abc", "Bearer", "Bearer ", "Token abc"} {
		req := httptest.NewRequest(http.MethodGet, "/v1/conversations", nil)
		req.Header.Set("Authorization", header)
		req.Header.Set("X-Device-Id", "dev-1")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("header %q: status = %d, want 401", header, rec.Code)
		}
	}
}

func TestRequireAuthRequiresDeviceHeader(t *testing.T) {
	h := RequireAuth(&fakeAuthenticator{})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/v1/conversations", nil)
	req.Header.Set("Authorization", "Bearer abc")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	if code := errorCode(t, rec); code != "VALIDATION_ERROR" {
		t.Errorf("code = %q, want VALIDATION_ERROR", code)
	}
}

func TestRequireAuthMapsGatewayErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code string
		http int
	}{
		{"expired", domain.ErrTokenExpired, "TOKEN_EXPIRED", http.StatusUnauthorized},
		{"revoked", domain.ErrTokenRevoked, "TOKEN_REVOKED", http.StatusUnauthorized},
		{"session revoked", domain.ErrSessionRevoked, "SESSION_REVOKED", http.StatusUnauthorized},
		{"invalid", domain.ErrTokenInvalid, "UNAUTHORIZED", http.StatusUnauthorized},
		{"suspended", domain.ErrAccountSuspended, "ACCOUNT_SUSPENDED", http.StatusForbidden},
		{"deleted", domain.ErrAccountDeleted, "ACCOUNT_DELETED", http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := RequireAuth(&fakeAuthenticator{err: tt.err})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			req := httptest.NewRequest(http.MethodGet, "/v1/conversations", nil)
			req.Header.Set("Authorization", "Bearer abc")
			req.Header.Set("X-Device-Id", "dev-1")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tt.http {
				t.Errorf("status = %d, want %d", rec.Code, tt.http)
			}
			if code := errorCode(t, rec); code != tt.code {
				t.Errorf("code = %q, want %q", code, tt.code)
			}
		})
	}
}

func TestRequireAuthBindsPrincipal(t *testing.T) {
	auth := &fakeAuthenticator{user: &userdomain.User{ID: 42}}
	var gotPrincipal Principal
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPrincipal, _ = PrincipalFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	h := RequireAuth(auth)(next)
	req := httptest.NewRequest(http.MethodGet, "/v1/conversations", nil)
	req.Header.Set("Authorization", "Bearer token-123")
	req.Header.Set("X-Device-Id", "dev-9")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if auth.token != "token-123" || auth.dev != "dev-9" {
		t.Errorf("gateway inputs = (%q, %q), want (token-123, dev-9)", auth.token, auth.dev)
	}
	if gotPrincipal.UserID() != 42 || gotPrincipal.DeviceID != "dev-9" {
		t.Errorf("principal = %+v, want user 42 / dev-9", gotPrincipal)
	}
}

func TestBearerTokenExtraction(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "bearer abc.def")
	if got := bearerToken(req); got != "abc.def" {
		t.Errorf("bearerToken = %q, want abc.def", got)
	}
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return body.Code
}
