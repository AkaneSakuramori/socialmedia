package application

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/internal/auth/domain"
	userdomain "github.com/AkaneSakuramori/socialmedia/server/internal/user/domain"
	"github.com/AkaneSakuramori/socialmedia/server/pkg/clock"
	"github.com/AkaneSakuramori/socialmedia/server/pkg/tx"
)

// ---- fakes (ENGINEERING.md §35: prefer real in-memory fakes over mocks) ----

type fakeTx struct {
	committed  bool
	rolledBack bool
	mu         sync.Mutex
}

func (f *fakeTx) Commit(context.Context) error {
	f.mu.Lock()
	f.committed = true
	f.mu.Unlock()
	return nil
}
func (f *fakeTx) Rollback(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Like a real DB transaction, rollback after commit is a no-op.
	if !f.committed {
		f.rolledBack = true
	}
	return nil
}

type fakeBeginner struct {
	mu   sync.Mutex
	last *fakeTx
}

func (b *fakeBeginner) Begin(context.Context) (tx.Tx, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	t := &fakeTx{}
	b.last = t
	return t, nil
}

// lastTx returns the most recent transaction begun by this beginner (a fresh
// fakeTx per call, like a real database session).
func (b *fakeBeginner) lastTx() *fakeTx {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.last
}

type fakeUserRepo struct {
	byPhone    map[string]*userdomain.User
	byEmail    map[string]*userdomain.User
	byUsername map[string]*userdomain.User
	byID       map[int64]*userdomain.User
	createErr  error
}

func newFakeUserRepo() *fakeUserRepo {
	return &fakeUserRepo{
		byPhone:    map[string]*userdomain.User{},
		byEmail:    map[string]*userdomain.User{},
		byUsername: map[string]*userdomain.User{},
		byID:       map[int64]*userdomain.User{},
	}
}

func (r *fakeUserRepo) Create(_ context.Context, _ tx.Tx, u *userdomain.User) error {
	if r.createErr != nil {
		return r.createErr
	}
	r.byID[u.ID] = u
	if u.PhoneNumber != nil {
		r.byPhone[*u.PhoneNumber] = u
	}
	if u.Email != nil {
		r.byEmail[*u.Email] = u
	}
	if u.Username != nil {
		r.byUsername[*u.Username] = u
	}
	return nil
}
func (r *fakeUserRepo) FindByID(_ context.Context, id int64) (*userdomain.User, error) {
	if u, ok := r.byID[id]; ok {
		return u, nil
	}
	return nil, userdomain.ErrUserNotFound
}
func (r *fakeUserRepo) FindByPhone(_ context.Context, p string) (*userdomain.User, error) {
	if u, ok := r.byPhone[p]; ok {
		return u, nil
	}
	return nil, userdomain.ErrUserNotFound
}
func (r *fakeUserRepo) FindByEmail(_ context.Context, e string) (*userdomain.User, error) {
	if u, ok := r.byEmail[e]; ok {
		return u, nil
	}
	return nil, userdomain.ErrUserNotFound
}
func (r *fakeUserRepo) PhoneTaken(_ context.Context, p string) (bool, error) {
	_, ok := r.byPhone[p]
	return ok, nil
}
func (r *fakeUserRepo) EmailTaken(_ context.Context, e string) (bool, error) {
	_, ok := r.byEmail[e]
	return ok, nil
}
func (r *fakeUserRepo) UsernameTaken(_ context.Context, u string) (bool, error) {
	_, ok := r.byUsername[u]
	return ok, nil
}

type fakeCredRepo struct {
	creds []*domain.Credential
	mu    sync.Mutex
}

func (r *fakeCredRepo) Create(_ context.Context, _ tx.Tx, c *domain.Credential) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.creds = append(r.creds, c)
	return nil
}
func (r *fakeCredRepo) FindPassword(_ context.Context, userID int64) (*domain.Credential, error) {
	for _, c := range r.creds {
		if c.UserID == userID && c.Method == domain.MethodPassword {
			return c, nil
		}
	}
	return nil, userdomain.ErrUserNotFound
}

type fakeSessionRepo struct {
	sessions  []*domain.Session
	updateErr error
	mu        sync.Mutex
}

func (r *fakeSessionRepo) Create(_ context.Context, _ tx.Tx, s *domain.Session) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions = append(r.sessions, s)
	return nil
}

func (r *fakeSessionRepo) FindByDeviceID(_ context.Context, userID int64, deviceID string) (*domain.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.sessions {
		if s.UserID == userID && s.Device.DeviceID == deviceID {
			return s, nil
		}
	}
	return nil, domain.ErrSessionNotFound
}

func (r *fakeSessionRepo) Update(_ context.Context, _ tx.Tx, s *domain.Session) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.updateErr != nil {
		return r.updateErr
	}
	for i, old := range r.sessions {
		if old.ID == s.ID {
			r.sessions[i] = s
			return nil
		}
	}
	return domain.ErrSessionNotFound
}

type fakeHasher struct{}

func (fakeHasher) Hash(_ context.Context, plaintext string) (domain.PasswordHash, error) {
	return domain.NewPasswordHash("$argon2id$fake:" + plaintext), nil
}
func (fakeHasher) Verify(_ context.Context, h domain.PasswordHash, plaintext string) (bool, error) {
	return h.String() == "$argon2id$fake:"+plaintext, nil
}

type fakeTokenIssuer struct {
	err   error
	nonce int
}

func (f *fakeTokenIssuer) IssuePair(_ context.Context, sessionID, userID int64, deviceID string, now time.Time) (domain.TokenPair, error) {
	if f.err != nil {
		return domain.TokenPair{}, f.err
	}
	f.nonce++
	return domain.TokenPair{
		AccessToken:      fmt.Sprintf("access.%d.%d", sessionID, f.nonce),
		RefreshToken:     fmt.Sprintf("rt-%d-%d", f.nonce, sessionID),
		JTI:              fmt.Sprintf("jti-%d", f.nonce),
		AccessExpiresAt:  now.Add(15 * time.Minute),
		RefreshExpiresAt: now.Add(30 * 24 * time.Hour),
	}, nil
}

type fakeOTP struct {
	valid map[string]string // identifier value -> code
	err   error
}

func (f *fakeOTP) Verify(_ context.Context, ident domain.Identifier, code string) error {
	if f.err != nil {
		return f.err
	}
	if want, ok := f.valid[ident.Value]; ok && want == code {
		return nil
	}
	return domain.ErrOTPInvalid
}

type fakeIDGen struct {
	next     int64
	failFrom int64
}

func (g *fakeIDGen) NextID() (int64, error) {
	if g.failFrom != 0 && g.next >= g.failFrom {
		return 0, errors.New("idgen: exhausted")
	}
	g.next++
	return g.next, nil
}

type fakeThrottle struct {
	counts map[string]int
	policy domain.LoginPolicy
	mu     sync.Mutex
	err    error
}

func (t *fakeThrottle) Failures(_ context.Context, identifier string) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.counts[identifier], t.err
}

func (t *fakeThrottle) RecordFailure(_ context.Context, identifier string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.counts[identifier]++
	return t.err
}

func (t *fakeThrottle) Clear(_ context.Context, identifier string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.counts, identifier)
	return t.err
}

func (t *fakeThrottle) LockoutRemaining(_ context.Context, identifier string) (time.Duration, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.counts[identifier] >= t.policy.MaxFailures {
		return t.policy.LockoutDuration, t.err
	}
	return 0, t.err
}

type fakeAudit struct {
	events []domain.AuditEvent
	mu     sync.Mutex
}

func (a *fakeAudit) Log(_ context.Context, e domain.AuditEvent) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
	return nil
}

func (a *fakeAudit) actions() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for _, e := range a.events {
		out = append(out, e.Action)
	}
	return out
}

// ---- test harness ----

type harness struct {
	svc      Service
	users    *fakeUserRepo
	creds    *fakeCredRepo
	sess     *fakeSessionRepo
	otp      *fakeOTP
	begin    *fakeBeginner
	ids      *fakeIDGen
	tokens   *fakeTokenIssuer
	throttle *fakeThrottle
	audit    *fakeAudit
	clk      clock.Clock
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		users:    newFakeUserRepo(),
		creds:    &fakeCredRepo{},
		sess:     &fakeSessionRepo{},
		otp:      &fakeOTP{valid: map[string]string{"+15550123": "482913", "+15550999": "999999", "aya@example.com": "123456"}},
		ids:      &fakeIDGen{next: 1000},
		clk:      clock.System(),
		tokens:   &fakeTokenIssuer{},
		throttle: &fakeThrottle{counts: map[string]int{}, policy: domain.DefaultLoginPolicy()},
		audit:    &fakeAudit{},
	}
	h.begin = &fakeBeginner{}
	h.svc = New(Deps{
		Users:       h.users,
		Credentials: h.creds,
		Sessions:    h.sess,
		Hasher:      fakeHasher{},
		Tokens:      h.tokens,
		OTP:         h.otp,
		Throttle:    h.throttle,
		Policy:      domain.DefaultLoginPolicy(),
		Audit:       h.audit,
		IDs:         h.ids,
		TxBeginner:  h.begin,
		Clock:       h.clk,
	})
	return h
}

func baseCmd() RegisterCommand {
	un := "aya.s"
	pw := "correct horse 42"
	return RegisterCommand{
		IdentifierType: domain.IdentifierPhone,
		Identifier:     "+15550123",
		OTPCode:        "482913",
		DisplayName:    "Aya",
		Username:       &un,
		Password:       &pw,
		Device:         domain.DeviceInfo{DeviceID: "d-abc", DeviceName: strPtr("Pixel 9"), Platform: strPtr("android")},
	}
}

func strPtr(s string) *string { return &s }

// ---- tests ----

func TestRegisterCreatesUserCredentialAndSession(t *testing.T) {
	h := newHarness(t)
	res, err := h.svc.Register(t.Context(), baseCmd())
	if err != nil {
		t.Fatalf("Register error: %v", err)
	}

	if res.User.ID != 1001 {
		t.Errorf("user id = %d, want first generated id", res.User.ID)
	}
	if res.User.DisplayName != "Aya" || res.User.Username == nil || *res.User.Username != "aya.s" {
		t.Errorf("user = %+v, want display Aya username aya.s", res.User)
	}
	if res.User.PhoneNumber == nil || *res.User.PhoneNumber != "+15550123" {
		t.Errorf("phone = %v, want +15550123", res.User.PhoneNumber)
	}
	if res.User.PrimaryIdentifier != userdomain.PrimaryPhone {
		t.Errorf("primary identifier = %q, want phone", res.User.PrimaryIdentifier)
	}

	if got := len(h.creds.creds); got != 1 {
		t.Fatalf("created %d credentials, want 1", got)
	}
	cred := h.creds.creds[0]
	if cred.Method != domain.MethodPassword || cred.UserID != res.User.ID {
		t.Errorf("credential = %+v", cred)
	}

	if got := len(h.sess.sessions); got != 1 {
		t.Fatalf("created %d sessions, want 1", got)
	}
	sess := h.sess.sessions[0]
	if sess.RefreshTokenHash != domain.HashOpaqueToken(res.TokenPair.RefreshToken) {
		t.Error("session stores the refresh token hash, not the raw token")
	}
	if sess.State != domain.SessionActive || sess.Device.DeviceID != "d-abc" {
		t.Errorf("session = %+v", sess)
	}
	if res.TokenPair.AccessToken == "" || res.TokenPair.RefreshToken == "" {
		t.Error("token pair must be issued")
	}

	if !h.begin.lastTx().committed {
		t.Error("transaction was not committed")
	}
	if h.begin.lastTx().rolledBack {
		t.Error("transaction was rolled back after success")
	}
}

func TestRegisterNormalizesEmail(t *testing.T) {
	h := newHarness(t)
	cmd := baseCmd()
	cmd.IdentifierType = domain.IdentifierEmail
	cmd.Identifier = "  AYA@Example.COM "
	cmd.OTPCode = "123456"

	res, err := h.svc.Register(t.Context(), cmd)
	if err != nil {
		t.Fatalf("Register error: %v", err)
	}
	if res.User.Email == nil || *res.User.Email != "aya@example.com" {
		t.Errorf("email = %v, want normalized aya@example.com", res.User.Email)
	}
	if res.User.PrimaryIdentifier != userdomain.PrimaryEmail {
		t.Errorf("primary identifier = %q, want email", res.User.PrimaryIdentifier)
	}
}

func TestRegisterWithoutPasswordCreatesNoCredential(t *testing.T) {
	h := newHarness(t)
	cmd := baseCmd()
	cmd.Password = nil

	res, err := h.svc.Register(t.Context(), cmd)
	if err != nil {
		t.Fatalf("Register error: %v", err)
	}
	if len(h.creds.creds) != 0 {
		t.Errorf("expected no credentials for OTP-only account, got %d", len(h.creds.creds))
	}
	if res.TokenPair.AccessToken == "" {
		t.Error("token pair still required for the first session")
	}
}

func TestRegisterRejectsWeakPassword(t *testing.T) {
	h := newHarness(t)
	cmd := baseCmd()
	pw := "short1"
	cmd.Password = &pw

	_, err := h.svc.Register(t.Context(), cmd)
	if err == nil {
		t.Fatal("Register expected weak-password error")
	}
	var ve *domain.ValidationError
	if !errors.As(err, &ve) || ve.Field != "password" {
		t.Fatalf("error = %v, want ValidationError{password}", err)
	}
	if len(h.sess.sessions) != 0 {
		t.Error("no session may be created on validation failure")
	}
}

func TestRegisterRejectsInvalidIdentifier(t *testing.T) {
	h := newHarness(t)
	cmd := baseCmd()
	cmd.Identifier = "not-a-phone"
	_, err := h.svc.Register(t.Context(), cmd)
	var ve *domain.ValidationError
	if !errors.As(err, &ve) || ve.Field != "identifier" {
		t.Fatalf("error = %v, want ValidationError{identifier}", err)
	}
}

func TestRegisterRejectsInvalidUsername(t *testing.T) {
	h := newHarness(t)
	cmd := baseCmd()
	bad := "admin"
	cmd.Username = &bad
	_, err := h.svc.Register(t.Context(), cmd)
	var ve *userdomain.ValidationError
	if !errors.As(err, &ve) || ve.Field != "username" || ve.Reason != "reserved" {
		t.Fatalf("error = %v, want reserved username", err)
	}
}

func TestRegisterRejectsDuplicateIdentifier(t *testing.T) {
	h := newHarness(t)
	if _, err := h.svc.Register(t.Context(), baseCmd()); err != nil {
		t.Fatalf("first Register error: %v", err)
	}
	_, err := h.svc.Register(t.Context(), baseCmd())
	if !errors.Is(err, userdomain.ErrIdentifierTaken) {
		t.Fatalf("second Register error = %v, want ErrIdentifierTaken", err)
	}
}

func TestRegisterRejectsDuplicateUsername(t *testing.T) {
	h := newHarness(t)
	if _, err := h.svc.Register(t.Context(), baseCmd()); err != nil {
		t.Fatalf("first Register error: %v", err)
	}
	cmd := baseCmd()
	cmd.Identifier = "+15550999"
	cmd.OTPCode = "999999"
	_, err := h.svc.Register(t.Context(), cmd)
	if !errors.Is(err, userdomain.ErrUsernameTaken) {
		t.Fatalf("error = %v, want ErrUsernameTaken", err)
	}
}

func TestRegisterRejectsInvalidOTP(t *testing.T) {
	h := newHarness(t)
	cmd := baseCmd()
	cmd.OTPCode = "000000"
	_, err := h.svc.Register(t.Context(), cmd)
	if !errors.Is(err, domain.ErrOTPInvalid) {
		t.Fatalf("error = %v, want ErrOTPInvalid", err)
	}
}

func TestRegisterOTPStoreFailureIsPropagated(t *testing.T) {
	h := newHarness(t)
	h.otp.err = errors.New("redis down")
	_, err := h.svc.Register(t.Context(), baseCmd())
	if err == nil {
		t.Fatal("Register expected otp store failure to propagate")
	}
}

func TestRegisterRollsBackWhenUserCreateFails(t *testing.T) {
	h := newHarness(t)
	h.users.createErr = errors.New("constraint violation")
	_, err := h.svc.Register(t.Context(), baseCmd())
	if err == nil {
		t.Fatal("Register expected user-create failure")
	}
	if !h.begin.lastTx().rolledBack {
		t.Error("transaction must be rolled back when an inner step fails")
	}
	if h.begin.lastTx().committed {
		t.Error("transaction must not commit on failure")
	}
}

func TestRegisterRollsBackWhenTokenIssuanceFails(t *testing.T) {
	h := newHarness(t)
	h.tokens.err = errors.New("signing key unavailable")
	_, err := h.svc.Register(t.Context(), baseCmd())
	if err == nil {
		t.Fatal("Register expected token-issuance failure")
	}
	if !h.begin.lastTx().rolledBack {
		t.Error("transaction must be rolled back on token-issuance failure")
	}
	if h.begin.lastTx().committed {
		t.Error("transaction must not commit on failure")
	}
}
