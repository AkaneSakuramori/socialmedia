package application

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/internal/auth/domain"
	userdomain "github.com/AkaneSakuramori/socialmedia/server/internal/user/domain"
)

// seedPair registers a password user and returns its user, session, and token
// pair so tests can drive the refresh flow.
func seedPair(t *testing.T, h *harness) (userdomain.User, domain.Session, domain.TokenPair) {
	t.Helper()
	res, err := h.svc.Register(t.Context(), baseCmd())
	if err != nil {
		t.Fatalf("seed Register error: %v", err)
	}
	return res.User, res.Session, res.TokenPair
}

func refreshCmd(token string) RefreshCommand {
	return RefreshCommand{RefreshToken: token}
}

func TestRefreshRotatesTokenPair(t *testing.T) {
	h := newHarness(t)
	_, sess, pair := seedPair(t, h)

	res, err := h.svc.Refresh(t.Context(), refreshCmd(pair.RefreshToken))
	if err != nil {
		t.Fatalf("Refresh error: %v", err)
	}
	if res.AccessToken == "" || res.RefreshToken == "" {
		t.Error("must issue a fresh token pair")
	}
	if res.SessionID != sess.ID {
		t.Errorf("session id = %d, want %d", res.SessionID, sess.ID)
	}
	if res.ExpiresIn <= 0 {
		t.Errorf("expires_in = %d, want > 0", res.ExpiresIn)
	}
	if res.RefreshToken == pair.RefreshToken {
		t.Error("refresh token must rotate (REFR-4 single-use)")
	}

	stored := h.sess.sessions[0]
	if stored.RefreshTokenHash != domain.HashOpaqueToken(res.RefreshToken) {
		t.Error("session must store the rotated refresh token hash")
	}
	if stored.RefreshTokenPreviousHash != domain.HashOpaqueToken(pair.RefreshToken) {
		t.Error("old token must be retained as the previous hash (reuse detection)")
	}
	if stored.RefreshTokenFamily != 1 {
		t.Errorf("family = %d, want 1 after first rotation", stored.RefreshTokenFamily)
	}
	if !hasAction(h.audit, "auth.token_refresh") {
		t.Errorf("audit = %v, want auth.token_refresh", h.audit.actions())
	}
	if tx := h.begin.lastTx(); !tx.committed || tx.rolledBack {
		t.Errorf("rotation tx: committed=%v rolledBack=%v, want committed only", tx.committed, tx.rolledBack)
	}
}

func TestRefreshChainsRotations(t *testing.T) {
	h := newHarness(t)
	_, _, pair := seedPair(t, h)

	r1, err := h.svc.Refresh(t.Context(), refreshCmd(pair.RefreshToken))
	if err != nil {
		t.Fatalf("first Refresh error: %v", err)
	}
	r2, err := h.svc.Refresh(t.Context(), refreshCmd(r1.RefreshToken))
	if err != nil {
		t.Fatalf("second Refresh error: %v", err)
	}
	if r2.RefreshToken == r1.RefreshToken {
		t.Error("each refresh must issue a distinct refresh token")
	}
	stored := h.sess.sessions[0]
	if stored.RefreshTokenFamily != 2 {
		t.Errorf("family = %d, want 2 after two rotations", stored.RefreshTokenFamily)
	}
	if stored.RefreshTokenPreviousHash != domain.HashOpaqueToken(r1.RefreshToken) {
		t.Error("previous hash must track the last-rotated-out token")
	}
}

func TestRefreshReuseRevokesAllSessions(t *testing.T) {
	h := newHarness(t)
	_, _, pair := seedPair(t, h)
	// Second device, so the user has two sessions that must both be revoked.
	cmd := LoginCommand{
		IdentifierType: domain.IdentifierPhone,
		Identifier:     "+15550123",
		Method:         domain.LoginMethodPassword,
		Password:       "correct horse 42",
		Device:         domain.DeviceInfo{DeviceID: "d-other"},
	}
	if _, err := h.svc.Login(t.Context(), cmd); err != nil {
		t.Fatalf("seed Login error: %v", err)
	}

	r1, err := h.svc.Refresh(t.Context(), refreshCmd(pair.RefreshToken))
	if err != nil {
		t.Fatalf("first Refresh error: %v", err)
	}
	if len(h.sess.sessions) != 2 {
		t.Fatalf("sessions = %d, want 2", len(h.sess.sessions))
	}

	// Presenting the now-rotated-out token is the theft signal (REFR-5).
	_, err = h.svc.Refresh(t.Context(), refreshCmd(pair.RefreshToken))
	if !errors.Is(err, domain.ErrRefreshTokenReuse) {
		t.Fatalf("error = %v, want ErrRefreshTokenReuse", err)
	}
	for _, s := range h.sess.sessions {
		if s.State != domain.SessionRevoked {
			t.Errorf("session %d state = %s, want revoked (REFR-5 revokes all)", s.ID, s.State)
		}
	}
	if !hasAction(h.audit, "auth.token_reuse") {
		t.Errorf("audit = %v, want auth.token_reuse", h.audit.actions())
	}
	if tx := h.begin.lastTx(); !tx.committed {
		t.Error("revocation must be committed")
	}
	_ = r1
}

func TestRefreshRejectsMalformedToken(t *testing.T) {
	h := newHarness(t)
	_, _, _ = seedPair(t, h)
	_, err := h.svc.Refresh(t.Context(), refreshCmd("not-an-opaque-token"))
	if !errors.Is(err, domain.ErrRefreshTokenInvalid) {
		t.Fatalf("error = %v, want ErrRefreshTokenInvalid", err)
	}
	if len(h.sess.sessions) != 1 || h.sess.sessions[0].State != domain.SessionActive {
		t.Error("malformed input must not touch sessions")
	}
}

func TestRefreshRejectsUnknownToken(t *testing.T) {
	h := newHarness(t)
	_, _, _ = seedPair(t, h)
	unknown := strings.Repeat("A", domain.OpaqueTokenLen) // well-formed, never issued
	_, err := h.svc.Refresh(t.Context(), refreshCmd(unknown))
	if !errors.Is(err, domain.ErrRefreshTokenInvalid) {
		t.Fatalf("error = %v, want ErrRefreshTokenInvalid", err)
	}
	if tx := h.begin.lastTx(); tx.committed {
		t.Error("invalid-token path must not commit a transaction")
	}
}

func TestRefreshRejectsExpiredToken(t *testing.T) {
	h := newHarness(t)
	_, _, pair := seedPair(t, h)
	h.sess.sessions[0].RefreshExpiresAt = time.Now().Add(-time.Minute)

	_, err := h.svc.Refresh(t.Context(), refreshCmd(pair.RefreshToken))
	if !errors.Is(err, domain.ErrRefreshTokenInvalid) {
		t.Fatalf("error = %v, want ErrRefreshTokenInvalid (REFR-6 idle expiry)", err)
	}
}

func TestRefreshRejectsRevokedSession(t *testing.T) {
	h := newHarness(t)
	_, _, pair := seedPair(t, h)
	h.sess.sessions[0].State = domain.SessionRevoked

	_, err := h.svc.Refresh(t.Context(), refreshCmd(pair.RefreshToken))
	if !errors.Is(err, domain.ErrRefreshTokenInvalid) {
		t.Fatalf("error = %v, want ErrRefreshTokenInvalid", err)
	}
}

func TestRefreshRejectsSuspendedUser(t *testing.T) {
	h := newHarness(t)
	user, _, pair := seedPair(t, h)
	stored, _ := h.users.FindByID(t.Context(), user.ID)
	stored.AccountState = userdomain.AccountSuspended

	_, err := h.svc.Refresh(t.Context(), refreshCmd(pair.RefreshToken))
	if !errors.Is(err, domain.ErrAccountSuspended) {
		t.Fatalf("error = %v, want ErrAccountSuspended (AUTH-8)", err)
	}
}

func TestRefreshRejectsDeletedUser(t *testing.T) {
	h := newHarness(t)
	user, _, pair := seedPair(t, h)
	stored, _ := h.users.FindByID(t.Context(), user.ID)
	stored.AccountState = userdomain.AccountDeleted

	_, err := h.svc.Refresh(t.Context(), refreshCmd(pair.RefreshToken))
	if !errors.Is(err, domain.ErrRefreshTokenInvalid) {
		t.Fatalf("error = %v, want ErrRefreshTokenInvalid (no state leak)", err)
	}
}

func TestRefreshConcurrentSingleWinner(t *testing.T) {
	h := newHarness(t)
	_, _, pair := seedPair(t, h)

	const n = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	success, reuse := 0, 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := h.svc.Refresh(t.Context(), refreshCmd(pair.RefreshToken))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				success++
			case errors.Is(err, domain.ErrRefreshTokenReuse):
				reuse++
			default:
				t.Errorf("unexpected refresh error: %v", err)
			}
		}()
	}
	wg.Wait()

	// Exactly one request may win the race; every loser is treated as reuse.
	if success != 1 {
		t.Errorf("successes = %d, want exactly 1", success)
	}
	if reuse != n-1 {
		t.Errorf("reuse results = %d, want %d", reuse, n-1)
	}
	for _, s := range h.sess.sessions {
		if s.State != domain.SessionRevoked {
			t.Errorf("session %d state = %s, want revoked after the race", s.ID, s.State)
		}
	}
}

func TestRefreshRollsBackOnInfraFailure(t *testing.T) {
	h := newHarness(t)
	_, _, pair := seedPair(t, h)
	h.tokens.err = errors.New("signing key unavailable")

	_, err := h.svc.Refresh(t.Context(), refreshCmd(pair.RefreshToken))
	if err == nil {
		t.Fatal("Refresh expected token-issuance failure")
	}
	if tx := h.begin.lastTx(); tx.committed || !tx.rolledBack {
		t.Errorf("tx: committed=%v rolledBack=%v, want rolled back", tx.committed, tx.rolledBack)
	}
}

var _ = context.Background
