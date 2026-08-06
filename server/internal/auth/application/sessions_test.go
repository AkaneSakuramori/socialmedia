package application

import (
	"errors"
	"testing"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/internal/auth/domain"
	userdomain "github.com/AkaneSakuramori/socialmedia/server/internal/user/domain"
)

// seedSecondDevice logs the seeded user in from another device so the user
// owns multiple sessions.
func seedSecondDevice(t *testing.T, h *harness, deviceID string) {
	t.Helper()
	cmd := LoginCommand{
		IdentifierType: domain.IdentifierPhone,
		Identifier:     "+15550123",
		Method:         domain.LoginMethodPassword,
		Password:       "correct horse 42",
		Device:         domain.DeviceInfo{DeviceID: deviceID},
	}
	if _, err := h.svc.Login(t.Context(), cmd); err != nil {
		t.Fatalf("seed second device: %v", err)
	}
}

func activeIDs(t *testing.T, h *harness, userID int64) []int64 {
	t.Helper()
	list, err := h.svc.ListSessions(t.Context(), ListSessionsCommand{UserID: userID, CurrentSessionID: 0})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	ids := make([]int64, 0, len(list))
	for _, s := range list {
		ids = append(ids, s.ID)
	}
	return ids
}

func TestListSessionsReturnsActiveDevices(t *testing.T) {
	h := newHarness(t)
	user, first, _ := seedPair(t, h)
	seedSecondDevice(t, h, "d-web")

	list, err := h.svc.ListSessions(t.Context(), ListSessionsCommand{UserID: user.ID, CurrentSessionID: first.ID})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("listed %d sessions, want 2", len(list))
	}

	var cur *SessionInfo
	for i := range list {
		s := &list[i]
		if s.Current {
			cur = s
		}
		if s.DeviceID == "d-abc" {
			if s.DeviceName == nil || *s.DeviceName != "Pixel 9" {
				t.Errorf("device d-abc name = %v, want Pixel 9", s.DeviceName)
			}
			if s.Platform == nil || *s.Platform != "android" {
				t.Errorf("device d-abc platform = %v, want android", s.Platform)
			}
		}
		if s.LastActiveAt.IsZero() {
			t.Error("SessionInfo must carry last_active_at (SESS-8)")
		}
	}
	if cur == nil || cur.ID != first.ID {
		t.Fatalf("current session not flagged, list = %+v", list)
	}
	// Newest activity first: the just-logged-in second device leads.
	if list[0].DeviceID != "d-web" {
		t.Errorf("first listed = %s, want d-web (newest first)", list[0].DeviceID)
	}
}

func TestListSessionsExcludesRevoked(t *testing.T) {
	h := newHarness(t)
	user, _, _ := seedPair(t, h)
	seedSecondDevice(t, h, "d-web")
	h.sess.sessions[0].State = domain.SessionRevoked

	if ids := activeIDs(t, h, user.ID); len(ids) != 1 {
		t.Fatalf("active sessions = %v, want only the surviving device", ids)
	}
}

func TestRenameSessionUpdatesDeviceName(t *testing.T) {
	h := newHarness(t)
	user, _, _ := seedPair(t, h)

	err := h.svc.RenameSession(t.Context(), RenameSessionCommand{
		UserID: user.ID, SessionID: h.sess.sessions[0].ID, DeviceName: "Work Pixel",
	})
	if err != nil {
		t.Fatalf("RenameSession: %v", err)
	}
	if n := h.sess.sessions[0].Device.DeviceName; n == nil || *n != "Work Pixel" {
		t.Errorf("device_name = %v, want Work Pixel", n)
	}
	if !hasAction(h.audit, "auth.session_renamed") {
		t.Errorf("audit = %v, want auth.session_renamed", h.audit.actions())
	}
}

func TestRenameSessionValidatesName(t *testing.T) {
	h := newHarness(t)
	user, _, _ := seedPair(t, h)
	sid := h.sess.sessions[0].ID

	for _, name := range []string{"", "this device name is far too long to be accepted and must be rejected by validation"} {
		err := h.svc.RenameSession(t.Context(), RenameSessionCommand{UserID: user.ID, SessionID: sid, DeviceName: name})
		var ve *domain.ValidationError
		if !errors.As(err, &ve) || ve.Field != "device_name" {
			t.Errorf("Rename(%q) error = %v, want ValidationError{device_name}", name, err)
		}
	}
}

func TestRenameSessionRejectsForeignSession(t *testing.T) {
	h := newHarness(t)
	user, _, _ := seedPair(t, h)
	_, other, _ := seedOtherUser(t, h)

	err := h.svc.RenameSession(t.Context(), RenameSessionCommand{
		UserID: user.ID, SessionID: other.ID, DeviceName: "nope",
	})
	if !errors.Is(err, domain.ErrSessionNotOwned) {
		t.Fatalf("error = %v, want ErrSessionNotOwned (SESS-3)", err)
	}
	if tx := h.begin.lastTx(); tx.committed || !tx.rolledBack {
		t.Errorf("tx: committed=%v rolledBack=%v, want rolled back", tx.committed, tx.rolledBack)
	}
}

func TestRenameSessionNotFound(t *testing.T) {
	h := newHarness(t)
	user, _, _ := seedPair(t, h)

	err := h.svc.RenameSession(t.Context(), RenameSessionCommand{UserID: user.ID, SessionID: 999999, DeviceName: "x"})
	if !errors.Is(err, domain.ErrSessionNotFound) {
		t.Fatalf("error = %v, want ErrSessionNotFound", err)
	}
}

func TestRenameSessionRejectsRevokedSession(t *testing.T) {
	h := newHarness(t)
	user, _, _ := seedPair(t, h)
	h.sess.sessions[0].State = domain.SessionRevoked

	err := h.svc.RenameSession(t.Context(), RenameSessionCommand{
		UserID: user.ID, SessionID: h.sess.sessions[0].ID, DeviceName: "nope",
	})
	if !errors.Is(err, domain.ErrSessionNotFound) {
		t.Fatalf("error = %v, want ErrSessionNotFound for a revoked session", err)
	}
}

func TestLogoutRevokesCurrentSession(t *testing.T) {
	h := newHarness(t)
	user, sess, _ := seedPair(t, h)

	err := h.svc.Logout(t.Context(), LogoutCommand{UserID: user.ID, SessionID: sess.ID})
	if err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if h.sess.sessions[0].State != domain.SessionRevoked {
		t.Errorf("state = %s, want revoked (API.md §4.5)", h.sess.sessions[0].State)
	}
	// Subsequent refresh must fail: the session is gone from the active set.
	if _, err := h.svc.Refresh(t.Context(), refreshCmd(h.sess.sessions[0].RefreshTokenHash)); err == nil {
		t.Error("refresh after logout must fail")
	}
	if !hasAction(h.audit, "auth.logout") {
		t.Errorf("audit = %v, want auth.logout", h.audit.actions())
	}
}

func TestLogoutUnknownSession(t *testing.T) {
	h := newHarness(t)
	user, _, _ := seedPair(t, h)

	err := h.svc.Logout(t.Context(), LogoutCommand{UserID: user.ID, SessionID: 999999})
	if !errors.Is(err, domain.ErrSessionNotFound) {
		t.Fatalf("error = %v, want ErrSessionNotFound", err)
	}
}

func TestLogoutSessionRevokesSelectedDevice(t *testing.T) {
	h := newHarness(t)
	user, cur, _ := seedPair(t, h)
	seedSecondDevice(t, h, "d-web")
	target := h.sess.sessions[1] // d-web

	err := h.svc.LogoutSession(t.Context(), LogoutSessionCommand{UserID: user.ID, SessionID: target.ID})
	if err != nil {
		t.Fatalf("LogoutSession: %v", err)
	}
	if target.State != domain.SessionRevoked {
		t.Errorf("target state = %s, want revoked (API.md §4.8)", target.State)
	}
	if cur.State != domain.SessionActive {
		t.Errorf("current session must survive revoking another device")
	}
	if !hasAction(h.audit, "auth.session_revoked") {
		t.Errorf("audit = %v, want auth.session_revoked", h.audit.actions())
	}
}

func TestLogoutSessionRejectsForeignSession(t *testing.T) {
	h := newHarness(t)
	user, _, _ := seedPair(t, h)
	_, other, _ := seedOtherUser(t, h)

	err := h.svc.LogoutSession(t.Context(), LogoutSessionCommand{UserID: user.ID, SessionID: other.ID})
	if !errors.Is(err, domain.ErrSessionNotOwned) {
		t.Fatalf("error = %v, want ErrSessionNotOwned", err)
	}
	if other.State != domain.SessionActive {
		t.Error("a foreign session must not be revoked")
	}
}

func TestLogoutSessionNotFound(t *testing.T) {
	h := newHarness(t)
	user, _, _ := seedPair(t, h)

	err := h.svc.LogoutSession(t.Context(), LogoutSessionCommand{UserID: user.ID, SessionID: 999999})
	if !errors.Is(err, domain.ErrSessionNotFound) {
		t.Fatalf("error = %v, want ErrSessionNotFound", err)
	}
}

func TestLogoutOtherSessionsKeepsCurrent(t *testing.T) {
	h := newHarness(t)
	user, cur, _ := seedPair(t, h)
	seedSecondDevice(t, h, "d-web")
	seedSecondDevice(t, h, "d-tablet")

	err := h.svc.LogoutOtherSessions(t.Context(), LogoutOtherSessionsCommand{UserID: user.ID, SessionID: cur.ID})
	if err != nil {
		t.Fatalf("LogoutOtherSessions: %v", err)
	}
	for _, s := range h.sess.sessions {
		if s.ID == cur.ID {
			if s.State != domain.SessionActive {
				t.Errorf("current session must stay active, got %s", s.State)
			}
		} else if s.State != domain.SessionRevoked {
			t.Errorf("session %d state = %s, want revoked", s.ID, s.State)
		}
	}
	if !hasAction(h.audit, "auth.logout_others") {
		t.Errorf("audit = %v, want auth.logout_others", h.audit.actions())
	}
}

func TestLogoutAllRevokesEverythingAndBumpsVersion(t *testing.T) {
	h := newHarness(t)
	user, cur, _ := seedPair(t, h)
	seedSecondDevice(t, h, "d-web")
	if h.users.byID[user.ID].TokenVersion != 0 {
		t.Fatalf("initial token version = %d, want 0", h.users.byID[user.ID].TokenVersion)
	}

	err := h.svc.LogoutAll(t.Context(), LogoutAllCommand{UserID: user.ID})
	if err != nil {
		t.Fatalf("LogoutAll: %v", err)
	}
	if v := h.users.byID[user.ID].TokenVersion; v != 1 {
		t.Errorf("token version = %d, want 1 (SESS-6 bump)", v)
	}
	for _, s := range h.sess.sessions {
		if s.State != domain.SessionRevoked {
			t.Errorf("session %d state = %s, want revoked (includes current, API.md §4.6)", s.ID, s.State)
		}
	}
	if !hasAction(h.audit, "auth.logout_all") {
		t.Errorf("audit = %v, want auth.logout_all", h.audit.actions())
	}
	if tx := h.begin.lastTx(); !tx.committed || tx.rolledBack {
		t.Errorf("tx: committed=%v rolledBack=%v, want committed", tx.committed, tx.rolledBack)
	}

	// A fresh login must embed the bumped version in its access token.
	seedSecondDevice(t, h, "d-web")
	if got := h.tokens.lastVersion; got != 1 {
		t.Errorf("tokens issued after logout-all embed version %d, want 1", got)
	}
	_ = cur
}

func TestLogoutAllRollsBackOnRevocationFailure(t *testing.T) {
	h := newHarness(t)
	user, _, _ := seedPair(t, h)
	h.sess.revokeErr = errors.New("db down")

	err := h.svc.LogoutAll(t.Context(), LogoutAllCommand{UserID: user.ID})
	if err == nil {
		t.Fatal("LogoutAll expected revocation failure")
	}
	if tx := h.begin.lastTx(); tx.committed || !tx.rolledBack {
		t.Errorf("tx: committed=%v rolledBack=%v, want rolled back (version must not bump)", tx.committed, tx.rolledBack)
	}
	if v := h.users.byID[user.ID].TokenVersion; v != 0 {
		t.Errorf("token version = %d, want 0 — bump must not survive a failed revocation", v)
	}
}

func TestExpireIdleSessions(t *testing.T) {
	h := newHarness(t)
	_, _, _ = seedPair(t, h)
	seedSecondDevice(t, h, "d-web")

	// First device idle beyond the 30d window; second device recently active.
	h.sess.sessions[0].LastActiveAt = time.Now().Add(-31 * 24 * time.Hour)

	n, err := h.svc.ExpireIdleSessions(t.Context())
	if err != nil {
		t.Fatalf("ExpireIdleSessions: %v", err)
	}
	if n != 1 {
		t.Fatalf("expired %d sessions, want 1 (SESS-9)", n)
	}
	if h.sess.sessions[0].State != domain.SessionExpired {
		t.Errorf("idle session state = %s, want expired", h.sess.sessions[0].State)
	}
	if h.sess.sessions[1].State != domain.SessionActive {
		t.Errorf("recently active session must stay active, got %s", h.sess.sessions[1].State)
	}
	if !hasAction(h.audit, "auth.sessions_expired") {
		t.Errorf("audit = %v, want auth.sessions_expired", h.audit.actions())
	}
}

func TestExpireIdleSessionsOnRefreshWindow(t *testing.T) {
	h := newHarness(t)
	_, _, _ = seedPair(t, h)
	h.sess.sessions[0].RefreshExpiresAt = time.Now().Add(-time.Minute)

	n, err := h.svc.ExpireIdleSessions(t.Context())
	if err != nil {
		t.Fatalf("ExpireIdleSessions: %v", err)
	}
	if n != 1 || h.sess.sessions[0].State != domain.SessionExpired {
		t.Fatalf("expired=%d state=%s, want refresh-window expiry (REFR-6)", n, h.sess.sessions[0].State)
	}
}

func TestExpireIdleAuditsNothingWhenEmpty(t *testing.T) {
	h := newHarness(t)
	_, _, _ = seedPair(t, h)

	n, err := h.svc.ExpireIdleSessions(t.Context())
	if err != nil {
		t.Fatalf("ExpireIdleSessions: %v", err)
	}
	if n != 0 {
		t.Fatalf("expired = %d, want 0", n)
	}
	if hasAction(h.audit, "auth.sessions_expired") {
		t.Error("no audit noise when nothing expired")
	}
}

func TestPurgeRevokedSessions(t *testing.T) {
	h := newHarness(t)
	user, _, _ := seedPair(t, h)
	seedSecondDevice(t, h, "d-web")

	h.sess.sessions[0].State = domain.SessionRevoked
	h.sess.sessions[0].UpdatedAt = time.Now().Add(-100 * 24 * time.Hour) // beyond 90d retention

	n, err := h.svc.PurgeRevokedSessions(t.Context())
	if err != nil {
		t.Fatalf("PurgeRevokedSessions: %v", err)
	}
	if n != 1 {
		t.Fatalf("purged %d sessions, want 1 (DATABASE.md §4.4)", n)
	}
	if len(h.sess.sessions) != 1 || h.sess.sessions[0].Device.DeviceID != "d-web" {
		t.Errorf("sessions after purge = %+v, want only the active one", h.sess.sessions)
	}
	if !hasAction(h.audit, "auth.sessions_purged") {
		t.Errorf("audit = %v, want auth.sessions_purged", h.audit.actions())
	}
	_ = user
}

// seedOtherUser registers a separate account so ownership tests can cross
// users. It returns that account's first session.
func seedOtherUser(t *testing.T, h *harness) (userdomain.User, domain.Session, domain.TokenPair) {
	t.Helper()
	cmd := baseCmd()
	cmd.Identifier = "+15550999"
	cmd.OTPCode = "999999"
	cmd.Username = nil // the base command's username already belongs to user 1001
	res, err := h.svc.Register(t.Context(), cmd)
	if err != nil {
		t.Fatalf("seed other user: %v", err)
	}
	return res.User, res.Session, res.TokenPair
}
