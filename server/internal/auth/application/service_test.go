package application

import (
	"context"
	"encoding/json"
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
	onCommit   []func()
}

func (f *fakeTx) Commit(context.Context) error {
	f.mu.Lock()
	f.committed = true
	hooks := f.onCommit
	f.mu.Unlock()
	for _, fn := range hooks {
		fn()
	}
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
func (f *fakeTx) Exec(context.Context, string, ...any) (int64, error) { return 0, nil }
func (f *fakeTx) QueryRow(context.Context, string, ...any) tx.Row     { return nil }
func (f *fakeTx) Query(context.Context, string, ...any) (tx.Rows, error) {
	return nil, nil
}

// OnCommit registers a hook run when the transaction commits — lets fakes
// model real database atomicity (a change only becomes visible on commit).
func (f *fakeTx) OnCommit(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onCommit = append(f.onCommit, fn)
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
	// deletedAt records the soft-delete timestamp per id (the domain User has
	// no DeletedAt field; the real repo keeps it in SQL only).
	deletedAt map[int64]time.Time
	createErr error
}

func newFakeUserRepo() *fakeUserRepo {
	return &fakeUserRepo{
		byPhone:    map[string]*userdomain.User{},
		byEmail:    map[string]*userdomain.User{},
		byUsername: map[string]*userdomain.User{},
		byID:       map[int64]*userdomain.User{},
		deletedAt:  map[int64]time.Time{},
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
func (r *fakeUserRepo) ListByIDs(_ context.Context, _ tx.Querier, ids []int64) ([]userdomain.User, error) {
	var out []userdomain.User
	for _, id := range ids {
		if u, ok := r.byID[id]; ok && u.AccountState != userdomain.AccountDeleted {
			out = append(out, *u)
		}
	}
	return out, nil
}
func (r *fakeUserRepo) FindByPhone(_ context.Context, p string) (*userdomain.User, error) {
	if u, ok := r.byPhone[p]; ok && u.AccountState != userdomain.AccountDeleted {
		return u, nil
	}
	return nil, userdomain.ErrUserNotFound
}
func (r *fakeUserRepo) FindByEmail(_ context.Context, e string) (*userdomain.User, error) {
	if u, ok := r.byEmail[e]; ok && u.AccountState != userdomain.AccountDeleted {
		return u, nil
	}
	return nil, userdomain.ErrUserNotFound
}
func (r *fakeUserRepo) PhoneTaken(_ context.Context, p string) (bool, error) {
	u, ok := r.byPhone[p]
	return ok && u.AccountState != userdomain.AccountDeleted, nil
}
func (r *fakeUserRepo) EmailTaken(_ context.Context, e string) (bool, error) {
	u, ok := r.byEmail[e]
	return ok && u.AccountState != userdomain.AccountDeleted, nil
}
func (r *fakeUserRepo) UsernameTaken(_ context.Context, u string) (bool, error) {
	uu, ok := r.byUsername[u]
	return ok && uu.AccountState != userdomain.AccountDeleted, nil
}

// SetEmail assigns a verified email, returning ErrIdentifierTaken when another
// non-deleted account already holds it (the unique index is the arbiter).
func (r *fakeUserRepo) SetEmail(_ context.Context, _ tx.Tx, userID int64, email string) error {
	u, ok := r.byID[userID]
	if !ok {
		return userdomain.ErrUserNotFound
	}
	for id, other := range r.byID {
		if id != userID && other.Email != nil && *other.Email == email && other.AccountState != userdomain.AccountDeleted {
			return userdomain.ErrIdentifierTaken
		}
	}
	if u.Email != nil {
		delete(r.byEmail, *u.Email)
	}
	u.Email = &email
	r.byEmail[email] = u
	return nil
}

// SetPhone assigns a verified phone, returning ErrIdentifierTaken when claimed.
func (r *fakeUserRepo) SetPhone(_ context.Context, _ tx.Tx, userID int64, phone string) error {
	u, ok := r.byID[userID]
	if !ok {
		return userdomain.ErrUserNotFound
	}
	for id, other := range r.byID {
		if id != userID && other.PhoneNumber != nil && *other.PhoneNumber == phone && other.AccountState != userdomain.AccountDeleted {
			return userdomain.ErrIdentifierTaken
		}
	}
	if u.PhoneNumber != nil {
		delete(r.byPhone, *u.PhoneNumber)
	}
	u.PhoneNumber = &phone
	r.byPhone[phone] = u
	return nil
}

// MarkDeleted soft-deletes the account; a second call returns
// ErrAccountAlreadyDeleted.
func (r *fakeUserRepo) MarkDeleted(_ context.Context, _ tx.Tx, userID int64, deletedAt time.Time) error {
	u, ok := r.byID[userID]
	if !ok {
		return userdomain.ErrUserNotFound
	}
	if u.AccountState == userdomain.AccountDeleted {
		return userdomain.ErrAccountAlreadyDeleted
	}
	u.AccountState = userdomain.AccountDeleted
	r.deletedAt[userID] = deletedAt
	return nil
}

// Restore reactivates a deleted account whose deleted_at is within the grace
// window (deletedAt >= graceCutoff).
func (r *fakeUserRepo) Restore(_ context.Context, _ tx.Tx, userID int64, graceCutoff time.Time) error {
	u, ok := r.byID[userID]
	if !ok {
		return userdomain.ErrUserNotFound
	}
	deletedAt, deleted := r.deletedAt[userID]
	if !deleted || deletedAt.Before(graceCutoff) {
		return userdomain.ErrAccountRestoreExpired
	}
	u.AccountState = userdomain.AccountActive
	delete(r.deletedAt, userID)
	return nil
}

// FindDeletedByPhone loads a soft-deleted account by phone (recovery lookup).
func (r *fakeUserRepo) FindDeletedByPhone(_ context.Context, phone string) (*userdomain.User, error) {
	for _, u := range r.byID {
		if u.AccountState == userdomain.AccountDeleted && u.PhoneNumber != nil && *u.PhoneNumber == phone {
			return u, nil
		}
	}
	return nil, userdomain.ErrUserNotFound
}

// FindDeletedByEmail loads a soft-deleted account by email (recovery lookup).
func (r *fakeUserRepo) FindDeletedByEmail(_ context.Context, email string) (*userdomain.User, error) {
	for _, u := range r.byID {
		if u.AccountState == userdomain.AccountDeleted && u.Email != nil && *u.Email == email {
			return u, nil
		}
	}
	return nil, userdomain.ErrUserNotFound
}

// PurgeDeleted hard-deletes accounts deleted before the cutoff.
func (r *fakeUserRepo) PurgeDeleted(_ context.Context, cutoff time.Time) (int64, error) {
	var n int64
	for id, u := range r.byID {
		deletedAt, deleted := r.deletedAt[id]
		if deleted && deletedAt.Before(cutoff) {
			delete(r.byID, id)
			delete(r.deletedAt, id)
			if u.PhoneNumber != nil {
				delete(r.byPhone, *u.PhoneNumber)
			}
			if u.Email != nil {
				delete(r.byEmail, *u.Email)
			}
			if u.Username != nil {
				delete(r.byUsername, *u.Username)
			}
			n++
		}
	}
	return n, nil
}

func (r *fakeUserRepo) BumpTokenVersion(_ context.Context, dbtx tx.Tx, userID int64) (int64, error) {
	u, ok := r.byID[userID]
	if !ok {
		return 0, userdomain.ErrUserNotFound
	}
	// Model transactional atomicity: the increment becomes visible only when
	// the transaction commits (a rolled-back bump must not survive).
	if ft, ok := dbtx.(*fakeTx); ok {
		ft.OnCommit(func() { u.TokenVersion++ })
		return u.TokenVersion + 1, nil
	}
	u.TokenVersion++
	return u.TokenVersion, nil
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

// ReplacePassword atomically replaces the user's password hash in place.
func (r *fakeCredRepo) ReplacePassword(_ context.Context, _ tx.Tx, userID int64, hash domain.PasswordHash) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.creds {
		if c.UserID == userID && c.Method == domain.MethodPassword {
			data, err := json.Marshal(domain.PasswordCredentialData{Hash: hash.String()})
			if err != nil {
				return err
			}
			c.Data = data
			return nil
		}
	}
	data, err := json.Marshal(domain.PasswordCredentialData{Hash: hash.String()})
	if err != nil {
		return err
	}
	r.creds = append(r.creds, &domain.Credential{
		UserID: userID,
		Method: domain.MethodPassword,
		Data:   data,
	})
	return nil
}

type fakeSessionRepo struct {
	sessions  []*domain.Session
	updateErr error
	revokeErr error
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
			return copySession(s), nil
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

func (r *fakeSessionRepo) FindByHash(_ context.Context, _ tx.Tx, hash string) (*domain.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.sessions {
		if s.RefreshTokenHash == hash {
			return copySession(s), nil
		}
	}
	return nil, domain.ErrSessionNotFound
}

func (r *fakeSessionRepo) FindByPreviousHash(_ context.Context, _ tx.Tx, hash string) (*domain.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.sessions {
		if s.RefreshTokenPreviousHash == hash {
			return copySession(s), nil
		}
	}
	return nil, domain.ErrSessionNotFound
}

func (r *fakeSessionRepo) Rotate(_ context.Context, _ tx.Tx, s *domain.Session, presentedHash string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Compare-and-swap, like the SQL WHERE refresh_token_hash = presentedHash:
	// a concurrent rotation already replaced the token -> no match.
	for i, old := range r.sessions {
		if old.ID == s.ID && old.RefreshTokenHash == presentedHash {
			r.sessions[i] = s
			return nil
		}
	}
	return domain.ErrSessionNotFound
}

func (r *fakeSessionRepo) RevokeAllByUserID(_ context.Context, _ tx.Tx, userID int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.revokeErr != nil {
		return r.revokeErr
	}
	for _, s := range r.sessions {
		if s.UserID == userID && s.State != domain.SessionRevoked {
			s.State = domain.SessionRevoked
		}
	}
	return nil
}

func (r *fakeSessionRepo) ListByUser(_ context.Context, userID int64) ([]domain.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.Session
	for _, s := range r.sessions {
		if s.UserID == userID && s.State == domain.SessionActive {
			out = append(out, *copySession(s))
		}
	}
	// newest activity first (like the SQL ORDER BY last_active_at DESC)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].LastActiveAt.After(out[j-1].LastActiveAt); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}

func (r *fakeSessionRepo) FindByID(_ context.Context, _ tx.Tx, sessionID int64) (*domain.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.sessions {
		if s.ID == sessionID {
			return copySession(s), nil
		}
	}
	return nil, domain.ErrSessionNotFound
}

func (r *fakeSessionRepo) RevokeByID(_ context.Context, _ tx.Tx, userID, sessionID int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.sessions {
		if s.ID == sessionID && s.UserID == userID {
			if s.State != domain.SessionRevoked {
				s.State = domain.SessionRevoked
				return nil
			}
			return domain.ErrSessionNotFound
		}
	}
	return domain.ErrSessionNotFound
}

func (r *fakeSessionRepo) RevokeOthersByUserID(_ context.Context, _ tx.Tx, userID, keepSessionID int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.sessions {
		if s.UserID == userID && s.ID != keepSessionID && s.State != domain.SessionRevoked {
			s.State = domain.SessionRevoked
		}
	}
	return nil
}

func (r *fakeSessionRepo) SuspendOthersByUserID(_ context.Context, _ tx.Tx, userID, keepSessionID int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.sessions {
		if s.UserID == userID && s.ID != keepSessionID && s.State == domain.SessionActive {
			s.State = domain.SessionSuspended
		}
	}
	return nil
}

func (r *fakeSessionRepo) SuspendAllByUserID(_ context.Context, _ tx.Tx, userID int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.sessions {
		if s.UserID == userID && s.State == domain.SessionActive {
			s.State = domain.SessionSuspended
		}
	}
	return nil
}

func (r *fakeSessionRepo) Rename(_ context.Context, _ tx.Tx, userID, sessionID int64, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.sessions {
		if s.ID == sessionID && s.UserID == userID && s.State == domain.SessionActive {
			n := name
			s.Device.DeviceName = &n
			return nil
		}
	}
	return domain.ErrSessionNotFound
}

func (r *fakeSessionRepo) ExpireIdle(_ context.Context, now time.Time, idleTimeout time.Duration) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var n int64
	cutoff := now.Add(-idleTimeout)
	for _, s := range r.sessions {
		if s.State != domain.SessionActive {
			continue
		}
		idle := !s.LastActiveAt.After(cutoff)
		expired := !s.RefreshExpiresAt.IsZero() && !s.RefreshExpiresAt.After(now)
		if idle || expired {
			s.State = domain.SessionExpired
			n++
		}
	}
	return n, nil
}

func (r *fakeSessionRepo) Purge(_ context.Context, cutoff time.Time) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var n int64
	kept := r.sessions[:0]
	for _, s := range r.sessions {
		old := s.State == domain.SessionRevoked || s.State == domain.SessionExpired
		if old && !s.UpdatedAt.After(cutoff) {
			n++
			continue
		}
		kept = append(kept, s)
	}
	r.sessions = kept
	return n, nil
}

type fakeHasher struct{}

func (fakeHasher) Hash(_ context.Context, plaintext string) (domain.PasswordHash, error) {
	return domain.NewPasswordHash("$argon2id$fake:" + plaintext), nil
}
func (fakeHasher) Verify(_ context.Context, h domain.PasswordHash, plaintext string) (bool, error) {
	return h.String() == "$argon2id$fake:"+plaintext, nil
}

type fakeTokenIssuer struct {
	mu    sync.Mutex
	err   error
	nonce int
	// lastVersion records the tokenVersion passed to the most recent
	// IssuePair, for asserting the SESS-6 version flow.
	lastVersion int64
}

// fakeTokenVerifier returns a canned claim set; tests inject the error they
// want VerifyAccess to produce (expired/invalid/revoked).
type fakeTokenVerifier struct {
	mu     sync.Mutex
	claims *domain.AccessClaims
	verErr error
}

func (f *fakeTokenVerifier) set(claims *domain.AccessClaims, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claims, f.verErr = claims, err
}

func (f *fakeTokenVerifier) VerifyAccess(_ string) (*domain.AccessClaims, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.verErr != nil {
		return nil, f.verErr
	}
	if f.claims == nil {
		return &domain.AccessClaims{UserID: 1001, SessionID: 7001, DeviceID: "d-abc", TokenVersion: 0}, nil
	}
	return f.claims, nil
}

func (f *fakeTokenIssuer) IssuePair(_ context.Context, sessionID, userID int64, deviceID string, tokenVersion int64, now time.Time) (domain.TokenPair, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return domain.TokenPair{}, f.err
	}
	f.nonce++
	f.lastVersion = tokenVersion
	// 43-char base64url-safe value (domain.OpaqueTokenLen): the refresh shape
	// gate (REFR-1) rejects anything shorter.
	return domain.TokenPair{
		AccessToken:      fmt.Sprintf("access.%d.%d", sessionID, f.nonce),
		RefreshToken:     fmt.Sprintf("rt-%040d", f.nonce),
		JTI:              fmt.Sprintf("jti-%d", f.nonce),
		AccessExpiresAt:  now.Add(15 * time.Minute),
		RefreshExpiresAt: now.Add(30 * 24 * time.Hour),
	}, nil
}

// copySession deep-copies a stored session so reads behave like a database
// scan (callers mutate their own copy; Rotate is the atomic compare-and-swap).
func copySession(s *domain.Session) *domain.Session {
	c := *s
	return &c
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

// fakeAuthTokenRepo stores single-use recovery/verification tokens by hash.
// Consume is atomic in-memory: a token is usable exactly once and TTL-bounded.
type fakeAuthTokenRepo struct {
	mu     sync.Mutex
	tokens map[string]*domain.AuthToken
	nextID int64
}

func newFakeAuthTokenRepo() *fakeAuthTokenRepo {
	return &fakeAuthTokenRepo{tokens: map[string]*domain.AuthToken{}}
}

func (r *fakeAuthTokenRepo) Create(_ context.Context, _ tx.Tx, t *domain.AuthToken) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	c := *t
	c.ID = r.nextID
	r.tokens[t.TokenHash] = &c
	return nil
}

func (r *fakeAuthTokenRepo) Consume(_ context.Context, _ tx.Tx, tokenHash string) (*domain.AuthToken, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.tokens[tokenHash]
	if !ok {
		return nil, domain.ErrRecoveryTokenInvalid
	}
	if t.UsedAt != nil || !time.Now().Before(t.ExpiresAt) {
		return nil, domain.ErrRecoveryTokenInvalid
	}
	now := time.Now()
	t.UsedAt = &now
	c := *t
	return &c, nil
}

// tokensByUser returns copies of all stored tokens for a user.
func (r *fakeAuthTokenRepo) tokensByUser(userID int64) []domain.AuthToken {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.AuthToken
	for _, t := range r.tokens {
		if t.UserID == userID {
			out = append(out, *t)
		}
	}
	return out
}

// fakeLoginHistoryRepo stores login events for the history screen.
type fakeLoginHistoryRepo struct {
	mu     sync.Mutex
	events []domain.LoginEvent
}

func (r *fakeLoginHistoryRepo) Record(_ context.Context, e domain.LoginEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}

func (r *fakeLoginHistoryRepo) ListByUser(_ context.Context, userID int64, limit int) ([]domain.LoginEvent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.LoginEvent
	for _, e := range r.events {
		if e.UserID != nil && *e.UserID == userID {
			out = append(out, e)
		}
	}
	// newest first
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].CreatedAt.After(out[j-1].CreatedAt); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (r *fakeLoginHistoryRepo) all() []domain.LoginEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]domain.LoginEvent, len(r.events))
	copy(out, r.events)
	return out
}

// fakeRisk is a scripted RiskEvaluator for the AUTH-11 hook.
type fakeRisk struct {
	decision domain.RiskDecision
	err      error
	// lastContext records the most recent evaluation input.
	lastContext domain.RiskContext
}

func (f *fakeRisk) Evaluate(_ context.Context, rc domain.RiskContext) (domain.RiskDecision, error) {
	f.lastContext = rc
	return f.decision, f.err
}

// fakeNotifier records security notifications for the account holder.
type fakeNotifier struct {
	mu          sync.Mutex
	notified    []string // event names, in order
	notifyError error
}

func (f *fakeNotifier) Notify(_ context.Context, _ int64, event string, _ map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.notifyError != nil {
		return f.notifyError
	}
	f.notified = append(f.notified, event)
	return nil
}

func (f *fakeNotifier) events() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.notified))
	copy(out, f.notified)
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
	verifier *fakeTokenVerifier
	throttle *fakeThrottle
	audit    *fakeAudit
	authToks *fakeAuthTokenRepo
	hist     *fakeLoginHistoryRepo
	risk     *fakeRisk
	notifier *fakeNotifier
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
		authToks: newFakeAuthTokenRepo(),
		hist:     &fakeLoginHistoryRepo{},
		risk:     &fakeRisk{},
		notifier: &fakeNotifier{},
		verifier: &fakeTokenVerifier{},
	}
	h.begin = &fakeBeginner{}
	h.svc = New(Deps{
		Users:                      h.users,
		Credentials:                h.creds,
		Sessions:                   h.sess,
		Hasher:                     fakeHasher{},
		Tokens:                     h.tokens,
		OTP:                        h.otp,
		Throttle:                   h.throttle,
		Policy:                     domain.DefaultLoginPolicy(),
		Audit:                      h.audit,
		IDs:                        h.ids,
		TxBeginner:                 h.begin,
		Clock:                      h.clk,
		SessionIdleTimeout:         30 * 24 * time.Hour,
		SessionRetention:           90 * 24 * time.Hour,
		AuthTokens:                 h.authToks,
		LoginHistory:               h.hist,
		Risk:                       h.risk,
		Notifier:                   h.notifier,
		Verifier:                   h.verifier,
		PasswordResetTokenTTL:      30 * time.Minute,
		ChangeVerificationTokenTTL: 15 * time.Minute,
		DeletionGracePeriod:        30 * 24 * time.Hour,
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
