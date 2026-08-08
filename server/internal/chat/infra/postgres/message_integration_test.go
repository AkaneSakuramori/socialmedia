//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/application"
	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/domain"
	"github.com/AkaneSakuramori/socialmedia/server/internal/platform/idgen"
	platformpg "github.com/AkaneSakuramori/socialmedia/server/internal/platform/postgres"
	"github.com/AkaneSakuramori/socialmedia/server/pkg/clock"
	"github.com/AkaneSakuramori/socialmedia/server/pkg/tx"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	authpostgres "github.com/AkaneSakuramori/socialmedia/server/internal/auth/infra/postgres"
)

// integCleanMessages removes every message-row family for a conversation
// (registered last so it runs first, before the conversation cleanup).
func integCleanMessages(t *testing.T, p *pgxpool.Pool, convID int64) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = p.Exec(ctx, `DELETE FROM message_reactions WHERE message_id IN (SELECT id FROM messages WHERE conversation_id = $1)`, convID)
		_, _ = p.Exec(ctx, `DELETE FROM message_edits WHERE message_id IN (SELECT id FROM messages WHERE conversation_id = $1)`, convID)
		_, _ = p.Exec(ctx, `DELETE FROM change_log WHERE conversation_id = $1`, convID)
		_, _ = p.Exec(ctx, `DELETE FROM messages WHERE conversation_id = $1`, convID)
	})
}

// integSequenceRow creates the conversation_sequences row for a conversation.
func integSequenceRow(t *testing.T, begin tx.Beginner, convID int64) {
	t.Helper()
	runChat(t, begin, func(dbtx tx.Tx) error {
		return NewSequenceRepo().Init(context.Background(), dbtx, convID)
	})
}

// integRedis connects to the local stack Redis, skipping when unreachable.
func integRedis(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	rc := redis.NewClient(&redis.Options{Addr: addr})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rc.Ping(ctx).Err(); err != nil {
		t.Skipf("redis not reachable at %s, skipping: %v", addr, err)
	}
	t.Cleanup(func() { _ = rc.Close() })
	return rc
}

// integChatService wires the full application service over real adapters:
// postgres repos, Redis sequence source, real idgen, and the auth user repo.
func integChatService(t *testing.T, p *pgxpool.Pool, rc *redis.Client) application.Service {
	t.Helper()
	ids, err := idgen.New(99, idgen.DefaultEpoch)
	if err != nil {
		t.Fatalf("idgen: %v", err)
	}
	return application.New(application.Deps{
		Conversations:  NewConversationRepo(p),
		Memberships:    NewMembershipRepo(p),
		Sequences:      NewSequenceRepo(),
		Messages:       NewMessageRepo(p),
		Reactions:      NewReactionRepo(p),
		SequenceSource: NewSequenceSource(p, rc),
		ChangeLog:      NewChangeLogRepo(p),
		Users:          authpostgres.NewUserRepo(p),
		IDs:            ids,
		TxBeginner:     platformpg.NewBeginner(p),
		Clock:          clock.System(),
		DB:             platformpg.NewQuerier(p),
	})
}

// integConversationTwo seeds a 2-member group (owner + member) and returns
// both the conversation id and the member id.
func integConversationTwo(t *testing.T, p *pgxpool.Pool) (convID, owner, member int64) {
	t.Helper()
	owner = integUser(t, p)
	member = integUser(t, p)
	convID = integConversation(t, p, domain.ConversationGroup, owner, strptr("Conv"), at(0))
	integCleanMessages(t, p, convID)
	integSequenceRow(t, platformpg.NewBeginner(p), convID)
	integMembers(t, p, convID, domain.RoleOwner, at(0), owner)
	integMembers(t, p, convID, domain.RoleMember, at(1), member)
	return
}

// integInsertMessage inserts one message row directly (bypassing the service).
func integInsertMessage(t *testing.T, p *pgxpool.Pool, m *domain.Message) {
	t.Helper()
	repo := NewMessageRepo(p)
	runChat(t, platformpg.NewBeginner(p), func(dbtx tx.Tx) error {
		_, err := repo.Insert(context.Background(), dbtx, m)
		return err
	})
}

// ---- message repo (DATABASE.md §5.3) ----

func TestIntegMessageInsertDedupeAndFind(t *testing.T) {
	p := integPool(t)
	convID, owner, member := integConversationTwo(t, p)
	repo := NewMessageRepo(p)
	ctx := context.Background()

	text := "hello"
	sender := owner
	m := &domain.Message{
		ID: 90000000000001, ConversationID: convID, Sequence: 1,
		ClientMsgID: strptr("cm-1"), SenderID: &sender,
		Type: domain.MessageTypeText, Content: &text, CreatedAt: at(10),
	}
	integInsertMessage(t, p, m)

	got, err := repo.FindByID(ctx, m.ID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if got.Sequence != 1 || got.ConversationID != convID || got.Content == nil || *got.Content != "hello" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if got.GlobalSeq == 0 {
		t.Error("global_seq not assigned")
	}

	bySeq, err := repo.FindByConversationSeq(ctx, convID, 1)
	if err != nil || bySeq.ID != m.ID {
		t.Errorf("FindByConversationSeq = %+v, %v", bySeq, err)
	}

	// A different sender may reuse the same client_msg_id (per-user scope).
	other := &domain.Message{
		ID: 90000000000002, ConversationID: convID, Sequence: 2,
		ClientMsgID: strptr("cm-1"), SenderID: &member,
		Type: domain.MessageTypeText, Content: &text, CreatedAt: at(11),
	}
	integInsertMessage(t, p, other)

	// The same (sender, client_msg_id) collapses to a no-op insert.
	dup := *m
	dup.Sequence = 3
	dup.ID = 90000000000003
	inserted, err := repo.Insert(ctx, beginTx(t, platformpg.NewBeginner(p)), &dup)
	if err != nil {
		t.Fatalf("dup insert: %v", err)
	}
	if inserted {
		t.Error("duplicate (sender, client_msg_id) inserted a second row")
	}
	orig, err := repo.FindByClientMsgID(ctx, platformpg.NewQuerier(p), owner, "cm-1")
	if err != nil || orig.ID != m.ID || orig.Sequence != 1 {
		t.Errorf("replay lookup = %+v, %v; want original seq 1", orig, err)
	}
}

func TestIntegMessageCompositePKConflict(t *testing.T) {
	p := integPool(t)
	convID, owner, member := integConversationTwo(t, p)
	repo := NewMessageRepo(p)
	ctx := context.Background()

	text := "hi"
	a, b := owner, member
	m1 := &domain.Message{ID: 90000000000011, ConversationID: convID, Sequence: 1, ClientMsgID: strptr("a"), SenderID: &a, Type: domain.MessageTypeText, Content: &text, CreatedAt: at(0)}
	m2 := &domain.Message{ID: 90000000000012, ConversationID: convID, Sequence: 1, ClientMsgID: strptr("b"), SenderID: &b, Type: domain.MessageTypeText, Content: &text, CreatedAt: at(1)}
	integInsertMessage(t, p, m1)

	// Same sequence, different sender + client id: the composite PK fires.
	_, err := repo.Insert(ctx, beginTx(t, platformpg.NewBeginner(p)), m2)
	if !errors.Is(err, domain.ErrSequenceConflict) {
		t.Errorf("err = %v, want ErrSequenceConflict", err)
	}
}

func TestIntegMessageListKeysetAndDelta(t *testing.T) {
	p := integPool(t)
	convID, owner, _ := integConversationTwo(t, p)
	repo := NewMessageRepo(p)
	ctx := context.Background()

	for seq := int64(1); seq <= 5; seq++ {
		text := fmt.Sprintf("m%d", seq)
		s := owner
		integInsertMessage(t, p, &domain.Message{
			ID: 90000000000020 + seq, ConversationID: convID, Sequence: seq,
			ClientMsgID: strptr(fmt.Sprintf("cm-%d", seq)), SenderID: &s,
			Type: domain.MessageTypeText, Content: &text, CreatedAt: at(int(seq)),
		})
	}

	// Newest page: newest-first from the repo.
	page, err := repo.ListByConversation(ctx, domain.MessageListQuery{ConversationID: convID, Limit: 3})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page) != 3 || page[0].Sequence != 5 || page[1].Sequence != 4 || page[2].Sequence != 3 {
		t.Errorf("newest page = %v, want [5 4 3]", seqs(page))
	}

	// Scroll-back keyset: before=3 → sequences 1,2 (newest first).
	page, err = repo.ListByConversation(ctx, domain.MessageListQuery{ConversationID: convID, Limit: 10, BeforeSeq: ptr64(3)})
	if err != nil {
		t.Fatalf("List before: %v", err)
	}
	if got := seqs(page); len(got) != 2 || got[0] != 2 || got[1] != 1 {
		t.Errorf("before=3 = %v, want [2 1]", got)
	}

	// Delta poll: after_global_seq on the second row → ascending global order.
	second, err := repo.FindByConversationSeq(ctx, convID, 2)
	if err != nil {
		t.Fatalf("find second: %v", err)
	}
	page, err = repo.ListByConversation(ctx, domain.MessageListQuery{ConversationID: convID, Limit: 10, AfterGlobalSeq: &second.GlobalSeq})
	if err != nil {
		t.Fatalf("List delta: %v", err)
	}
	if got := seqs(page); len(got) != 3 || got[0] != 3 || got[1] != 4 || got[2] != 5 {
		t.Errorf("delta after g2 = %v, want [3 4 5] ascending", got)
	}
}

func TestIntegMessageEditAndTombstone(t *testing.T) {
	p := integPool(t)
	convID, owner, _ := integConversationTwo(t, p)
	repo := NewMessageRepo(p)
	begin := platformpg.NewBeginner(p)
	ctx := context.Background()

	text := "before"
	s := owner
	integInsertMessage(t, p, &domain.Message{
		ID: 90000000000031, ConversationID: convID, Sequence: 1,
		ClientMsgID: strptr("cm-1"), SenderID: &s,
		Type: domain.MessageTypeText, Content: &text, CreatedAt: at(0),
	})
	m, err := repo.FindByConversationSeq(ctx, convID, 1)
	if err != nil {
		t.Fatalf("find: %v", err)
	}

	// Edit records the new body + an append-only history row (committed).
	newText := "after"
	m.Content = &newText
	updated := false
	runChat(t, begin, func(dbtx tx.Tx) error {
		var err error
		updated, err = repo.Edit(ctx, dbtx, 90000000000041, m, "before", at(1))
		return err
	})
	if err != nil || !updated {
		t.Fatalf("edit = %v, %v", updated, err)
	}
	var oldContent string
	if err := p.QueryRow(ctx, `SELECT old_content FROM message_edits WHERE message_id = $1`, m.ID).Scan(&oldContent); err != nil {
		t.Fatalf("read edit history: %v", err)
	}
	if oldContent != "before" {
		t.Errorf("edit history old_content = %q, want before", oldContent)
	}

	// Tombstone succeeds; a second tombstone is a no-op (idempotent).
	ok := false
	runChat(t, begin, func(dbtx tx.Tx) error {
		var err error
		ok, err = repo.Tombstone(ctx, dbtx, m.ID, owner, at(2))
		return err
	})
	if err != nil || !ok {
		t.Fatalf("tombstone = %v, %v", ok, err)
	}
	ok = true
	runChat(t, begin, func(dbtx tx.Tx) error {
		var err error
		ok, err = repo.Tombstone(ctx, dbtx, m.ID, owner, at(3))
		return err
	})
	if err != nil || ok {
		t.Errorf("second tombstone = %v, %v; want false (no-op)", ok, err)
	}

	// An edit racing the tombstone must be rejected (guarded UPDATE).
	again, err := repo.FindByID(ctx, m.ID)
	if err != nil {
		t.Fatalf("find after tombstone: %v", err)
	}
	again.Content = strptr("too late")
	updated = true
	runChat(t, begin, func(dbtx tx.Tx) error {
		var err error
		updated, err = repo.Edit(ctx, dbtx, 90000000000042, again, "after", at(4))
		return err
	})
	if err != nil {
		t.Fatalf("edit after tombstone err = %v", err)
	}
	if updated {
		t.Error("edit succeeded on a tombstoned message")
	}
}

func TestIntegMessageSenderIDsBetween(t *testing.T) {
	p := integPool(t)
	convID, owner, member := integConversationTwo(t, p)
	repo := NewMessageRepo(p)
	begin := platformpg.NewBeginner(p)
	ctx := context.Background()

	for seq, s := range map[int64]int64{1: owner, 2: member, 3: owner, 4: member} {
		text := "x"
		integInsertMessage(t, p, &domain.Message{
			ID: 90000000000050 + seq, ConversationID: convID, Sequence: seq,
			ClientMsgID: strptr(fmt.Sprintf("cm-%d", seq)), SenderID: &s,
			Type: domain.MessageTypeText, Content: &text, CreatedAt: at(int(seq)),
		})
	}

	// Distinct senders in (1, 4] → owner + member; (1, 3] → owner only.
	dbtx := beginTx(t, begin)
	defer dbtx.Rollback(context.Background())
	ids, err := repo.SenderIDsBetween(ctx, dbtx, convID, 1, 4)
	if err != nil {
		t.Fatalf("SenderIDsBetween: %v", err)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if len(ids) != 2 || ids[0] != owner || ids[1] != member {
		t.Errorf("senders (1,4] = %v, want [%d %d]", ids, owner, member)
	}
}

// ---- reactions (DATABASE.md §5.6) ----

func TestIntegReactionLifecycle(t *testing.T) {
	p := integPool(t)
	convID, owner, member := integConversationTwo(t, p)
	repo := NewReactionRepo(p)
	begin := platformpg.NewBeginner(p)
	ctx := context.Background()

	text := "hi"
	s := owner
	integInsertMessage(t, p, &domain.Message{
		ID: 90000000000061, ConversationID: convID, Sequence: 1,
		ClientMsgID: strptr("cm-1"), SenderID: &s,
		Type: domain.MessageTypeText, Content: &text, CreatedAt: at(0),
	})

	reactSeq := int64(90000000000070)
	add := func(uid int64, emoji string) {
		t.Helper()
		reactSeq++
		added := false
		runChat(t, begin, func(dbtx tx.Tx) error {
			var err error
			added, err = repo.Add(ctx, dbtx, &domain.ReactionRow{ID: reactSeq, MessageID: 90000000000061, UserID: uid, Emoji: emoji, CreatedAt: at(1)})
			return err
		})
		if !added {
			t.Fatalf("add reaction (%d,%s) not applied", uid, emoji)
		}
	}
	add(owner, "👍")
	add(member, "👍")
	add(owner, "🔥")

	if n, err := repo.DistinctEmoji(ctx, 90000000000061); err != nil || n != 2 {
		t.Errorf("distinct emoji = %d, %v; want 2", n, err)
	}
	if n, err := repo.Count(ctx, 90000000000061, "👍"); err != nil || n != 2 {
		t.Errorf("count thumbs = %d, %v; want 2", n, err)
	}
	counts, err := repo.CountsByMessages(ctx, []int64{90000000000061})
	if err != nil || counts[90000000000061]["👍"] != 2 || counts[90000000000061]["🔥"] != 1 {
		t.Errorf("counts = %+v, %v", counts, err)
	}
	users, err := repo.UserIDsByMessages(ctx, []int64{90000000000061})
	if err != nil || len(users[90000000000061]["👍"]) != 2 {
		t.Errorf("user ids = %+v, %v", users, err)
	}

	// A duplicate add is a no-op.
	added := true
	runChat(t, begin, func(dbtx tx.Tx) error {
		reactSeq++
		var err error
		added, err = repo.Add(ctx, dbtx, &domain.ReactionRow{ID: reactSeq, MessageID: 90000000000061, UserID: owner, Emoji: "👍", CreatedAt: at(2)})
		return err
	})
	if added {
		t.Error("duplicate add was applied")
	}

	// Remove returns whether a row was actually deleted.
	removed := false
	runChat(t, begin, func(dbtx tx.Tx) error {
		var err error
		removed, err = repo.Remove(ctx, dbtx, 90000000000061, owner, "👍")
		return err
	})
	if !removed {
		t.Fatal("remove did not delete")
	}
	removed = true
	runChat(t, begin, func(dbtx tx.Tx) error {
		var err error
		removed, err = repo.Remove(ctx, dbtx, 90000000000061, owner, "👍")
		return err
	})
	if removed {
		t.Error("second remove deleted again")
	}

	reactors, err := repo.Reactors(ctx, 90000000000061, "👍")
	if err != nil || len(reactors) != 1 || reactors[0].UserID != member {
		t.Errorf("reactors = %+v, %v; want [member]", reactors, err)
	}
}

// ---- receipts (API.md §10.1/§10.2) ----

func TestIntegMarkReadConcurrentMonotonic(t *testing.T) {
	p := integPool(t)
	convID, owner, _ := integConversationTwo(t, p)
	memRepo := NewMembershipRepo(p)
	begin := platformpg.NewBeginner(p)
	ctx := context.Background()

	const goroutines = 12
	const maxSeq = 50
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(seq int64) {
			defer wg.Done()
			dbtx, err := begin.Begin(ctx)
			if err != nil {
				t.Errorf("begin: %v", err)
				return
			}
			defer dbtx.Rollback(ctx)
			if _, _, err := memRepo.MarkRead(ctx, dbtx, convID, owner, seq, seq, time.Now()); err != nil {
				t.Errorf("mark read %d: %v", seq, err)
			}
			if err := dbtx.Commit(ctx); err != nil {
				t.Errorf("commit %d: %v", seq, err)
			}
		}(int64(i+1) * maxSeq / goroutines)
	}
	wg.Wait()

	m, err := memRepo.FindActive(ctx, convID, owner)
	if err != nil {
		t.Fatalf("FindActive: %v", err)
	}
	// The cursor is the max, never any intermediate value — GREATEST wins.
	if m.LastReadSeq != maxSeq {
		t.Errorf("last_read_seq = %d, want %d (max, never regressed)", m.LastReadSeq, maxSeq)
	}
	if m.LastDeliveredSeq != maxSeq {
		t.Errorf("last_delivered_seq = %d, want %d", m.LastDeliveredSeq, maxSeq)
	}

	// ListReceipts + CursorsByConversation expose the final state.
	receipts, err := memRepo.ListReceipts(ctx, convID)
	if err != nil {
		t.Fatalf("ListReceipts: %v", err)
	}
	for _, r := range receipts {
		if r.UserID == owner && r.LastReadSeq != maxSeq {
			t.Errorf("ListReceipts owner = %d, want %d", r.LastReadSeq, maxSeq)
		}
	}
	cursors, err := memRepo.CursorsByConversation(ctx, convID)
	if err != nil {
		t.Fatalf("CursorsByConversation: %v", err)
	}
	if len(cursors) != 2 {
		t.Errorf("cursors = %d rows, want 2 (member + owner)", len(cursors))
	}
}

// ---- sequence source (ARCHITECTURE.md §13.2, DATABASE.md §5.4) ----

func TestIntegSequenceSourceRedisAndFloor(t *testing.T) {
	p := integPool(t)
	rc := integRedis(t)
	convID, _, _ := integConversationTwo(t, p)
	seqSrc := NewSequenceSource(p, rc)
	ctx := context.Background()

	// Fresh Redis key: allocate 1,2,3 and persist the floor to 3.
	if err := rc.Del(ctx, seqKey(convID)).Err(); err != nil {
		t.Fatalf("reset redis key: %v", err)
	}
	var got []int64
	for i := 0; i < 3; i++ {
		n, err := seqSrc.Next(ctx, convID)
		if err != nil {
			t.Fatalf("Next %d: %v", i, err)
		}
		got = append(got, n)
	}
	if got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Fatalf("next = %v, want [1 2 3]", got)
	}
	runChat(t, platformpg.NewBeginner(p), func(dbtx tx.Tx) error {
		return seqSrc.Persist(ctx, dbtx, convID, 3)
	})
	floor, err := seqSrc.Floor(ctx, convID)
	if err != nil || floor != 3 {
		t.Fatalf("floor = %d, %v; want 3", floor, err)
	}

	// Redis restart: the key is gone, so the counter must bootstrap to the
	// durable floor and continue from 4 — never reuse 1..3.
	if err := rc.Del(ctx, seqKey(convID)).Err(); err != nil {
		t.Fatalf("del redis key: %v", err)
	}
	n, err := seqSrc.Next(ctx, convID)
	if err != nil {
		t.Fatalf("Next after redis loss: %v", err)
	}
	if n != 4 {
		t.Errorf("sequence after redis restart = %d, want 4 (floor-bootstrapped)", n)
	}

	// Persist is a GREATEST max-merge: a stale flush can never regress the floor.
	runChat(t, platformpg.NewBeginner(p), func(dbtx tx.Tx) error {
		return seqSrc.Persist(ctx, dbtx, convID, 2) // stale
	})
	if floor, _ = seqSrc.Floor(ctx, convID); floor < 3 {
		t.Errorf("floor regressed to %d, want >= 3", floor)
	}
}

func TestIntegSequenceSourcePgFallback(t *testing.T) {
	p := integPool(t)
	convID, _, _ := integConversationTwo(t, p)
	// A broken Redis client forces the PG single-row increment fallback.
	seqSrc := NewSequenceSource(p, redis.NewClient(&redis.Options{Addr: "localhost:1", DialTimeout: 300 * time.Millisecond}))
	ctx := context.Background()

	a, err := seqSrc.Next(ctx, convID)
	if err != nil {
		t.Fatalf("Next 1: %v", err)
	}
	b, err := seqSrc.Next(ctx, convID)
	if err != nil {
		t.Fatalf("Next 2: %v", err)
	}
	if a <= 0 || b <= a {
		t.Errorf("pg fallback sequences = %d, %d; want strictly increasing", a, b)
	}
}

// ---- BumpLastMessage monotonic guard (§5.1 out-of-order commits) ----

func TestIntegBumpLastMessageMonotonic(t *testing.T) {
	p := integPool(t)
	convID, owner, _ := integConversationTwo(t, p)
	repo := NewConversationRepo(p)
	begin := platformpg.NewBeginner(p)
	ctx := context.Background()

	// A transaction with seq=2 commits first (out-of-order commit vs seq=1).
	advanced := false
	runChat(t, begin, func(dbtx tx.Tx) error {
		var err error
		advanced, err = repo.BumpLastMessage(ctx, dbtx, convID, 2, strptr("two"), &owner, at(10))
		return err
	})
	if !advanced {
		t.Fatal("bump 2 was not applied")
	}
	// The seq=1 transaction lands later: the guard rejects the regress.
	advanced = true
	runChat(t, begin, func(dbtx tx.Tx) error {
		var err error
		advanced, err = repo.BumpLastMessage(ctx, dbtx, convID, 1, strptr("one"), &owner, at(9))
		return err
	})
	if advanced {
		t.Error("stale bump (seq 1 after seq 2) was applied")
	}

	c, err := repo.FindByID(ctx, convID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if c.LastMessageSeq == nil || *c.LastMessageSeq != 2 {
		t.Errorf("last_message_seq = %v, want 2 (never regresses)", c.LastMessageSeq)
	}
	if c.LastMessageSnippet == nil || *c.LastMessageSnippet != "two" {
		t.Errorf("snippet = %v, want two", c.LastMessageSnippet)
	}
}

// ---- application-level: sends, dedupe, ordering, stress (DATABASE.md §10/§11) ----

func TestIntegSendConcurrentStrictOrdering(t *testing.T) {
	p := integPool(t)
	rc := integRedis(t)
	convID, owner, member := integConversationTwo(t, p)
	svc := integChatService(t, p, rc)
	ctx := context.Background()
	if err := rc.Del(ctx, seqKey(convID)).Err(); err != nil {
		t.Fatalf("reset redis key: %v", err)
	}

	const n = 30
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sender := owner
			if i%2 == 0 {
				sender = member
			}
			text := fmt.Sprintf("msg-%d", i)
			_, err := svc.SendMessage(ctx, application.SendMessageCommand{
				UserID: sender, ConversationID: convID,
				ClientMsgID: fmt.Sprintf("cm-%d", i), Type: "text", Text: &text,
			})
			if err != nil {
				errs <- fmt.Errorf("send %d: %w", i, err)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent send failed: %v", err)
	}

	repo := NewMessageRepo(p)
	page, err := repo.ListByConversation(ctx, domain.MessageListQuery{ConversationID: convID, Limit: 200})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(page) != n {
		t.Fatalf("messages = %d, want %d", len(page), n)
	}
	seqs := seqs(page)
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	for i, s := range seqs {
		if s != int64(i+1) {
			t.Errorf("sequence %d = %d, want %d — gaps or duplicates", i, s, i+1)
		}
	}
	// global_seq (nextval at insert time) is unique across the page — the
	// delta-feed ordering is exercised separately by the keyset test.
	seenGlobal := map[int64]bool{}
	for _, m := range page {
		if seenGlobal[m.GlobalSeq] {
			t.Errorf("global_seq %d duplicated across the page", m.GlobalSeq)
		}
		seenGlobal[m.GlobalSeq] = true
	}

	// The conversation's last-message bump and the durable floor both reach n.
	c, err := NewConversationRepo(p).FindByID(ctx, convID)
	if err != nil {
		t.Fatalf("conversation: %v", err)
	}
	if c.LastMessageSeq == nil || *c.LastMessageSeq != n {
		t.Errorf("last_message_seq = %v, want %d", c.LastMessageSeq, n)
	}
	floor, err := NewSequenceSource(p, rc).Floor(ctx, convID)
	if err != nil || floor != n {
		t.Errorf("sequence floor = %d, %v; want %d", floor, err, n)
	}
}

func TestIntegSendConcurrentDedupeExactlyOnce(t *testing.T) {
	p := integPool(t)
	rc := integRedis(t)
	convID, owner, _ := integConversationTwo(t, p)
	svc := integChatService(t, p, rc)
	ctx := context.Background()
	if err := rc.Del(ctx, seqKey(convID)).Err(); err != nil {
		t.Fatalf("reset redis key: %v", err)
	}

	const n = 12
	var wg sync.WaitGroup
	created := int64(0)
	results := make(chan string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			text := "dup"
			res, err := svc.SendMessage(ctx, application.SendMessageCommand{
				UserID: owner, ConversationID: convID,
				ClientMsgID: "same-cm", Type: "text", Text: &text,
			})
			if err != nil {
				t.Errorf("send: %v", err)
				return
			}
			if res.Created {
				atomic.AddInt64(&created, 1)
			}
			results <- res.View.ID
		}()
	}
	wg.Wait()
	close(results)

	if created != 1 {
		t.Errorf("created = %d, want exactly 1", created)
	}
	var firstID string
	for id := range results {
		if firstID == "" {
			firstID = id
		} else if id != firstID {
			t.Errorf("replay returned a different id: %s vs %s", id, firstID)
		}
	}

	repo := NewMessageRepo(p)
	m, err := repo.FindByClientMsgID(ctx, platformpg.NewQuerier(p), owner, "same-cm")
	if err != nil {
		t.Fatalf("find original: %v", err)
	}
	if firstID != fmt.Sprintf("%d", m.ID) {
		t.Errorf("winner id %s != original id %d", firstID, m.ID)
	}
}

func TestIntegConcurrentSendsAndReceiptsNoDeadlock(t *testing.T) {
	p := integPool(t)
	rc := integRedis(t)
	convID, owner, member := integConversationTwo(t, p)
	svc := integChatService(t, p, rc)
	ctx := context.Background()
	if err := rc.Del(ctx, seqKey(convID)).Err(); err != nil {
		t.Fatalf("reset redis key: %v", err)
	}

	const n = 24
	var wg sync.WaitGroup
	errs := make(chan error, n*2)
	// Half the goroutines send messages; half race read receipts concurrently.
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			text := fmt.Sprintf("m-%d", i)
			if _, err := svc.SendMessage(ctx, application.SendMessageCommand{
				UserID: owner, ConversationID: convID,
				ClientMsgID: fmt.Sprintf("scm-%d", i), Type: "text", Text: &text,
			}); err != nil {
				errs <- fmt.Errorf("send %d: %w", i, err)
			}
		}(i)
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			seq := int64(i + 1)
			if _, err := svc.MarkRead(ctx, application.MarkReadCommand{
				UserID: member, ConversationID: convID, ReadSeq: seq,
			}); err != nil {
				errs <- fmt.Errorf("markread %d: %w", i, err)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		assertNotDeadlock(t, err)
		t.Fatalf("stress failed: %v", err)
	}

	// No conversation is blocked: the chat list is still queryable and the
	// final read cursor is the max of the receipts that advanced.
	c, err := NewConversationRepo(p).FindByID(ctx, convID)
	if err != nil {
		t.Fatalf("conversation after stress: %v", err)
	}
	if c.LastMessageSeq == nil || *c.LastMessageSeq < n {
		t.Errorf("last_message_seq = %v, want >= %d", c.LastMessageSeq, n)
	}
}

func TestIntegEditDeleteRace(t *testing.T) {
	p := integPool(t)
	rc := integRedis(t)
	convID, owner, _ := integConversationTwo(t, p)
	svc := integChatService(t, p, rc)
	ctx := context.Background()
	if err := rc.Del(ctx, seqKey(convID)).Err(); err != nil {
		t.Fatalf("reset redis key: %v", err)
	}

	text := "original"
	sent, err := svc.SendMessage(ctx, application.SendMessageCommand{
		UserID: owner, ConversationID: convID,
		ClientMsgID: "race-cm", Type: "text", Text: &text,
	})
	if err != nil {
		t.Fatalf("seed message: %v", err)
	}
	msgID := sent.View.ID

	msgInt, err := strconv.ParseInt(msgID, 10, 64)
	if err != nil {
		t.Fatalf("parse message id %q: %v", msgID, err)
	}

	var wg sync.WaitGroup
	var editErr, delErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, editErr = svc.EditMessage(ctx, application.EditMessageCommand{UserID: owner, MessageID: msgInt, NewText: "edited"})
	}()
	go func() {
		defer wg.Done()
		_, delErr = svc.DeleteMessage(ctx, application.DeleteMessageCommand{UserID: owner, MessageID: msgInt})
	}()
	wg.Wait()

	// An edit racing a delete may win, lose (ErrMessageDeleted), or see the
	// message already gone — but never corrupt state or deadlock.
	if editErr != nil && !errors.Is(editErr, domain.ErrMessageDeleted) && !errors.Is(editErr, domain.ErrMessageNotFound) {
		t.Fatalf("edit race error = %v", editErr)
	}
	if delErr != nil {
		assertNotDeadlock(t, delErr)
		t.Fatalf("delete race error = %v", delErr)
	}

	m, err := NewMessageRepo(p).FindByID(ctx, msgInt)
	if err != nil {
		t.Fatalf("find after race: %v", err)
	}
	if m.DeletedAt == nil {
		t.Error("message not tombstoned after delete race")
	}
	var edits int
	if err := p.QueryRow(ctx, `SELECT count(*) FROM message_edits WHERE message_id = $1`, msgInt).Scan(&edits); err != nil {
		t.Fatalf("edit count: %v", err)
	}
	if edits > 1 {
		t.Errorf("edit history rows = %d, want 0 or 1", edits)
	}
}

func TestIntegReactionConcurrentToggle(t *testing.T) {
	p := integPool(t)
	rc := integRedis(t)
	convID, owner, member := integConversationTwo(t, p)
	svc := integChatService(t, p, rc)
	ctx := context.Background()
	if err := rc.Del(ctx, seqKey(convID)).Err(); err != nil {
		t.Fatalf("reset redis key: %v", err)
	}

	text := "hi"
	sent, err := svc.SendMessage(ctx, application.SendMessageCommand{
		UserID: owner, ConversationID: convID,
		ClientMsgID: "rct-cm", Type: "text", Text: &text,
	})
	if err != nil {
		t.Fatalf("seed message: %v", err)
	}
	msgInt, err := strconv.ParseInt(sent.View.ID, 10, 64)
	if err != nil {
		t.Fatalf("parse message id %q: %v", sent.View.ID, err)
	}

	// Many concurrent identical adds must collapse to exactly one row.
	const n = 10
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := svc.AddReaction(ctx, application.ReactionCommand{UserID: member, MessageID: msgInt, Emoji: "👍"}); err != nil {
				t.Errorf("add reaction: %v", err)
			}
		}()
	}
	wg.Wait()

	cnt, err := NewReactionRepo(p).Count(ctx, msgInt, "👍")
	if err != nil || cnt != 1 {
		t.Errorf("reaction count = %d, %v; want exactly 1", cnt, err)
	}
}

// seqs extracts the sequence values from a page in page order.
func seqs(page []domain.Message) []int64 {
	out := make([]int64, len(page))
	for i, m := range page {
		out[i] = m.Sequence
	}
	return out
}

// assertNotDeadlock fails the test when err is a Postgres deadlock/serialization
// failure (SQLSTATE 40P01 / 40001) — the stress scenarios must never hit one.
func assertNotDeadlock(t *testing.T, err error) {
	t.Helper()
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == "40P01" || pgErr.Code == "40001") {
		t.Fatalf("deadlock/serialization failure: %v", err)
	}
}
