package application

import (
	"context"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/domain"
	userdomain "github.com/AkaneSakuramori/socialmedia/server/internal/user/domain"
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

func (f *fakeTx) OnCommit(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onCommit = append(f.onCommit, fn)
}

type fakeBeginner struct{}

func (b *fakeBeginner) Begin(context.Context) (tx.Tx, error) {
	return &fakeTx{}, nil
}

type stubClock struct {
	now time.Time
}

func (c *stubClock) Now() time.Time { return c.now }

type fakeIDGen struct {
	next int64
}

func (g *fakeIDGen) NextID() (int64, error) {
	g.next++
	return g.next, nil
}

// fakeUserRepo is a trimmed userdomain.UserRepository: full behavior for the
// chat module's methods (ListByIDs/Create/FindByID), stubs elsewhere.
type fakeUserRepo struct {
	byID   map[int64]*userdomain.User
	byName map[int64]string
}

func newFakeUserRepo() *fakeUserRepo {
	return &fakeUserRepo{byID: map[int64]*userdomain.User{}, byName: map[int64]string{}}
}

func (r *fakeUserRepo) seed(id int64, name string) {
	r.byID[id] = &userdomain.User{ID: id, DisplayName: name, AccountState: userdomain.AccountActive}
	r.byName[id] = name
}

func (r *fakeUserRepo) Create(_ context.Context, _ tx.Tx, u *userdomain.User) error {
	r.byID[u.ID] = u
	r.byName[u.ID] = u.DisplayName
	return nil
}

func (r *fakeUserRepo) FindByID(_ context.Context, id int64) (*userdomain.User, error) {
	u, ok := r.byID[id]
	if !ok || u.AccountState == userdomain.AccountDeleted {
		return nil, userdomain.ErrUserNotFound
	}
	cp := *u
	return &cp, nil
}

func (r *fakeUserRepo) ListByIDs(_ context.Context, ids []int64) ([]userdomain.User, error) {
	var out []userdomain.User
	for _, id := range ids {
		u, ok := r.byID[id]
		if ok && u.AccountState != userdomain.AccountDeleted {
			out = append(out, *u)
		}
	}
	return out, nil
}

func (r *fakeUserRepo) FindByPhone(context.Context, string) (*userdomain.User, error) {
	return nil, userdomain.ErrUserNotFound
}
func (r *fakeUserRepo) FindByEmail(context.Context, string) (*userdomain.User, error) {
	return nil, userdomain.ErrUserNotFound
}
func (r *fakeUserRepo) PhoneTaken(context.Context, string) (bool, error) { return false, nil }
func (r *fakeUserRepo) EmailTaken(context.Context, string) (bool, error) { return false, nil }
func (r *fakeUserRepo) UsernameTaken(context.Context, string) (bool, error) {
	return false, nil
}
func (r *fakeUserRepo) BumpTokenVersion(context.Context, tx.Tx, int64) (int64, error) {
	return 0, nil
}
func (r *fakeUserRepo) SetEmail(context.Context, tx.Tx, int64, string) error { return nil }
func (r *fakeUserRepo) SetPhone(context.Context, tx.Tx, int64, string) error { return nil }
func (r *fakeUserRepo) MarkDeleted(context.Context, tx.Tx, int64, time.Time) error {
	return nil
}
func (r *fakeUserRepo) Restore(context.Context, tx.Tx, int64, time.Time) error { return nil }
func (r *fakeUserRepo) FindDeletedByPhone(context.Context, string) (*userdomain.User, error) {
	return nil, userdomain.ErrUserNotFound
}
func (r *fakeUserRepo) FindDeletedByEmail(context.Context, string) (*userdomain.User, error) {
	return nil, userdomain.ErrUserNotFound
}
func (r *fakeUserRepo) PurgeDeleted(context.Context, time.Time) (int64, error) { return 0, nil }

// fakeConversationRepo is an in-memory domain.ConversationRepository. It reads
// memberships through the shared fakeMembershipRepo so the direct-pair dedup
// and the chat-list query see consistent state.
type fakeConversationRepo struct {
	byID    map[int64]*domain.Conversation
	members *fakeMembershipRepo
	users   *fakeUserRepo
}

func newFakeConversationRepo(members *fakeMembershipRepo, users *fakeUserRepo) *fakeConversationRepo {
	return &fakeConversationRepo{byID: map[int64]*domain.Conversation{}, members: members, users: users}
}

func (r *fakeConversationRepo) Create(_ context.Context, _ tx.Tx, c *domain.Conversation) error {
	r.byID[c.ID] = c
	return nil
}

func (r *fakeConversationRepo) FindByID(_ context.Context, id int64) (*domain.Conversation, error) {
	c, ok := r.byID[id]
	if !ok || c.DeletedAt != nil {
		return nil, domain.ErrConversationNotFound
	}
	cp := *c
	return &cp, nil
}

func (r *fakeConversationRepo) FindDirectPair(_ context.Context, a, b int64) (*domain.Conversation, error) {
	for _, c := range r.byID {
		if c.Type != domain.ConversationDirect || c.DeletedAt != nil {
			continue
		}
		ms, ok := r.members.rows[c.ID]
		if !ok {
			continue
		}
		if len(ms) != 2 {
			continue
		}
		ma, oka := ms[a]
		mb, okb := ms[b]
		if oka && okb && ma.LeftAt == nil && mb.LeftAt == nil {
			cp := *c
			return &cp, nil
		}
	}
	return nil, domain.ErrConversationNotFound
}

func (r *fakeConversationRepo) Update(_ context.Context, _ tx.Tx, c *domain.Conversation) error {
	r.byID[c.ID] = c
	return nil
}

func (r *fakeConversationRepo) Tombstone(_ context.Context, _ tx.Tx, id int64, at time.Time) error {
	if c, ok := r.byID[id]; ok {
		c.DeletedAt = &at
	}
	return nil
}

func (r *fakeConversationRepo) List(_ context.Context, q domain.ConversationListQuery) ([]domain.ConversationRow, error) {
	var rows []domain.ConversationRow
	for _, c := range r.byID {
		if c.DeletedAt != nil {
			continue
		}
		m, ok := r.members.rows[c.ID][q.UserID]
		if !ok || m.LeftAt != nil {
			continue
		}
		switch q.Filter {
		case "pinned":
			if m.PinnedAt == nil {
				continue
			}
		case "archived":
			if m.ArchivedAt == nil {
				continue
			}
		case "groups":
			if c.Type != domain.ConversationGroup {
				continue
			}
		case "direct":
			if c.Type != domain.ConversationDirect {
				continue
			}
		}
		if q.UnreadOnly && !(c.LastMessageSeq != nil && *c.LastMessageSeq > m.LastReadSeq) {
			continue
		}
		row := domain.ConversationRow{Conversation: *c, Membership: *m}
		if c.Type == domain.ConversationDirect {
			for uid, om := range r.members.rows[c.ID] {
				if uid != q.UserID && om.LeftAt == nil {
					cp := uid
					row.CounterpartID = &cp
					break
				}
			}
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		ai, aj := rows[i].LastActivity(), rows[j].LastActivity()
		if !ai.Equal(aj) {
			return ai.After(aj)
		}
		return rows[i].ID > rows[j].ID
	})
	if q.Cursor != nil {
		out := rows[:0]
		for _, row := range rows {
			if row.LastActivity().Before(q.Cursor.Activity) ||
				(row.LastActivity().Equal(q.Cursor.Activity) && row.ID < q.Cursor.ID) {
				out = append(out, row)
			}
		}
		rows = out
	}
	if len(rows) > q.Limit {
		rows = rows[:q.Limit]
	}
	return rows, nil
}

// fakeMembershipRepo is an in-memory domain.MembershipRepository.
type fakeMembershipRepo struct {
	rows  map[int64]map[int64]*domain.Membership
	users *fakeUserRepo
}

func newFakeMembershipRepo(users *fakeUserRepo) *fakeMembershipRepo {
	return &fakeMembershipRepo{rows: map[int64]map[int64]*domain.Membership{}, users: users}
}

func (r *fakeMembershipRepo) AddMany(_ context.Context, _ tx.Tx, ms []*domain.Membership) error {
	for _, m := range ms {
		if r.rows[m.ConversationID] == nil {
			r.rows[m.ConversationID] = map[int64]*domain.Membership{}
		}
		cp := *m
		r.rows[m.ConversationID][m.UserID] = &cp
	}
	return nil
}

func (r *fakeMembershipRepo) FindActive(_ context.Context, convID, userID int64) (*domain.Membership, error) {
	m, ok := r.rows[convID][userID]
	if !ok || m.LeftAt != nil {
		return nil, domain.ErrMembershipNotFound
	}
	cp := *m
	return &cp, nil
}

func (r *fakeMembershipRepo) Update(_ context.Context, _ tx.Tx, m *domain.Membership) error {
	if r.rows[m.ConversationID] == nil {
		r.rows[m.ConversationID] = map[int64]*domain.Membership{}
	}
	cp := *m
	r.rows[m.ConversationID][m.UserID] = &cp
	return nil
}

func (r *fakeMembershipRepo) Remove(_ context.Context, _ tx.Tx, convID, userID int64, leftAt time.Time) error {
	if m, ok := r.rows[convID][userID]; ok {
		m.LeftAt = &leftAt
	}
	return nil
}

func (r *fakeMembershipRepo) CountActive(_ context.Context, convID int64) (int64, error) {
	var n int64
	for _, m := range r.rows[convID] {
		if m.LeftAt == nil {
			n++
		}
	}
	return n, nil
}

func (r *fakeMembershipRepo) ActiveUserIDs(_ context.Context, convID int64) ([]int64, error) {
	var ids []int64
	for uid, m := range r.rows[convID] {
		if m.LeftAt == nil {
			ids = append(ids, uid)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, nil
}

func (r *fakeMembershipRepo) ListMembers(_ context.Context, convID int64, q domain.MemberListQuery) ([]domain.MemberRow, error) {
	var rows []domain.MemberRow
	for uid, m := range r.rows[convID] {
		if m.LeftAt != nil {
			continue
		}
		u, ok := r.users.byID[uid]
		if !ok {
			continue
		}
		if q.Q != "" && !strings.Contains(strings.ToLower(u.DisplayName), strings.ToLower(q.Q)) {
			continue
		}
		rows = append(rows, domain.MemberRow{UserID: uid, DisplayName: u.DisplayName, Role: m.Role, JoinedAt: m.JoinedAt})
	}
	sort.Slice(rows, func(i, j int) bool {
		if !rows[i].JoinedAt.Equal(rows[j].JoinedAt) {
			return rows[i].JoinedAt.After(rows[j].JoinedAt)
		}
		return rows[i].UserID > rows[j].UserID
	})
	if q.Cursor != nil {
		out := rows[:0]
		for _, row := range rows {
			if row.JoinedAt.Before(q.Cursor.JoinedAt) ||
				(row.JoinedAt.Equal(q.Cursor.JoinedAt) && row.UserID < q.Cursor.UserID) {
				out = append(out, row)
			}
		}
		rows = out
	}
	if len(rows) > q.Limit {
		rows = rows[:q.Limit]
	}
	return rows, nil
}

type fakeSequenceRepo struct {
	byID map[int64]int64
}

func newFakeSequenceRepo() *fakeSequenceRepo { return &fakeSequenceRepo{byID: map[int64]int64{}} }

func (r *fakeSequenceRepo) Init(_ context.Context, _ tx.Tx, conversationID int64) error {
	r.byID[conversationID] = 0
	return nil
}

type fakeChangeLogRepo struct {
	entries []domain.ChangeLogEntry
}

func (r *fakeChangeLogRepo) Append(_ context.Context, _ tx.Tx, es []domain.ChangeLogEntry) error {
	r.entries = append(r.entries, es...)
	return nil
}

func (r *fakeChangeLogRepo) types() []string {
	var out []string
	for _, e := range r.entries {
		out = append(out, e.EventType)
	}
	return out
}

// harness wires the chat service with in-memory fakes.
type harness struct {
	svc       Service
	begin     *fakeBeginner
	users     *fakeUserRepo
	convos    *fakeConversationRepo
	members   *fakeMembershipRepo
	sequences *fakeSequenceRepo
	changelog *fakeChangeLogRepo
	ids       *fakeIDGen
	clk       *stubClock
	now       time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	users := newFakeUserRepo()
	members := newFakeMembershipRepo(users)
	convos := newFakeConversationRepo(members, users)
	h := &harness{
		begin:     &fakeBeginner{},
		users:     users,
		convos:    convos,
		members:   members,
		sequences: newFakeSequenceRepo(),
		changelog: &fakeChangeLogRepo{},
		ids:       &fakeIDGen{next: 9000000000},
		now:       time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC),
	}
	h.clk = &stubClock{now: h.now}
	h.svc = New(Deps{
		Conversations: convos,
		Memberships:   members,
		Sequences:     h.sequences,
		ChangeLog:     h.changelog,
		Users:         users,
		IDs:           h.ids,
		TxBeginner:    h.begin,
		Clock:         h.clk,
	})
	return h
}
