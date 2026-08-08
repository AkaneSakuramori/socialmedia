package application

import (
	"context"
	"sort"
	"strconv"
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

func (r *fakeUserRepo) ListByIDs(_ context.Context, _ tx.Querier, ids []int64) ([]userdomain.User, error) {
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

func (r *fakeConversationRepo) BumpLastMessage(_ context.Context, _ tx.Tx, id, seq int64, snippet *string, senderID *int64, at time.Time) (bool, error) {
	c, ok := r.byID[id]
	if !ok {
		return false, nil
	}
	if c.LastMessageSeq != nil && *c.LastMessageSeq >= seq {
		return false, nil // monotonic guard: never regress
	}
	c.LastMessageAt = &at
	c.LastMessageSeq = &seq
	c.LastMessageSnippet = snippet
	c.LastSenderID = senderID
	c.UpdatedAt = at
	return true, nil
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

func (r *fakeMembershipRepo) ActiveUserIDs(_ context.Context, _ tx.Querier, convID int64) ([]int64, error) {
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

// MarkRead advances cursors monotonically (GREATEST semantics).
func (r *fakeMembershipRepo) MarkRead(_ context.Context, _ tx.Tx, convID, userID, readSeq, deliveredSeq int64, at time.Time) (bool, bool, error) {
	m := r.rows[convID][userID]
	if m == nil || m.LeftAt != nil {
		return false, false, nil
	}
	advR := readSeq > m.LastReadSeq
	if advR {
		m.LastReadSeq = readSeq
		m.LastReadAt = &at
	}
	// A read receipt implies delivery (migration 000006 CHECK: delivered >= read).
	effDelivered := deliveredSeq
	if readSeq > effDelivered {
		effDelivered = readSeq
	}
	advD := effDelivered > m.LastDeliveredSeq
	if advD {
		m.LastDeliveredSeq = effDelivered
	}
	return advR, advD, nil
}

func (r *fakeMembershipRepo) ListReceipts(_ context.Context, convID int64) ([]domain.ReceiptRow, error) {
	var out []domain.ReceiptRow
	for uid, m := range r.rows[convID] {
		if m.LeftAt != nil {
			continue
		}
		out = append(out, domain.ReceiptRow{UserID: uid, LastReadSeq: m.LastReadSeq, LastReadAt: m.LastReadAt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UserID < out[j].UserID })
	return out, nil
}

func (r *fakeMembershipRepo) CursorsByConversation(_ context.Context, convID int64) ([]domain.CursorRow, error) {
	var out []domain.CursorRow
	for uid, m := range r.rows[convID] {
		if m.LeftAt != nil {
			continue
		}
		out = append(out, domain.CursorRow{UserID: uid, LastReadSeq: m.LastReadSeq, LastDeliveredSeq: m.LastDeliveredSeq})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UserID < out[j].UserID })
	return out, nil
}

type fakeSequenceRepo struct {
	byID map[int64]int64
}

func newFakeSequenceRepo() *fakeSequenceRepo { return &fakeSequenceRepo{byID: map[int64]int64{}} }

func (r *fakeSequenceRepo) Init(_ context.Context, _ tx.Tx, conversationID int64) error {
	r.byID[conversationID] = 0
	return nil
}

// fakeSequenceSource is an in-memory domain.SequenceSource: Redis-free counter
// with the durable floor semantics (max-merge Persist).
type fakeSequenceSource struct {
	counters map[int64]int64
	floors   map[int64]int64
}

func newFakeSequenceSource() *fakeSequenceSource {
	return &fakeSequenceSource{counters: map[int64]int64{}, floors: map[int64]int64{}}
}

func (s *fakeSequenceSource) Next(_ context.Context, conversationID int64) (int64, error) {
	s.counters[conversationID]++
	return s.counters[conversationID], nil
}

func (s *fakeSequenceSource) Persist(_ context.Context, _ tx.Tx, conversationID, sequence int64) error {
	if s.floors[conversationID] < sequence {
		s.floors[conversationID] = sequence
	}
	return nil
}

func (s *fakeSequenceSource) Floor(_ context.Context, conversationID int64) (int64, error) {
	return s.floors[conversationID], nil
}

// fakeMessageRepo is an in-memory domain.MessageRepository modeling the
// partial-unique dedupe on (sender_id, client_msg_id) and the guarded
// edit/tombstone updates.
type fakeMessageRepo struct {
	byID        map[int64]*domain.Message
	edits       []domain.MessageEdit
	conflictSeq map[int64]bool // sequences that collide on the composite PK
}

func newFakeMessageRepo() *fakeMessageRepo {
	return &fakeMessageRepo{byID: map[int64]*domain.Message{}, conflictSeq: map[int64]bool{}}
}

// setConflict makes Insert return ErrSequenceConflict for the given sequence,
// simulating a composite-PK collision (DATABASE.md §11 final guard).
func (r *fakeMessageRepo) setConflict(seq int64) { r.conflictSeq[seq] = true }

func (r *fakeMessageRepo) Insert(_ context.Context, _ tx.Tx, m *domain.Message) (bool, error) {
	if r.conflictSeq[m.Sequence] {
		return false, domain.ErrSequenceConflict
	}
	for _, ex := range r.byID {
		if ex.SenderID != nil && m.SenderID != nil &&
			*ex.SenderID == *m.SenderID && m.ClientMsgID != nil &&
			ex.ClientMsgID != nil && *ex.ClientMsgID == *m.ClientMsgID {
			return false, nil // dedupe replay
		}
	}
	cp := *m
	r.byID[m.ID] = &cp
	return true, nil
}

func (r *fakeMessageRepo) FindByClientMsgID(_ context.Context, _ tx.Querier, senderID int64, clientMsgID string) (*domain.Message, error) {
	for _, m := range r.byID {
		if m.SenderID != nil && *m.SenderID == senderID && m.ClientMsgID != nil && *m.ClientMsgID == clientMsgID {
			cp := *m
			return &cp, nil
		}
	}
	return nil, domain.ErrMessageNotFound
}

func (r *fakeMessageRepo) FindByID(_ context.Context, id int64) (*domain.Message, error) {
	m, ok := r.byID[id]
	if !ok {
		return nil, domain.ErrMessageNotFound
	}
	cp := *m
	return &cp, nil
}

func (r *fakeMessageRepo) FindByConversationSeq(_ context.Context, convID, seq int64) (*domain.Message, error) {
	for _, m := range r.byID {
		if m.ConversationID == convID && m.Sequence == seq {
			cp := *m
			return &cp, nil
		}
	}
	return nil, domain.ErrMessageNotFound
}

func (r *fakeMessageRepo) ListByConversation(_ context.Context, q domain.MessageListQuery) ([]domain.Message, error) {
	var out []domain.Message
	for _, m := range r.byID {
		if m.ConversationID != q.ConversationID {
			continue
		}
		if q.BeforeSeq != nil && m.Sequence >= *q.BeforeSeq {
			continue
		}
		if q.AfterGlobalSeq != nil && m.GlobalSeq <= *q.AfterGlobalSeq {
			continue
		}
		out = append(out, *m)
	}
	if q.AfterGlobalSeq != nil {
		sort.Slice(out, func(i, j int) bool { return out[i].GlobalSeq < out[j].GlobalSeq })
	} else {
		sort.Slice(out, func(i, j int) bool { return out[i].Sequence > out[j].Sequence })
	}
	if len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

func (r *fakeMessageRepo) Edit(_ context.Context, _ tx.Tx, editID int64, m *domain.Message, oldContent string, at time.Time) (bool, error) {
	cur, ok := r.byID[m.ID]
	if !ok || cur.DeletedAt != nil {
		return false, nil
	}
	r.edits = append(r.edits, domain.MessageEdit{ID: editID, MessageID: m.ID, OldContent: oldContent, EditedAt: at})
	cur.Content = m.Content
	cur.EditCount++
	cur.EditedAt = &at
	return true, nil
}

func (r *fakeMessageRepo) Tombstone(_ context.Context, _ tx.Tx, id, deletedBy int64, at time.Time) (bool, error) {
	m, ok := r.byID[id]
	if !ok || m.DeletedAt != nil {
		return false, nil
	}
	m.DeletedAt = &at
	m.DeletedBy = &deletedBy
	return true, nil
}

func (r *fakeMessageRepo) SenderIDsBetween(_ context.Context, _ tx.Tx, convID, from, to int64) ([]int64, error) {
	seen := map[int64]bool{}
	var out []int64
	for _, m := range r.byID {
		if m.ConversationID == convID && m.Sequence > from && m.Sequence <= to && m.SenderID != nil {
			if !seen[*m.SenderID] {
				seen[*m.SenderID] = true
				out = append(out, *m.SenderID)
			}
		}
	}
	return out, nil
}

// fakeReactionRepo is an in-memory domain.ReactionRepository.
type fakeReactionRepo struct {
	rows map[string]domain.ReactionRow
}

func newFakeReactionRepo() *fakeReactionRepo {
	return &fakeReactionRepo{rows: map[string]domain.ReactionRow{}}
}

func reactionKey(mid, uid int64, emoji string) string {
	return strconv.FormatInt(mid, 10) + ":" + strconv.FormatInt(uid, 10) + ":" + emoji
}

func (r *fakeReactionRepo) Add(_ context.Context, _ tx.Tx, rec *domain.ReactionRow) (bool, error) {
	k := reactionKey(rec.MessageID, rec.UserID, rec.Emoji)
	if _, ok := r.rows[k]; ok {
		return false, nil
	}
	r.rows[k] = *rec
	return true, nil
}

func (r *fakeReactionRepo) Remove(_ context.Context, _ tx.Tx, mid, uid int64, emoji string) (bool, error) {
	k := reactionKey(mid, uid, emoji)
	if _, ok := r.rows[k]; !ok {
		return false, nil
	}
	delete(r.rows, k)
	return true, nil
}

func (r *fakeReactionRepo) DistinctEmoji(_ context.Context, mid int64) (int64, error) {
	seen := map[string]bool{}
	for _, rec := range r.rows {
		if rec.MessageID == mid {
			seen[rec.Emoji] = true
		}
	}
	return int64(len(seen)), nil
}

func (r *fakeReactionRepo) Count(_ context.Context, mid int64, emoji string) (int64, error) {
	var n int64
	for _, rec := range r.rows {
		if rec.MessageID == mid && rec.Emoji == emoji {
			n++
		}
	}
	return n, nil
}

func (r *fakeReactionRepo) CountsByMessages(_ context.Context, ids []int64) (map[int64]map[string]int64, error) {
	out := map[int64]map[string]int64{}
	for _, rec := range r.rows {
		if !contains(ids, rec.MessageID) {
			continue
		}
		if out[rec.MessageID] == nil {
			out[rec.MessageID] = map[string]int64{}
		}
		out[rec.MessageID][rec.Emoji]++
	}
	return out, nil
}

func (r *fakeReactionRepo) UserIDsByMessages(_ context.Context, ids []int64) (map[int64]map[string][]int64, error) {
	out := map[int64]map[string][]int64{}
	for _, rec := range r.rows {
		if !contains(ids, rec.MessageID) {
			continue
		}
		if out[rec.MessageID] == nil {
			out[rec.MessageID] = map[string][]int64{}
		}
		out[rec.MessageID][rec.Emoji] = append(out[rec.MessageID][rec.Emoji], rec.UserID)
	}
	return out, nil
}

func (r *fakeReactionRepo) Reactors(_ context.Context, mid int64, emoji string) ([]domain.Reactor, error) {
	var out []domain.Reactor
	for _, rec := range r.rows {
		if rec.MessageID == mid && rec.Emoji == emoji {
			out = append(out, domain.Reactor{UserID: rec.UserID, At: rec.CreatedAt})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out, nil
}

func contains(ids []int64, id int64) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

type fakeChangeLogRepo struct {
	entries []domain.ChangeLogEntry
	head    int64
}

func (r *fakeChangeLogRepo) Append(_ context.Context, _ tx.Tx, es []domain.ChangeLogEntry) error {
	r.entries = append(r.entries, es...)
	return nil
}

func (r *fakeChangeLogRepo) Head(context.Context) (int64, error) { return r.head, nil }

func (r *fakeChangeLogRepo) ListAfter(_ context.Context, after, limit int64) ([]domain.ChangeLogRow, error) {
	var out []domain.ChangeLogRow
	for _, e := range r.entries {
		if int64(len(out)) >= limit {
			break
		}
		row := domain.ChangeLogRow{
			EventType:       e.EventType,
			ConversationID:  e.ConversationID,
			EntityID:        e.EntityID,
			ActorUserID:     e.ActorUserID,
			AffectedUserIDs: e.AffectedUserIDs,
			Payload:         e.Payload,
		}
		r.head++
		row.GlobalSeq = r.head
		if row.GlobalSeq <= after {
			continue
		}
		out = append(out, row)
	}
	return out, nil
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
	messages  *fakeMessageRepo
	reactions *fakeReactionRepo
	seqsource *fakeSequenceSource
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
	messages := newFakeMessageRepo()
	h := &harness{
		begin:     &fakeBeginner{},
		users:     users,
		convos:    convos,
		members:   members,
		sequences: newFakeSequenceRepo(),
		messages:  messages,
		reactions: newFakeReactionRepo(),
		seqsource: newFakeSequenceSource(),
		changelog: &fakeChangeLogRepo{},
		ids:       &fakeIDGen{next: 9000000000},
		now:       time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC),
	}
	h.clk = &stubClock{now: h.now}
	h.svc = New(Deps{
		Conversations:  convos,
		Memberships:    members,
		Sequences:      h.sequences,
		Messages:       messages,
		Reactions:      h.reactions,
		SequenceSource: h.seqsource,
		ChangeLog:      h.changelog,
		Users:          users,
		IDs:            h.ids,
		TxBeginner:     h.begin,
		Clock:          h.clk,
	})
	return h
}
