package application

import (
	"errors"
	"testing"

	"github.com/AkaneSakuramori/socialmedia/server/internal/auth/domain"
	userdomain "github.com/AkaneSakuramori/socialmedia/server/internal/user/domain"
)

// seedPasswordUser registers a password account on device d-abc and returns
// the login command that succeeds against it.
func seedPasswordUser(t *testing.T, h *harness) LoginCommand {
	t.Helper()
	if _, err := h.svc.Register(t.Context(), baseCmd()); err != nil {
		t.Fatalf("seed Register error: %v", err)
	}
	return LoginCommand{
		IdentifierType: domain.IdentifierPhone,
		Identifier:     "+15550123",
		Method:         domain.LoginMethodPassword,
		Password:       "correct horse 42",
		Device:         domain.DeviceInfo{DeviceID: "d-abc"},
	}
}

func newDevice(cmd LoginCommand, deviceID string) LoginCommand {
	cmd.Device = domain.DeviceInfo{DeviceID: deviceID}
	return cmd
}

func TestLoginPasswordSuccess(t *testing.T) {
	h := newHarness(t)
	cmd := seedPasswordUser(t, h)

	res, err := h.svc.Login(t.Context(), cmd)
	if err != nil {
		t.Fatalf("Login error: %v", err)
	}
	if res.User.PhoneNumber == nil || *res.User.PhoneNumber != "+15550123" {
		t.Errorf("logged in wrong user: %+v", res.User)
	}
	if res.TokenPair.AccessToken == "" || res.TokenPair.RefreshToken == "" {
		t.Error("token pair must be issued")
	}
	if got := h.throttle.counts["+15550123"]; got != 0 {
		t.Errorf("failure counter = %d, want cleared after success", got)
	}
	if !hasAction(h.audit, "auth.login") {
		t.Errorf("audit events = %v, want auth.login", h.audit.actions())
	}
}

func TestLoginCreatesSessionForNewDevice(t *testing.T) {
	h := newHarness(t)
	cmd := newDevice(seedPasswordUser(t, h), "d-other")

	res, err := h.svc.Login(t.Context(), cmd)
	if err != nil {
		t.Fatalf("Login error: %v", err)
	}
	if len(h.sess.sessions) != 2 {
		t.Fatalf("sessions = %d, want 2 (register device + login device)", len(h.sess.sessions))
	}
	sess := h.sess.sessions[1]
	if sess.Device.DeviceID != "d-other" || sess.State != domain.SessionActive {
		t.Errorf("session = %+v", sess)
	}
	if sess.RefreshTokenHash != domain.HashOpaqueToken(res.TokenPair.RefreshToken) {
		t.Error("session stores the refresh token hash, not the raw token")
	}
}

func TestLoginRotatesExistingSessionForSameDevice(t *testing.T) {
	h := newHarness(t)
	cmd := seedPasswordUser(t, h)
	// Value copy: upsertSession mutates the stored row in place.
	before := *h.sess.sessions[0]

	res, err := h.svc.Login(t.Context(), cmd)
	if err != nil {
		t.Fatalf("Login error: %v", err)
	}
	if len(h.sess.sessions) != 1 {
		t.Fatalf("sessions = %d, want 1 (upsert, not duplicate)", len(h.sess.sessions))
	}
	after := h.sess.sessions[0]
	if after.ID != before.ID {
		t.Errorf("session id changed %d -> %d, want reuse of the device row", before.ID, after.ID)
	}
	if after.RefreshTokenFamily != before.RefreshTokenFamily+1 {
		t.Errorf("family = %d, want %d (rotation increments the family)", after.RefreshTokenFamily, before.RefreshTokenFamily+1)
	}
	if after.RefreshTokenHash != domain.HashOpaqueToken(res.TokenPair.RefreshToken) {
		t.Error("session must hold the rotated refresh token hash")
	}
}

func TestLoginWrongPassword(t *testing.T) {
	h := newHarness(t)
	cmd := seedPasswordUser(t, h)
	cmd.Password = "wrong-password"

	_, err := h.svc.Login(t.Context(), cmd)
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("error = %v, want ErrInvalidCredentials", err)
	}
	if got := h.throttle.counts["+15550123"]; got != 1 {
		t.Errorf("failure counter = %d, want 1", got)
	}
	if !hasAction(h.audit, "auth.login_failed") {
		t.Errorf("audit events = %v, want auth.login_failed", h.audit.actions())
	}
}

func TestLoginLockoutAfterFiveFailures(t *testing.T) {
	h := newHarness(t)
	cmd := seedPasswordUser(t, h)
	cmd.Password = "wrong-password"

	for i := 1; i <= 5; i++ {
		if _, err := h.svc.Login(t.Context(), cmd); !errors.Is(err, domain.ErrInvalidCredentials) {
			t.Fatalf("attempt %d: error = %v, want ErrInvalidCredentials", i, err)
		}
	}

	// The 6th attempt is rejected by the lockout gate before any verify.
	_, err := h.svc.Login(t.Context(), cmd)
	var locked *domain.AccountLockedError
	if !errors.As(err, &locked) || locked.Remaining <= 0 {
		t.Fatalf("error = %v, want AccountLockedError with remaining > 0", err)
	}
	if len(h.sess.sessions) != 1 {
		t.Errorf("no new session may be created while locked (sessions=%d)", len(h.sess.sessions))
	}
	if !hasAction(h.audit, "auth.login_locked") {
		t.Errorf("audit events = %v, want auth.login_locked", h.audit.actions())
	}
}

func TestLoginClearsFailuresOnSuccess(t *testing.T) {
	h := newHarness(t)
	cmd := seedPasswordUser(t, h)
	bad := cmd
	bad.Password = "wrong-password"
	for i := 0; i < 3; i++ {
		if _, err := h.svc.Login(t.Context(), bad); !errors.Is(err, domain.ErrInvalidCredentials) {
			t.Fatalf("failure attempt: %v", err)
		}
	}
	if _, err := h.svc.Login(t.Context(), cmd); err != nil {
		t.Fatalf("Login error: %v", err)
	}
	if got := h.throttle.counts["+15550123"]; got != 0 {
		t.Errorf("failure counter = %d, want cleared", got)
	}
}

func TestLoginUnknownIdentifier(t *testing.T) {
	h := newHarness(t)
	cmd := LoginCommand{
		IdentifierType: domain.IdentifierPhone,
		Identifier:     "+15559999",
		Method:         domain.LoginMethodPassword,
		Password:       "correct horse 42",
		Device:         domain.DeviceInfo{DeviceID: "d-abc"},
	}
	_, err := h.svc.Login(t.Context(), cmd)
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("error = %v, want ErrInvalidCredentials (no account enumeration)", err)
	}
	if got := h.throttle.counts["+15559999"]; got != 1 {
		t.Errorf("failure counter = %d, want 1", got)
	}
}

func TestLoginSuspendedAccount(t *testing.T) {
	h := newHarness(t)
	cmd := seedPasswordUser(t, h)
	user, _ := h.users.FindByPhone(t.Context(), "+15550123")
	user.AccountState = userdomain.AccountSuspended

	_, err := h.svc.Login(t.Context(), cmd)
	if !errors.Is(err, domain.ErrAccountSuspended) {
		t.Fatalf("error = %v, want ErrAccountSuspended (AUTH-8)", err)
	}
	if got := h.throttle.counts["+15550123"]; got != 0 {
		t.Errorf("suspension must not count as a credential failure (counter=%d)", got)
	}
	if !hasAction(h.audit, "auth.login_blocked") {
		t.Errorf("audit events = %v, want auth.login_blocked", h.audit.actions())
	}
}

func TestLoginDeletedAccountIsNotEnumerated(t *testing.T) {
	h := newHarness(t)
	cmd := seedPasswordUser(t, h)
	user, _ := h.users.FindByPhone(t.Context(), "+15550123")
	user.AccountState = userdomain.AccountDeleted

	_, err := h.svc.Login(t.Context(), cmd)
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("error = %v, want ErrInvalidCredentials for deleted accounts", err)
	}
	if got := h.throttle.counts["+15550123"]; got != 1 {
		t.Errorf("failure counter = %d, want 1", got)
	}
}

func TestLoginOTPSuccess(t *testing.T) {
	h := newHarness(t)
	// OTP-only account: register without a password.
	cmd := baseCmd()
	cmd.Password = nil
	if _, err := h.svc.Register(t.Context(), cmd); err != nil {
		t.Fatalf("seed Register error: %v", err)
	}
	login := LoginCommand{
		IdentifierType: domain.IdentifierPhone,
		Identifier:     "+15550123",
		Method:         domain.LoginMethodOTP,
		OTPCode:        "482913",
		Device:         domain.DeviceInfo{DeviceID: "d-otp"},
	}
	res, err := h.svc.Login(t.Context(), login)
	if err != nil {
		t.Fatalf("Login error: %v", err)
	}
	if res.TokenPair.AccessToken == "" {
		t.Error("otp login must issue a token pair")
	}
}

func TestLoginOTPWrong(t *testing.T) {
	h := newHarness(t)
	cmd := baseCmd()
	cmd.Password = nil
	if _, err := h.svc.Register(t.Context(), cmd); err != nil {
		t.Fatalf("seed Register error: %v", err)
	}
	login := LoginCommand{
		IdentifierType: domain.IdentifierPhone,
		Identifier:     "+15550123",
		Method:         domain.LoginMethodOTP,
		OTPCode:        "000000",
		Device:         domain.DeviceInfo{DeviceID: "d-otp"},
	}
	_, err := h.svc.Login(t.Context(), login)
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("error = %v, want ErrInvalidCredentials", err)
	}
	if got := h.throttle.counts["+15550123"]; got != 1 {
		t.Errorf("otp failure must share the lockout counter (got %d)", got)
	}
}

func TestLoginPasskeyUnsupported(t *testing.T) {
	h := newHarness(t)
	cmd := seedPasswordUser(t, h)
	cmd.Method = domain.LoginMethodPasskey
	_, err := h.svc.Login(t.Context(), cmd)
	if !errors.Is(err, domain.ErrUnsupportedLoginMethod) {
		t.Fatalf("error = %v, want ErrUnsupportedLoginMethod", err)
	}
}

func TestLoginUnknownMethod(t *testing.T) {
	h := newHarness(t)
	cmd := seedPasswordUser(t, h)
	cmd.Method = "magic"
	_, err := h.svc.Login(t.Context(), cmd)
	var ve *domain.ValidationError
	if !errors.As(err, &ve) || ve.Field != "method" {
		t.Fatalf("error = %v, want ValidationError{method}", err)
	}
}

func TestLoginRequiresValidDeviceID(t *testing.T) {
	h := newHarness(t)
	cmd := newDevice(seedPasswordUser(t, h), "")
	_, err := h.svc.Login(t.Context(), cmd)
	var ve *domain.ValidationError
	if !errors.As(err, &ve) || ve.Field != "device_id" {
		t.Fatalf("error = %v, want ValidationError{device_id}", err)
	}
}

func TestLoginWithoutPasswordCredential(t *testing.T) {
	h := newHarness(t)
	cmd := baseCmd()
	cmd.Password = nil
	if _, err := h.svc.Register(t.Context(), cmd); err != nil {
		t.Fatalf("seed Register error: %v", err)
	}
	login := LoginCommand{
		IdentifierType: domain.IdentifierPhone,
		Identifier:     "+15550123",
		Method:         domain.LoginMethodPassword,
		Password:       "correct horse 42",
		Device:         domain.DeviceInfo{DeviceID: "d-otp"},
	}
	_, err := h.svc.Login(t.Context(), login)
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("error = %v, want ErrInvalidCredentials when no password credential exists", err)
	}
	if got := h.throttle.counts["+15550123"]; got != 1 {
		t.Errorf("failure counter = %d, want 1", got)
	}
}

func TestLoginRollsBackWhenSessionUpdateFails(t *testing.T) {
	h := newHarness(t)
	cmd := seedPasswordUser(t, h)
	h.sess.updateErr = errors.New("constraint violation")
	_, err := h.svc.Login(t.Context(), cmd)
	if err == nil {
		t.Fatal("Login expected session-update failure")
	}
	if !h.begin.lastTx().rolledBack {
		t.Error("transaction must be rolled back when the session step fails")
	}
	if h.begin.lastTx().committed {
		t.Error("transaction must not commit on failure")
	}
}

func TestLoginPropagatesThrottleFailure(t *testing.T) {
	h := newHarness(t)
	h.throttle.err = errors.New("redis down")
	cmd := seedPasswordUser(t, h)
	if _, err := h.svc.Login(t.Context(), cmd); err == nil {
		t.Fatal("Login expected throttle failure to propagate")
	}
}

func hasAction(a *fakeAudit, action string) bool {
	for _, got := range a.actions() {
		if got == action {
			return true
		}
	}
	return false
}
