package application

import (
	"errors"
	"testing"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/internal/auth/domain"
	userdomain "github.com/AkaneSakuramori/socialmedia/server/internal/user/domain"
)

// ---- password reset ----

func TestRequestPasswordResetKnownUser(t *testing.T) {
	h := newHarness(t)
	seedPasswordUser(t, h)

	res, err := h.svc.RequestPasswordReset(t.Context(), RequestPasswordResetCommand{
		IdentifierType: domain.IdentifierPhone,
		Identifier:     "+15550123",
	})
	if err != nil {
		t.Fatalf("RequestPasswordReset error: %v", err)
	}
	if res.Token == "" {
		t.Fatal("reset token must be issued for a known identifier")
	}
	if res.ExpiresIn != 30*60 {
		t.Errorf("ExpiresIn = %d, want %d", res.ExpiresIn, 30*60)
	}
	// only the hash is stored, never the plaintext (REFR-2 pattern)
	if got := h.authToks.tokensByUser(1001); len(got) != 1 {
		t.Fatalf("stored %d tokens, want 1", len(got))
	}
	if got := h.authToks.tokensByUser(1001)[0].TokenHash; got != domain.HashOpaqueToken(res.Token) {
		t.Error("token must be stored as a hash, not plaintext")
	}
	if !hasAction(h.audit, "auth.password_reset_requested") {
		t.Errorf("audit events = %v, want auth.password_reset_requested", h.audit.actions())
	}
}

func TestRequestPasswordResetUnknownIdentifierIsUniform(t *testing.T) {
	h := newHarness(t)

	res, err := h.svc.RequestPasswordReset(t.Context(), RequestPasswordResetCommand{
		IdentifierType: domain.IdentifierPhone,
		Identifier:     "+15559999", // never registered
	})
	if err != nil {
		t.Fatalf("RequestPasswordReset error: %v", err)
	}
	// Same shape as the known-identifier success: no enumeration (OWASP A07).
	if res.Token != "" {
		t.Error("no token may be issued for an unknown identifier")
	}
	if res.ExpiresIn != 30*60 {
		t.Errorf("ExpiresIn = %d, want %d (uniform)", res.ExpiresIn, 30*60)
	}
	if got := h.authToks.tokensByUser(1001); len(got) != 0 {
		t.Error("no token may be stored for an unknown identifier")
	}
	if !hasAction(h.audit, "auth.password_reset_requested") {
		t.Errorf("audit events = %v, want auth.password_reset_requested", h.audit.actions())
	}
}

func TestRequestPasswordResetDeletedAccountIsUniform(t *testing.T) {
	h := newHarness(t)
	seedPasswordUser(t, h)
	if err := h.svc.DeleteAccount(t.Context(), DeleteAccountCommand{
		UserID:    1001,
		SessionID: 1002,
		Reauth:    Reauth{Method: domain.LoginMethodPassword, Password: "correct horse 42"},
	}); err != nil {
		t.Fatalf("seed DeleteAccount error: %v", err)
	}

	res, err := h.svc.RequestPasswordReset(t.Context(), RequestPasswordResetCommand{
		IdentifierType: domain.IdentifierPhone,
		Identifier:     "+15550123",
	})
	if err != nil {
		t.Fatalf("RequestPasswordReset error: %v", err)
	}
	if res.Token != "" || res.ExpiresIn != 30*60 {
		t.Errorf("uniform response expected, got token=%q expiresIn=%d", res.Token, res.ExpiresIn)
	}
}

func TestResetPasswordSuccess(t *testing.T) {
	h := newHarness(t)
	seedPasswordUser(t, h)
	tok := requestReset(t, h)

	// The register session is active before the reset.
	if h.sess.sessions[0].State != domain.SessionActive {
		t.Fatalf("precondition: register session active, got %s", h.sess.sessions[0].State)
	}
	before := h.users.byID[1001].TokenVersion

	if err := h.svc.ResetPassword(t.Context(), ResetPasswordCommand{
		Token:       tok,
		NewPassword: "new horse 99",
	}); err != nil {
		t.Fatalf("ResetPassword error: %v", err)
	}

	// REC-4 / SESS-4: every session suspended, token version bumped.
	if h.sess.sessions[0].State != domain.SessionSuspended {
		t.Errorf("session state = %s, want suspended", h.sess.sessions[0].State)
	}
	if got := h.users.byID[1001].TokenVersion; got != before+1 {
		t.Errorf("token version = %d, want %d", got, before+1)
	}
	// The new password works, the old one does not.
	if _, err := h.svc.Login(t.Context(), LoginCommand{
		IdentifierType: domain.IdentifierPhone,
		Identifier:     "+15550123",
		Method:         domain.LoginMethodPassword,
		Password:       "new horse 99",
		Device:         domain.DeviceInfo{DeviceID: "d-new"},
	}); err != nil {
		t.Fatalf("login with new password failed: %v", err)
	}
	_, err := h.svc.Login(t.Context(), LoginCommand{
		IdentifierType: domain.IdentifierPhone,
		Identifier:     "+15550123",
		Method:         domain.LoginMethodPassword,
		Password:       "correct horse 42",
		Device:         domain.DeviceInfo{DeviceID: "d-old"},
	})
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("old password login error = %v, want ErrInvalidCredentials", err)
	}
	if !hasAction(h.audit, "auth.password_reset") {
		t.Errorf("audit events = %v, want auth.password_reset", h.audit.actions())
	}
	if got := h.notifier.events(); len(got) != 2 || got[1] != "password_reset_completed" {
		t.Errorf("notifications = %v, want [password_reset password_reset_completed]", got)
	}
}

func TestResetPasswordTokenSingleUse(t *testing.T) {
	h := newHarness(t)
	seedPasswordUser(t, h)
	tok := requestReset(t, h)

	if err := h.svc.ResetPassword(t.Context(), ResetPasswordCommand{Token: tok, NewPassword: "new horse 99"}); err != nil {
		t.Fatalf("first ResetPassword error: %v", err)
	}
	err := h.svc.ResetPassword(t.Context(), ResetPasswordCommand{Token: tok, NewPassword: "another pass 1"})
	if !errors.Is(err, domain.ErrRecoveryTokenInvalid) {
		t.Fatalf("second ResetPassword error = %v, want ErrRecoveryTokenInvalid", err)
	}
}

func TestResetPasswordExpiredToken(t *testing.T) {
	h := newHarness(t)
	seedPasswordUser(t, h)

	// Seed an expired-but-wellformed token directly into the store.
	raw, err := domain.GenerateOpaqueToken()
	if err != nil {
		t.Fatal(err)
	}
	expired := time.Now().Add(-time.Minute)
	if err := h.authToks.Create(t.Context(), nil, &domain.AuthToken{
		UserID:    1001,
		Purpose:   domain.PurposePasswordReset,
		TokenHash: domain.HashOpaqueToken(raw),
		ExpiresAt: expired,
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	err = h.svc.ResetPassword(t.Context(), ResetPasswordCommand{Token: raw, NewPassword: "new horse 99"})
	if !errors.Is(err, domain.ErrRecoveryTokenInvalid) {
		t.Fatalf("error = %v, want ErrRecoveryTokenInvalid (expired token)", err)
	}
}

func TestResetPasswordRejectsWrongPurpose(t *testing.T) {
	h := newHarness(t)
	seedPasswordUser(t, h)
	tok := requestEmailChange(t, h) // an email_change token

	err := h.svc.ResetPassword(t.Context(), ResetPasswordCommand{Token: tok, NewPassword: "new horse 99"})
	if !errors.Is(err, domain.ErrRecoveryTokenInvalid) {
		t.Fatalf("error = %v, want ErrRecoveryTokenInvalid (wrong purpose)", err)
	}
}

func TestResetPasswordRejectsMalformedToken(t *testing.T) {
	h := newHarness(t)
	seedPasswordUser(t, h)
	err := h.svc.ResetPassword(t.Context(), ResetPasswordCommand{Token: "short", NewPassword: "new horse 99"})
	if !errors.Is(err, domain.ErrRecoveryTokenInvalid) {
		t.Fatalf("error = %v, want ErrRecoveryTokenInvalid (malformed)", err)
	}
}

// ---- password change ----

func TestChangePasswordSuccess(t *testing.T) {
	h := newHarness(t)
	seedPasswordUser(t, h)
	// A second session on another device must be suspended; the caller's kept.
	if _, err := h.svc.Login(t.Context(), LoginCommand{
		IdentifierType: domain.IdentifierPhone,
		Identifier:     "+15550123",
		Method:         domain.LoginMethodPassword,
		Password:       "correct horse 42",
		Device:         domain.DeviceInfo{DeviceID: "d-other"},
	}); err != nil {
		t.Fatalf("seed second session error: %v", err)
	}
	callerID := h.sess.sessions[0].ID
	otherID := h.sess.sessions[1].ID

	if err := h.svc.ChangePassword(t.Context(), ChangePasswordCommand{
		UserID:      1001,
		SessionID:   callerID,
		Reauth:      Reauth{Method: domain.LoginMethodPassword, Password: "correct horse 42"},
		NewPassword: "new horse 99",
	}); err != nil {
		t.Fatalf("ChangePassword error: %v", err)
	}

	// PASS-4: current session survives, others suspended.
	if s := sessionByID(h.sess.sessions, callerID); s.State != domain.SessionActive {
		t.Errorf("caller session state = %s, want active", s.State)
	}
	if s := sessionByID(h.sess.sessions, otherID); s.State != domain.SessionSuspended {
		t.Errorf("other session state = %s, want suspended", s.State)
	}
	if !hasAction(h.audit, "auth.password_changed") {
		t.Errorf("audit events = %v, want auth.password_changed", h.audit.actions())
	}
	if got := h.notifier.events(); len(got) != 1 || got[0] != "password_changed" {
		t.Errorf("notifications = %v, want [password_changed]", got)
	}
}

func TestChangePasswordRequiresStepUp(t *testing.T) {
	h := newHarness(t)
	seedPasswordUser(t, h)

	err := h.svc.ChangePassword(t.Context(), ChangePasswordCommand{
		UserID:      1001,
		SessionID:   1002,
		Reauth:      Reauth{Method: domain.LoginMethodPassword, Password: "wrong-password"},
		NewPassword: "new horse 99",
	})
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("error = %v, want ErrInvalidCredentials (AUTH-9 step-up)", err)
	}
	// The password must be unchanged.
	if _, err := h.svc.Login(t.Context(), LoginCommand{
		IdentifierType: domain.IdentifierPhone,
		Identifier:     "+15550123",
		Method:         domain.LoginMethodPassword,
		Password:       "correct horse 42",
		Device:         domain.DeviceInfo{DeviceID: "d-other"},
	}); err != nil {
		t.Fatalf("original password must still work after failed step-up: %v", err)
	}
}

func TestChangePasswordRejectsWeakPassword(t *testing.T) {
	h := newHarness(t)
	seedPasswordUser(t, h)
	err := h.svc.ChangePassword(t.Context(), ChangePasswordCommand{
		UserID:      1001,
		SessionID:   1002,
		Reauth:      Reauth{Method: domain.LoginMethodPassword, Password: "correct horse 42"},
		NewPassword: "short1",
	})
	var ve *domain.ValidationError
	if !errors.As(err, &ve) || ve.Field != "password" {
		t.Fatalf("error = %v, want ValidationError{password}", err)
	}
}

// ---- email change ----

func TestEmailChangeRoundTrip(t *testing.T) {
	h := newHarness(t)
	seedPasswordUser(t, h)

	tok := requestEmailChange(t, h)

	if err := h.svc.ConfirmEmailChange(t.Context(), ConfirmEmailChangeCommand{Token: tok}); err != nil {
		t.Fatalf("ConfirmEmailChange error: %v", err)
	}
	if h.users.byID[1001].Email == nil || *h.users.byID[1001].Email != "new@example.com" {
		t.Errorf("email = %v, want new@example.com", h.users.byID[1001].Email)
	}
	if !hasAction(h.audit, "auth.email_changed") {
		t.Errorf("audit events = %v, want auth.email_changed", h.audit.actions())
	}
	if got := h.notifier.events(); len(got) != 1 || got[0] != "identifier_changed" {
		t.Errorf("notifications = %v, want [identifier_changed]", got)
	}
}

func TestEmailChangeRejectsUnchanged(t *testing.T) {
	h := newHarness(t)
	seedPasswordUser(t, h)
	// The seeded account has phone primary; give it its own email first.
	if err := h.svc.ConfirmEmailChange(t.Context(), ConfirmEmailChangeCommand{Token: requestEmailChangeTo(t, h, "aya@example.com")}); err != nil {
		t.Fatalf("seed email error: %v", err)
	}
	_, err := h.svc.RequestEmailChange(t.Context(), RequestEmailChangeCommand{
		UserID:    1001,
		SessionID: 1002,
		NewEmail:  "aya@example.com", // same as current
		Reauth:    Reauth{Method: domain.LoginMethodPassword, Password: "correct horse 42"},
	})
	var ve *domain.ValidationError
	if !errors.As(err, &ve) || ve.Field != "email" {
		t.Fatalf("error = %v, want ValidationError{email:unchanged}", err)
	}
}

func TestEmailChangeRejectsTaken(t *testing.T) {
	h := newHarness(t)
	seedPasswordUser(t, h)
	// Another account already holds the target email.
	if _, err := h.svc.Register(t.Context(), emailUserCmd()); err != nil {
		t.Fatalf("seed second account error: %v", err)
	}
	_, err := h.svc.RequestEmailChange(t.Context(), RequestEmailChangeCommand{
		UserID:    1001,
		SessionID: 1002,
		NewEmail:  "aya@example.com",
		Reauth:    Reauth{Method: domain.LoginMethodPassword, Password: "correct horse 42"},
	})
	if !errors.Is(err, userdomain.ErrIdentifierTaken) {
		t.Fatalf("error = %v, want ErrIdentifierTaken", err)
	}
}

func TestEmailChangeTokenSingleUse(t *testing.T) {
	h := newHarness(t)
	seedPasswordUser(t, h)
	tok := requestEmailChange(t, h)

	if err := h.svc.ConfirmEmailChange(t.Context(), ConfirmEmailChangeCommand{Token: tok}); err != nil {
		t.Fatalf("first ConfirmEmailChange error: %v", err)
	}
	err := h.svc.ConfirmEmailChange(t.Context(), ConfirmEmailChangeCommand{Token: tok})
	if !errors.Is(err, domain.ErrRecoveryTokenInvalid) {
		t.Fatalf("second confirm error = %v, want ErrRecoveryTokenInvalid", err)
	}
}

func TestEmailChangeRejectsWrongPurpose(t *testing.T) {
	h := newHarness(t)
	seedPasswordUser(t, h)
	tok := requestReset(t, h) // a password_reset token

	err := h.svc.ConfirmEmailChange(t.Context(), ConfirmEmailChangeCommand{Token: tok})
	if !errors.Is(err, domain.ErrRecoveryTokenInvalid) {
		t.Fatalf("error = %v, want ErrRecoveryTokenInvalid (wrong purpose)", err)
	}
}

// ---- phone change ----

func TestPhoneChangeRoundTrip(t *testing.T) {
	h := newHarness(t)
	seedPasswordUser(t, h)

	_, err := h.svc.RequestPhoneChange(t.Context(), RequestPhoneChangeCommand{
		UserID:    1001,
		SessionID: 1002,
		NewPhone:  "+15559000",
		Reauth:    Reauth{Method: domain.LoginMethodPassword, Password: "correct horse 42"},
	})
	if err != nil {
		t.Fatalf("RequestPhoneChange error: %v", err)
	}
	toks := h.authToks.tokensByUser(1001)
	if len(toks) != 1 || toks[0].Purpose != domain.PurposePhoneChange {
		t.Fatalf("stored tokens = %+v, want one phone_change token", toks)
	}
	raw, err := domain.GenerateOpaqueToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := h.authToks.Create(t.Context(), nil, &domain.AuthToken{
		UserID:    1001,
		Purpose:   domain.PurposePhoneChange,
		TokenHash: domain.HashOpaqueToken(raw),
		Data:      []byte(`{"phone":"+15559000"}`),
		ExpiresAt: time.Now().Add(15 * time.Minute),
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	if err := h.svc.ConfirmPhoneChange(t.Context(), ConfirmPhoneChangeCommand{Token: raw}); err != nil {
		t.Fatalf("ConfirmPhoneChange error: %v", err)
	}
	if h.users.byID[1001].PhoneNumber == nil || *h.users.byID[1001].PhoneNumber != "+15559000" {
		t.Errorf("phone = %v, want +15559000", h.users.byID[1001].PhoneNumber)
	}
	if !hasAction(h.audit, "auth.phone_changed") {
		t.Errorf("audit events = %v, want auth.phone_changed", h.audit.actions())
	}
}

func TestPhoneChangeRejectsTaken(t *testing.T) {
	h := newHarness(t)
	seedPasswordUser(t, h)
	if _, err := h.svc.Register(t.Context(), phoneUserCmd()); err != nil {
		t.Fatalf("seed second account error: %v", err)
	}
	_, err := h.svc.RequestPhoneChange(t.Context(), RequestPhoneChangeCommand{
		UserID:    1001,
		SessionID: 1002,
		NewPhone:  "+15550999", // held by the second account
		Reauth:    Reauth{Method: domain.LoginMethodPassword, Password: "correct horse 42"},
	})
	if !errors.Is(err, userdomain.ErrIdentifierTaken) {
		t.Fatalf("error = %v, want ErrIdentifierTaken", err)
	}
}

// ---- account deletion / restoration ----

func TestDeleteAccount(t *testing.T) {
	h := newHarness(t)
	seedPasswordUser(t, h)
	before := h.users.byID[1001].TokenVersion

	if err := h.svc.DeleteAccount(t.Context(), DeleteAccountCommand{
		UserID:    1001,
		SessionID: 1002,
		Reauth:    Reauth{Method: domain.LoginMethodPassword, Password: "correct horse 42"},
	}); err != nil {
		t.Fatalf("DeleteAccount error: %v", err)
	}

	// Soft delete: account hidden from login, still recoverable.
	if got := h.users.byID[1001].AccountState; got != userdomain.AccountDeleted {
		t.Errorf("account state = %s, want deleted", got)
	}
	if h.sess.sessions[0].State != domain.SessionRevoked {
		t.Errorf("session state = %s, want revoked", h.sess.sessions[0].State)
	}
	if got := h.users.byID[1001].TokenVersion; got != before+1 {
		t.Errorf("token version = %d, want %d", got, before+1)
	}
	_, err := h.svc.Login(t.Context(), LoginCommand{
		IdentifierType: domain.IdentifierPhone,
		Identifier:     "+15550123",
		Method:         domain.LoginMethodPassword,
		Password:       "correct horse 42",
		Device:         domain.DeviceInfo{DeviceID: "d-new"},
	})
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("deleted account login error = %v, want ErrInvalidCredentials", err)
	}
	if !hasAction(h.audit, "account.deleted") {
		t.Errorf("audit events = %v, want account.deleted", h.audit.actions())
	}
}

func TestDeleteAccountAlreadyDeleted(t *testing.T) {
	h := newHarness(t)
	seedPasswordUser(t, h)
	cmd := DeleteAccountCommand{
		UserID:    1001,
		SessionID: 1002,
		Reauth:    Reauth{Method: domain.LoginMethodPassword, Password: "correct horse 42"},
	}
	if err := h.svc.DeleteAccount(t.Context(), cmd); err != nil {
		t.Fatalf("first DeleteAccount error: %v", err)
	}
	err := h.svc.DeleteAccount(t.Context(), cmd)
	if !errors.Is(err, domain.ErrAccountAlreadyDeleted) {
		t.Fatalf("second DeleteAccount error = %v, want ErrAccountAlreadyDeleted", err)
	}
}

func TestDeleteAccountRequiresStepUp(t *testing.T) {
	h := newHarness(t)
	seedPasswordUser(t, h)
	err := h.svc.DeleteAccount(t.Context(), DeleteAccountCommand{
		UserID:    1001,
		SessionID: 1002,
		Reauth:    Reauth{Method: domain.LoginMethodPassword, Password: "wrong-password"},
	})
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("error = %v, want ErrInvalidCredentials (AUTH-9)", err)
	}
	if got := h.users.byID[1001].AccountState; got != userdomain.AccountActive {
		t.Errorf("account must not be deleted after failed step-up, state = %s", got)
	}
}

func TestRestoreAccountWithinGrace(t *testing.T) {
	h := newHarness(t)
	seedPasswordUser(t, h)
	if err := h.svc.DeleteAccount(t.Context(), DeleteAccountCommand{
		UserID:    1001,
		SessionID: 1002,
		Reauth:    Reauth{Method: domain.LoginMethodPassword, Password: "correct horse 42"},
	}); err != nil {
		t.Fatalf("seed DeleteAccount error: %v", err)
	}

	// REC-1: identifier verification via OTP gates the restore.
	if err := h.svc.RestoreAccount(t.Context(), RestoreAccountCommand{
		IdentifierType: domain.IdentifierPhone,
		Identifier:     "+15550123",
		OTPCode:        "482913",
	}); err != nil {
		t.Fatalf("RestoreAccount error: %v", err)
	}
	if got := h.users.byID[1001].AccountState; got != userdomain.AccountActive {
		t.Errorf("account state = %s, want active", got)
	}
	if !hasAction(h.audit, "account.restored") {
		t.Errorf("audit events = %v, want account.restored", h.audit.actions())
	}
	// The account can log in again.
	if _, err := h.svc.Login(t.Context(), LoginCommand{
		IdentifierType: domain.IdentifierPhone,
		Identifier:     "+15550123",
		Method:         domain.LoginMethodPassword,
		Password:       "correct horse 42",
		Device:         domain.DeviceInfo{DeviceID: "d-restored"},
	}); err != nil {
		t.Fatalf("login after restore failed: %v", err)
	}
}

func TestRestoreAccountWrongOTP(t *testing.T) {
	h := newHarness(t)
	seedPasswordUser(t, h)
	if err := h.svc.DeleteAccount(t.Context(), DeleteAccountCommand{
		UserID:    1001,
		SessionID: 1002,
		Reauth:    Reauth{Method: domain.LoginMethodPassword, Password: "correct horse 42"},
	}); err != nil {
		t.Fatalf("seed DeleteAccount error: %v", err)
	}
	err := h.svc.RestoreAccount(t.Context(), RestoreAccountCommand{
		IdentifierType: domain.IdentifierPhone,
		Identifier:     "+15550123",
		OTPCode:        "000000",
	})
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("error = %v, want ErrInvalidCredentials", err)
	}
}

func TestRestoreAccountGraceExpired(t *testing.T) {
	h := newHarness(t)
	seedPasswordUser(t, h)
	// Simulate a deletion 31 days ago, beyond the 30-day grace period.
	if err := h.users.MarkDeleted(t.Context(), nil, 1001, time.Now().Add(-31*24*time.Hour)); err != nil {
		t.Fatalf("seed MarkDeleted error: %v", err)
	}
	err := h.svc.RestoreAccount(t.Context(), RestoreAccountCommand{
		IdentifierType: domain.IdentifierPhone,
		Identifier:     "+15550123",
		OTPCode:        "482913",
	})
	if !errors.Is(err, userdomain.ErrAccountRestoreExpired) {
		t.Fatalf("error = %v, want ErrAccountRestoreExpired", err)
	}
}

func TestPurgeDeletedAccounts(t *testing.T) {
	h := newHarness(t)
	seedPasswordUser(t, h)
	// One account within grace (kept), one past grace (purged).
	second, err := h.svc.Register(t.Context(), emailUserCmd())
	if err != nil {
		t.Fatalf("seed second account error: %v", err)
	}
	now := time.Now()
	if err := h.users.MarkDeleted(t.Context(), nil, 1001, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := h.users.MarkDeleted(t.Context(), nil, second.User.ID, now.Add(-31*24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	n, err := h.svc.PurgeDeletedAccounts(t.Context())
	if err != nil {
		t.Fatalf("PurgeDeletedAccounts error: %v", err)
	}
	if n != 1 {
		t.Errorf("purged %d, want 1", n)
	}
	if _, ok := h.users.byID[second.User.ID]; ok {
		t.Error("account past grace must be hard-deleted")
	}
	if _, ok := h.users.byID[1001]; !ok {
		t.Error("account within grace must be kept")
	}
}

// ---- login history & risk ----

func TestLoginRecordsHistory(t *testing.T) {
	h := newHarness(t)
	seedPasswordUser(t, h)

	if _, err := h.svc.Login(t.Context(), LoginCommand{
		IdentifierType: domain.IdentifierPhone,
		Identifier:     "+15550123",
		Method:         domain.LoginMethodPassword,
		Password:       "correct horse 42",
		Device:         domain.DeviceInfo{DeviceID: "d-other"},
	}); err != nil {
		t.Fatalf("Login error: %v", err)
	}
	// A failure against a wrong password also records, with the user id.
	_, err := h.svc.Login(t.Context(), LoginCommand{
		IdentifierType: domain.IdentifierPhone,
		Identifier:     "+15550123",
		Method:         domain.LoginMethodPassword,
		Password:       "wrong-password",
		Device:         domain.DeviceInfo{DeviceID: "d-other"},
	})
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("failed login error = %v, want ErrInvalidCredentials", err)
	}

	events := h.hist.all()
	if len(events) != 2 {
		t.Fatalf("recorded %d events, want 2", len(events))
	}
	if !events[0].Success || !events[0].NewDevice {
		t.Errorf("first event = %+v, want success on new device", events[0])
	}
	if events[1].Success {
		t.Errorf("second event = %+v, want failure", events[1])
	}
	if got := *events[1].UserID; got != 1001 {
		t.Errorf("failure event user id = %d, want 1001", got)
	}
}

func TestLoginUnknownIdentifierRecordsHistoryWithoutUser(t *testing.T) {
	h := newHarness(t)
	_, err := h.svc.Login(t.Context(), LoginCommand{
		IdentifierType: domain.IdentifierPhone,
		Identifier:     "+15559999",
		Method:         domain.LoginMethodPassword,
		Password:       "whatever-1",
		Device:         domain.DeviceInfo{DeviceID: "d-x"},
	})
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("error = %v, want ErrInvalidCredentials", err)
	}
	events := h.hist.all()
	if len(events) != 1 || events[0].UserID != nil || events[0].Success {
		t.Fatalf("events = %+v, want one failure with nil user id", events)
	}
}

func TestListLoginHistoryOwnedByUser(t *testing.T) {
	h := newHarness(t)
	seedPasswordUser(t, h)
	if _, err := h.svc.Login(t.Context(), LoginCommand{
		IdentifierType: domain.IdentifierPhone,
		Identifier:     "+15550123",
		Method:         domain.LoginMethodPassword,
		Password:       "correct horse 42",
		Device:         domain.DeviceInfo{DeviceID: "d-other"},
	}); err != nil {
		t.Fatalf("Login error: %v", err)
	}

	events, err := h.svc.ListLoginHistory(t.Context(), ListLoginHistoryCommand{UserID: 1001, Limit: 10})
	if err != nil {
		t.Fatalf("ListLoginHistory error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1 (the login; register records no login event)", len(events))
	}
	if !events[0].Success {
		t.Error("the recorded login must be a success")
	}
}

func TestRiskStepUpBlocksSession(t *testing.T) {
	h := newHarness(t)
	seedPasswordUser(t, h)
	h.risk.decision = domain.RiskDecision{StepUp: true}

	_, err := h.svc.Login(t.Context(), LoginCommand{
		IdentifierType: domain.IdentifierPhone,
		Identifier:     "+15550123",
		Method:         domain.LoginMethodPassword,
		Password:       "correct horse 42",
		Device:         domain.DeviceInfo{DeviceID: "d-suspicious"},
	})
	if !errors.Is(err, domain.ErrStepUpRequired) {
		t.Fatalf("error = %v, want ErrStepUpRequired (AUTH-11)", err)
	}
	// No new session and no token pair; the login is recorded for review.
	if len(h.sess.sessions) != 1 {
		t.Errorf("sessions = %d, want 1 (step-up does not create a session)", len(h.sess.sessions))
	}
	events := h.hist.all()
	if len(events) != 1 || !events[0].Success {
		t.Fatalf("events = %+v, want a successful authentication attempt recorded", events)
	}
	if !hasAction(h.audit, "auth.login_step_up") {
		t.Errorf("audit events = %v, want auth.login_step_up", h.audit.actions())
	}
	if h.risk.lastContext.NewDevice != true {
		t.Errorf("risk context NewDevice = %v, want true", h.risk.lastContext.NewDevice)
	}
}

func TestRiskNotifyOnNewDevice(t *testing.T) {
	h := newHarness(t)
	seedPasswordUser(t, h)
	h.risk.decision = domain.RiskDecision{Notify: true}

	if _, err := h.svc.Login(t.Context(), LoginCommand{
		IdentifierType: domain.IdentifierPhone,
		Identifier:     "+15550123",
		Method:         domain.LoginMethodPassword,
		Password:       "correct horse 42",
		Device:         domain.DeviceInfo{DeviceID: "d-other"},
	}); err != nil {
		t.Fatalf("Login error: %v", err)
	}
	if got := h.notifier.events(); len(got) != 1 || got[0] != "login_new_device" {
		t.Errorf("notifications = %v, want [login_new_device]", got)
	}
}

// ---- helpers ----

// requestReset requests a password-reset token for the seeded phone account.
func requestReset(t *testing.T, h *harness) string {
	t.Helper()
	res, err := h.svc.RequestPasswordReset(t.Context(), RequestPasswordResetCommand{
		IdentifierType: domain.IdentifierPhone,
		Identifier:     "+15550123",
	})
	if err != nil {
		t.Fatalf("RequestPasswordReset error: %v", err)
	}
	if res.Token == "" {
		t.Fatal("no reset token issued")
	}
	return res.Token
}

// requestEmailChange requests an email change to new@example.com for user 1001.
func requestEmailChange(t *testing.T, h *harness) string {
	t.Helper()
	return requestEmailChangeTo(t, h, "new@example.com")
}

func requestEmailChangeTo(t *testing.T, h *harness, email string) string {
	t.Helper()
	res, err := h.svc.RequestEmailChange(t.Context(), RequestEmailChangeCommand{
		UserID:    1001,
		SessionID: 1002,
		NewEmail:  email,
		Reauth:    Reauth{Method: domain.LoginMethodPassword, Password: "correct horse 42"},
	})
	if err != nil {
		t.Fatalf("RequestEmailChange error: %v", err)
	}
	if res.Token == "" {
		t.Fatal("no email-change token issued")
	}
	return res.Token
}

// emailUserCmd registers a second account whose email is aya@example.com.
func emailUserCmd() RegisterCommand {
	un := "bob.k"
	return RegisterCommand{
		IdentifierType: domain.IdentifierEmail,
		Identifier:     "aya@example.com",
		OTPCode:        "123456",
		DisplayName:    "Bob",
		Username:       &un,
		Password:       strPtr("bob password 1"),
		Device:         domain.DeviceInfo{DeviceID: "d-bob"},
	}
}

// phoneUserCmd registers a second account whose phone is +15550999.
func phoneUserCmd() RegisterCommand {
	un := "carl.l"
	return RegisterCommand{
		IdentifierType: domain.IdentifierPhone,
		Identifier:     "+15550999",
		OTPCode:        "999999",
		DisplayName:    "Carl",
		Username:       &un,
		Password:       strPtr("carl password 1"),
		Device:         domain.DeviceInfo{DeviceID: "d-carl"},
	}
}

func sessionByID(sessions []*domain.Session, id int64) *domain.Session {
	for _, s := range sessions {
		if s.ID == id {
			return s
		}
	}
	return nil
}
