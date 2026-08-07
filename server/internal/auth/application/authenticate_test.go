package application

import (
	"errors"
	"testing"

	"github.com/AkaneSakuramori/socialmedia/server/internal/auth/domain"
	userdomain "github.com/AkaneSakuramori/socialmedia/server/internal/user/domain"
)

// seedAuthenticatedUser registers a live account + active session so
// Authenticate can pass the account/session checks.
func (h *harness) seedAuthenticatedUser(t *testing.T, userID int64, tokenVersion int64) {
	t.Helper()
	u := &userdomain.User{ID: userID, DisplayName: "Aya", AccountState: userdomain.AccountActive, TokenVersion: tokenVersion}
	if err := h.users.Create(t.Context(), nil, u); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	sess := &domain.Session{
		ID:           7001,
		UserID:       userID,
		Device:       domain.DeviceInfo{DeviceID: "d-abc"},
		State:        domain.SessionActive,
		LastActiveAt: h.clk.Now(),
		CreatedAt:    h.clk.Now(),
		UpdatedAt:    h.clk.Now(),
	}
	if err := h.sess.Create(t.Context(), nil, sess); err != nil {
		t.Fatalf("seed session: %v", err)
	}
}

func TestAuthenticateValidToken(t *testing.T) {
	h := newHarness(t)
	h.seedAuthenticatedUser(t, 1001, 0)
	h.verifier.set(&domain.AccessClaims{UserID: 1001, SessionID: 7001, DeviceID: "d-abc", TokenVersion: 0}, nil)

	u, err := h.svc.Authenticate(t.Context(), "token", "d-abc")
	if err != nil {
		t.Fatalf("Authenticate error: %v", err)
	}
	if u.ID != 1001 {
		t.Errorf("user id = %d, want 1001", u.ID)
	}
}

func TestAuthenticateTokenExpired(t *testing.T) {
	h := newHarness(t)
	h.seedAuthenticatedUser(t, 1001, 0)
	h.verifier.set(nil, domain.ErrTokenExpired)
	_, err := h.svc.Authenticate(t.Context(), "token", "d-abc")
	if !errors.Is(err, domain.ErrTokenExpired) {
		t.Errorf("err = %v, want ErrTokenExpired", err)
	}
}

func TestAuthenticateAccountSuspended(t *testing.T) {
	h := newHarness(t)
	u := &userdomain.User{ID: 1001, DisplayName: "Aya", AccountState: userdomain.AccountSuspended, TokenVersion: 0}
	_ = h.users.Create(t.Context(), nil, u)
	h.verifier.set(&domain.AccessClaims{UserID: 1001, SessionID: 7001, DeviceID: "d-abc", TokenVersion: 0}, nil)
	_, err := h.svc.Authenticate(t.Context(), "token", "d-abc")
	if !errors.Is(err, domain.ErrAccountSuspended) {
		t.Errorf("err = %v, want ErrAccountSuspended", err)
	}
}

func TestAuthenticateAccountDeleted(t *testing.T) {
	h := newHarness(t)
	u := &userdomain.User{ID: 1001, DisplayName: "Aya", AccountState: userdomain.AccountDeleted, TokenVersion: 0}
	_ = h.users.Create(t.Context(), nil, u)
	h.verifier.set(&domain.AccessClaims{UserID: 1001, SessionID: 7001, DeviceID: "d-abc", TokenVersion: 0}, nil)
	_, err := h.svc.Authenticate(t.Context(), "token", "d-abc")
	if !errors.Is(err, domain.ErrAccountDeleted) {
		t.Errorf("err = %v, want ErrAccountDeleted", err)
	}
}

func TestAuthenticateTokenVersionStale(t *testing.T) {
	h := newHarness(t)
	h.seedAuthenticatedUser(t, 1001, 3)
	h.verifier.set(&domain.AccessClaims{UserID: 1001, SessionID: 7001, DeviceID: "d-abc", TokenVersion: 1}, nil)
	_, err := h.svc.Authenticate(t.Context(), "token", "d-abc")
	if !errors.Is(err, domain.ErrTokenRevoked) {
		t.Errorf("err = %v, want ErrTokenRevoked (SESS-6)", err)
	}
}

func TestAuthenticateSessionMissing(t *testing.T) {
	h := newHarness(t)
	u := &userdomain.User{ID: 1001, DisplayName: "Aya", AccountState: userdomain.AccountActive, TokenVersion: 0}
	_ = h.users.Create(t.Context(), nil, u)
	h.verifier.set(&domain.AccessClaims{UserID: 1001, SessionID: 7001, DeviceID: "d-abc", TokenVersion: 0}, nil)
	_, err := h.svc.Authenticate(t.Context(), "token", "d-abc")
	if !errors.Is(err, domain.ErrSessionRevoked) {
		t.Errorf("err = %v, want ErrSessionRevoked", err)
	}
}

func TestAuthenticateSessionNotActive(t *testing.T) {
	h := newHarness(t)
	h.seedAuthenticatedUser(t, 1001, 0)
	// Revoke the session, then present its (unexpired) token.
	for _, s := range h.sess.sessions {
		if s.ID == 7001 {
			s.State = domain.SessionRevoked
		}
	}
	h.verifier.set(&domain.AccessClaims{UserID: 1001, SessionID: 7001, DeviceID: "d-abc", TokenVersion: 0}, nil)
	_, err := h.svc.Authenticate(t.Context(), "token", "d-abc")
	if !errors.Is(err, domain.ErrSessionRevoked) {
		t.Errorf("err = %v, want ErrSessionRevoked", err)
	}
}

func TestAuthenticateUnknownUserMapsToRevoked(t *testing.T) {
	h := newHarness(t)
	h.verifier.set(&domain.AccessClaims{UserID: 9999, SessionID: 7001, DeviceID: "d-abc", TokenVersion: 0}, nil)
	_, err := h.svc.Authenticate(t.Context(), "token", "d-abc")
	if !errors.Is(err, domain.ErrTokenRevoked) {
		t.Errorf("err = %v, want ErrTokenRevoked (no enumeration)", err)
	}
}

func TestAuthenticateUnwrapsClassifiedVerifyError(t *testing.T) {
	h := newHarness(t)
	// A wrapped classification must still match (the verifier wraps sentinels).
	h.verifier.set(nil, wrapErr(domain.ErrTokenInvalid))
	_, err := h.svc.Authenticate(t.Context(), "token", "d-abc")
	if !errors.Is(err, domain.ErrTokenInvalid) {
		t.Errorf("err = %v, want ErrTokenInvalid", err)
	}
}

func wrapErr(err error) error { return &wrapError{err: err} }

type wrapError struct{ err error }

func (w *wrapError) Error() string { return "wrapped: " + w.err.Error() }
func (w *wrapError) Unwrap() error { return w.err }
