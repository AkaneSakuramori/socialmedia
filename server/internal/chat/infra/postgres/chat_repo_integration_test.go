//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/domain"
	platformpg "github.com/AkaneSakuramori/socialmedia/server/internal/platform/postgres"
	"github.com/AkaneSakuramori/socialmedia/server/pkg/tx"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Integration tests for the chat repositories (DATABASE.md §5.1, §5.2, §5.4,
// §7.1) against the dev compose stack. Run with:
//
//	APP_PG_DSN=$(...) go test -tags integration ./internal/chat/infra/postgres/
//	# or simply: make test-integration
//
// APP_PG_DSN is required; the tests skip when it is unset or the database is
// unreachable. All seeded rows use ids >= 7000000 (conversations) and
// >= 9000000 (users) so they never collide with real data, and every test
// removes its rows on cleanup.

var chatSeq int64

// TestMain wipes disposable integration rows from a previously killed run. The
// order matters: outbox/members/sequences reference conversations, which
// reference users, so children are removed first.
func TestMain(m *testing.M) {
	dsn := os.Getenv("APP_PG_DSN")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "APP_PG_DSN not set; skipping chat integration tests")
		os.Exit(0)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if p, err := platformpg.Open(ctx, dsn, 1); err == nil {
		_, _ = p.Exec(ctx, `DELETE FROM change_log WHERE conversation_id >= 7000000 OR actor_user_id >= 9000000`)
		_, _ = p.Exec(ctx, `DELETE FROM conversation_members WHERE conversation_id >= 7000000`)
		_, _ = p.Exec(ctx, `DELETE FROM conversation_sequences WHERE conversation_id >= 7000000`)
		_, _ = p.Exec(ctx, `DELETE FROM conversations WHERE id >= 7000000`)
		_, _ = p.Exec(ctx, `DELETE FROM users WHERE id >= 9000000`)
		p.Close()
	}
	os.Exit(m.Run())
}

// integPool opens a pool, skipping the tests when the database is unreachable.
func integPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("APP_PG_DSN")
	if dsn == "" {
		t.Skip("APP_PG_DSN not set; skipping chat integration tests")
	}
	p, err := platformpg.Open(context.Background(), dsn, 4)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(p.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := platformpg.Ping(ctx, p); err != nil {
		t.Skipf("postgres not reachable, skipping: %v", err)
	}
	return p
}

// integUser seeds a disposable account and returns its id.
func integUser(t *testing.T, p *pgxpool.Pool) int64 {
	t.Helper()
	chatSeq++
	id := int64(9000000 + chatSeq)
	ctx := context.Background()
	_, _ = p.Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
	if _, err := p.Exec(ctx, `INSERT INTO users (id, display_name) VALUES ($1, 'integration')`, id); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() { _, _ = p.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id) })
	return id
}

// integConversation seeds a conversation (created_by must exist) and returns
// its id; cleanup removes it and anything referencing it.
func integConversation(t *testing.T, p *pgxpool.Pool, typ domain.ConversationType, createdBy int64, title *string, at time.Time) int64 {
	t.Helper()
	chatSeq++
	id := int64(7000000 + chatSeq)
	repo := NewConversationRepo(p)
	begin := platformpg.NewBeginner(p)
	runChat(t, begin, func(dbtx tx.Tx) error {
		return repo.Create(context.Background(), dbtx, &domain.Conversation{
			ID:        id,
			Type:      typ,
			Title:     title,
			CreatedBy: createdBy,
			Settings:  domain.DefaultSettings(),
			CreatedAt: at,
			UpdatedAt: at,
		})
	})
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = p.Exec(ctx, `DELETE FROM change_log WHERE conversation_id = $1`, id)
		_, _ = p.Exec(ctx, `DELETE FROM conversation_members WHERE conversation_id = $1`, id)
		_, _ = p.Exec(ctx, `DELETE FROM conversation_sequences WHERE conversation_id = $1`, id)
		_, _ = p.Exec(ctx, `DELETE FROM conversations WHERE id = $1`, id)
	})
	return id
}

// integMembers seeds memberships for users with the given role and joined time.
func integMembers(t *testing.T, p *pgxpool.Pool, convID int64, role domain.Role, joinedAt time.Time, users ...int64) {
	t.Helper()
	ms := make([]*domain.Membership, 0, len(users))
	for _, u := range users {
		ms = append(ms, &domain.Membership{
			ConversationID: convID, UserID: u, Role: role, JoinedAt: joinedAt,
		})
	}
	repo := NewMembershipRepo(p)
	begin := platformpg.NewBeginner(p)
	runChat(t, begin, func(dbtx tx.Tx) error {
		return repo.AddMany(context.Background(), dbtx, ms)
	})
}

// beginTx opens a transaction and registers a rollback cleanup.
func beginTx(t *testing.T, begin tx.Beginner) tx.Tx {
	t.Helper()
	dbtx, err := begin.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dbtx.Rollback(context.Background()) })
	return dbtx
}

// commit commits the given transaction.
func commit(t *testing.T, dbtx tx.Tx) {
	t.Helper()
	if err := dbtx.Commit(context.Background()); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// runChat executes fn inside a transaction that is committed on success and
// rolled back on failure.
func runChat(t *testing.T, begin tx.Beginner, fn func(dbtx tx.Tx) error) {
	t.Helper()
	dbtx, err := begin.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer dbtx.Rollback(context.Background()) // no-op after a successful Commit
	if err := fn(dbtx); err != nil {
		t.Fatalf("run: %v", err)
	}
	if err := dbtx.Commit(context.Background()); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// at returns a deterministic timestamp offset by the given minutes.
func at(minutes int) time.Time {
	return time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC).Add(time.Duration(minutes) * time.Minute)
}

func TestIntegConversationCreateAndFind(t *testing.T) {
	p := integPool(t)
	repo := NewConversationRepo(p)
	owner := integUser(t, p)
	now := at(0)

	id := integConversation(t, p, domain.ConversationGroup, owner, strptr("Squad"), now)
	got, err := repo.FindByID(context.Background(), id)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if got.ID != id || got.Type != domain.ConversationGroup || got.Title == nil || *got.Title != "Squad" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if got.Settings.HistoryVisible != domain.HistoryVisibleAll {
		t.Errorf("settings default not applied: %+v", got.Settings)
	}
	if !got.CreatedAt.Equal(now) {
		t.Errorf("created_at = %v, want %v", got.CreatedAt, now)
	}

	if _, err := repo.FindByID(context.Background(), 999999999999); !errors.Is(err, domain.ErrConversationNotFound) {
		t.Errorf("missing id err = %v, want ErrConversationNotFound", err)
	}
}

func TestIntegConversationGroupRequiresTitle(t *testing.T) {
	p := integPool(t)
	repo := NewConversationRepo(p)
	owner := integUser(t, p)
	begin := platformpg.NewBeginner(p)

	// A group without a title violates DATABASE.md §5.1 CHECK and must fail.
	err := func() error {
		dbtx, err := begin.Begin(context.Background())
		if err != nil {
			return err
		}
		defer dbtx.Rollback(context.Background())
		return repo.Create(context.Background(), dbtx, &domain.Conversation{
			ID:        7000100,
			Type:      domain.ConversationGroup,
			CreatedBy: owner,
			Settings:  domain.DefaultSettings(),
			CreatedAt: at(0),
			UpdatedAt: at(0),
		})
	}()
	if err == nil {
		t.Error("group without title: expected CHECK violation, got nil")
	}
}

func TestIntegConversationFindDirectPair(t *testing.T) {
	p := integPool(t)
	repo := NewConversationRepo(p)
	uA, uB, uC, uD := integUser(t, p), integUser(t, p), integUser(t, p), integUser(t, p)
	now := at(0)

	directID := integConversation(t, p, domain.ConversationDirect, uA, nil, now)
	integMembers(t, p, directID, domain.RoleMember, now, uA, uB)

	// Either direction finds the pair.
	if got, err := repo.FindDirectPair(context.Background(), uA, uB); err != nil || got.ID != directID {
		t.Errorf("FindDirectPair(A,B) = %+v, %v; want id %d", got, err, directID)
	}
	if got, err := repo.FindDirectPair(context.Background(), uB, uA); err != nil || got.ID != directID {
		t.Errorf("FindDirectPair(B,A) = %+v, %v; want id %d", got, err, directID)
	}

	// A pair with no direct conversation must be not-found.
	if _, err := repo.FindDirectPair(context.Background(), uA, uC); !errors.Is(err, domain.ErrConversationNotFound) {
		t.Errorf("FindDirectPair(A,C) err = %v, want ErrConversationNotFound", err)
	}

	// A group containing a matching pair must never satisfy a direct lookup:
	// pair (A,D) exists only inside the group.
	groupID := integConversation(t, p, domain.ConversationGroup, uA, strptr("G"), now)
	integMembers(t, p, groupID, domain.RoleOwner, now, uA, uB, uD)
	if _, err := repo.FindDirectPair(context.Background(), uA, uD); !errors.Is(err, domain.ErrConversationNotFound) {
		t.Errorf("group matched a direct lookup, err = %v", err)
	}

	// A direct conversation with a third member must not match a 2-member
	// pair: pair (B,C) exists only inside the 3-member direct.
	threeID := integConversation(t, p, domain.ConversationDirect, uA, nil, now)
	integMembers(t, p, threeID, domain.RoleMember, now, uA, uB, uC)
	if _, err := repo.FindDirectPair(context.Background(), uB, uC); !errors.Is(err, domain.ErrConversationNotFound) {
		t.Errorf("3-member direct matched a 2-member pair, err = %v", err)
	}
}

func TestIntegConversationUpdateAndTombstone(t *testing.T) {
	p := integPool(t)
	repo := NewConversationRepo(p)
	owner := integUser(t, p)
	now := at(0)
	begin := platformpg.NewBeginner(p)

	id := integConversation(t, p, domain.ConversationGroup, owner, strptr("Before"), now)
	msgAt := now.Add(10 * time.Minute)
	msgSeq := int64(3)
	sender := integUser(t, p)

	runChat(t, begin, func(dbtx tx.Tx) error {
		got, err := repo.FindByID(context.Background(), id)
		if err != nil {
			return err
		}
		got.Title = strptr("After")
		got.LastMessageAt = &msgAt
		got.LastMessageSeq = &msgSeq
		got.LastMessageSnippet = strptr("hi")
		got.LastSenderID = &sender
		got.UpdatedAt = msgAt
		return repo.Update(context.Background(), dbtx, got)
	})

	got, err := repo.FindByID(context.Background(), id)
	if err != nil {
		t.Fatalf("FindByID after update: %v", err)
	}
	if got.Title == nil || *got.Title != "After" || got.LastMessageSeq == nil || *got.LastMessageSeq != 3 {
		t.Errorf("update not persisted: %+v", got)
	}
	if got.LastMessageSnippet == nil || *got.LastMessageSnippet != "hi" {
		t.Errorf("last_message_snippet not persisted: %+v", got)
	}

	// Tombstone removes it from reads.
	runChat(t, begin, func(dbtx tx.Tx) error {
		return repo.Tombstone(context.Background(), dbtx, id, now.Add(time.Hour))
	})
	if _, err := repo.FindByID(context.Background(), id); !errors.Is(err, domain.ErrConversationNotFound) {
		t.Errorf("tombstoned conversation still readable, err = %v", err)
	}
}

func TestIntegConversationListOrderingAndPagination(t *testing.T) {
	p := integPool(t)
	repo := NewConversationRepo(p)
	u := integUser(t, p)
	begin := platformpg.NewBeginner(p)

	// Three conversations for u, newest activity first. c1 has a message (newer
	// than c2/c3), c2 and c3 have none and sort by created_at.
	c1 := integConversation(t, p, domain.ConversationGroup, u, strptr("c1"), at(0))
	c2 := integConversation(t, p, domain.ConversationGroup, u, strptr("c2"), at(1))
	c3 := integConversation(t, p, domain.ConversationGroup, u, strptr("c3"), at(2))
	integMembers(t, p, c1, domain.RoleOwner, at(0), u)
	integMembers(t, p, c2, domain.RoleOwner, at(1), u)
	integMembers(t, p, c3, domain.RoleOwner, at(2), u)

	msgAt := at(3)
	runChat(t, begin, func(dbtx tx.Tx) error {
		got, err := repo.FindByID(context.Background(), c1)
		if err != nil {
			return err
		}
		got.LastMessageAt = &msgAt
		got.LastMessageSeq = ptr64(1)
		got.UpdatedAt = msgAt
		return repo.Update(context.Background(), dbtx, got)
	})

	rows, err := repo.List(context.Background(), domain.ConversationListQuery{UserID: u, Limit: 4})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("List returned %d rows, want 3", len(rows))
	}
	// c1 has a message (at(3)); c3 and c2 have none and sort by created_at,
	// so c3 (at(2)) precedes c2 (at(1)).
	wantOrder := []int64{c1, c3, c2}
	for i, w := range wantOrder {
		if rows[i].ID != w {
			t.Errorf("rows[%d].ID = %d, want %d (activity order)", i, rows[i].ID, w)
		}
	}

	// Keyset pagination: page of 1 (limit=2 for the +1 fetch) stops after c1;
	// the second page (cursor at c1) returns c3, c2.
	page1, err := repo.List(context.Background(), domain.ConversationListQuery{UserID: u, Limit: 2})
	if err != nil {
		t.Fatalf("List page1: %v", err)
	}
	if len(page1) != 2 || page1[0].ID != c1 || page1[1].ID != c3 {
		t.Fatalf("page1 = %d,%d; want c1,c3", page1[0].ID, page1[1].ID)
	}
	last := page1[len(page1)-1]
	page2, err := repo.List(context.Background(), domain.ConversationListQuery{
		UserID: u,
		Limit:  2,
		Cursor: &domain.ConversationCursor{Activity: last.LastActivity(), ID: last.ID},
	})
	if err != nil {
		t.Fatalf("List page2: %v", err)
	}
	if len(page2) != 1 || page2[0].ID != c2 {
		t.Fatalf("page2 = %+v, want [c2]", page2)
	}
}

func TestIntegConversationListFilters(t *testing.T) {
	p := integPool(t)
	repo := NewConversationRepo(p)
	u := integUser(t, p)
	begin := platformpg.NewBeginner(p)

	direct := integConversation(t, p, domain.ConversationDirect, u, nil, at(0))
	group := integConversation(t, p, domain.ConversationGroup, u, strptr("G"), at(1))
	integMembers(t, p, direct, domain.RoleMember, at(0), u)
	integMembers(t, p, group, domain.RoleOwner, at(1), u)

	// Pin + archive + unread state via the membership repo.
	memRepo := NewMembershipRepo(p)
	runChat(t, begin, func(dbtx tx.Tx) error {
		m, err := memRepo.FindActive(context.Background(), group, u)
		if err != nil {
			return err
		}
		pin := at(5)
		m.PinnedAt = &pin
		return memRepo.Update(context.Background(), dbtx, m)
	})
	runChat(t, begin, func(dbtx tx.Tx) error {
		m, err := memRepo.FindActive(context.Background(), direct, u)
		if err != nil {
			return err
		}
		arch := at(6)
		m.ArchivedAt = &arch
		return memRepo.Update(context.Background(), dbtx, m)
	})

	listIDs := func(q domain.ConversationListQuery) map[int64]bool {
		t.Helper()
		rows, err := repo.List(context.Background(), q)
		if err != nil {
			t.Fatalf("List(%+v): %v", q, err)
		}
		out := map[int64]bool{}
		for _, r := range rows {
			out[r.ID] = true
		}
		return out
	}

	if got := listIDs(domain.ConversationListQuery{UserID: u, Limit: 10, Filter: "direct"}); len(got) != 1 || !got[direct] {
		t.Errorf("direct filter = %v", got)
	}
	if got := listIDs(domain.ConversationListQuery{UserID: u, Limit: 10, Filter: "groups"}); len(got) != 1 || !got[group] {
		t.Errorf("groups filter = %v", got)
	}
	if got := listIDs(domain.ConversationListQuery{UserID: u, Limit: 10, Filter: "pinned"}); len(got) != 1 || !got[group] {
		t.Errorf("pinned filter = %v", got)
	}
	if got := listIDs(domain.ConversationListQuery{UserID: u, Limit: 10, Filter: "archived"}); len(got) != 1 || !got[direct] {
		t.Errorf("archived filter = %v", got)
	}

	// Unread: group has no messages yet, so nothing is unread.
	if got := listIDs(domain.ConversationListQuery{UserID: u, Limit: 10, UnreadOnly: true}); len(got) != 0 {
		t.Errorf("unread filter with no messages = %v, want empty", got)
	}

	// Add a message to group → it becomes unread for u (last_read_seq = 0).
	msgAt := at(7)
	runChat(t, begin, func(dbtx tx.Tx) error {
		c, err := repo.FindByID(context.Background(), group)
		if err != nil {
			return err
		}
		c.LastMessageAt = &msgAt
		c.LastMessageSeq = ptr64(1)
		c.UpdatedAt = msgAt
		return repo.Update(context.Background(), dbtx, c)
	})
	if got := listIDs(domain.ConversationListQuery{UserID: u, Limit: 10, UnreadOnly: true}); len(got) != 1 || !got[group] {
		t.Errorf("unread filter after message = %v, want {group}", got)
	}

	// Advancing the read cursor makes it read again.
	runChat(t, begin, func(dbtx tx.Tx) error {
		m, err := memRepo.FindActive(context.Background(), group, u)
		if err != nil {
			return err
		}
		m.LastReadSeq = 1
		m.LastDeliveredSeq = 1
		return memRepo.Update(context.Background(), dbtx, m)
	})
	if got := listIDs(domain.ConversationListQuery{UserID: u, Limit: 10, UnreadOnly: true}); len(got) != 0 {
		t.Errorf("unread filter after read cursor = %v, want empty", got)
	}
}

func TestIntegMembershipLifecycle(t *testing.T) {
	p := integPool(t)
	repo := NewMembershipRepo(p)
	begin := platformpg.NewBeginner(p)
	owner := integUser(t, p)
	u2, u3 := integUser(t, p), integUser(t, p)
	now := at(0)

	convID := integConversation(t, p, domain.ConversationGroup, owner, strptr("G"), now)
	integMembers(t, p, convID, domain.RoleOwner, now, owner, u2, u3)

	if n, err := repo.CountActive(context.Background(), convID); err != nil || n != 3 {
		t.Errorf("CountActive = %d, %v; want 3", n, err)
	}

	ids, err := repo.ActiveUserIDs(context.Background(), convID)
	if err != nil || len(ids) != 3 {
		t.Errorf("ActiveUserIDs = %v, %v; want 3 users", ids, err)
	}

	m, err := repo.FindActive(context.Background(), convID, u2)
	if err != nil || m.Role != domain.RoleOwner || m.LeftAt != nil {
		t.Errorf("FindActive = %+v, %v; want owner active", m, err)
	}

	if _, err := repo.FindActive(context.Background(), convID, 999999999999); !errors.Is(err, domain.ErrMembershipNotFound) {
		t.Errorf("FindActive missing = %v, want ErrMembershipNotFound", err)
	}

	// Remove u3 → count drops and the user vanishes from the active set.
	runChat(t, begin, func(dbtx tx.Tx) error {
		return repo.Remove(context.Background(), dbtx, convID, u3, now.Add(time.Minute))
	})
	if n, _ := repo.CountActive(context.Background(), convID); n != 2 {
		t.Errorf("CountActive after remove = %d, want 2", n)
	}
	if _, err := repo.FindActive(context.Background(), convID, u3); !errors.Is(err, domain.ErrMembershipNotFound) {
		t.Errorf("removed member still active: %v", err)
	}

	// Re-adding resurrects the row (ON CONFLICT clears left_at).
	runChat(t, begin, func(dbtx tx.Tx) error {
		return repo.AddMany(context.Background(), dbtx,
			[]*domain.Membership{{ConversationID: convID, UserID: u3, Role: domain.RoleMember, JoinedAt: now}})
	})
	if m, err := repo.FindActive(context.Background(), convID, u3); err != nil || m.LeftAt != nil {
		t.Errorf("re-add failed: %+v, %v", m, err)
	}
}

func TestIntegMembershipUpdatePrefs(t *testing.T) {
	p := integPool(t)
	repo := NewMembershipRepo(p)
	begin := platformpg.NewBeginner(p)
	u := integUser(t, p)
	now := at(0)

	convID := integConversation(t, p, domain.ConversationGroup, u, strptr("G"), now)
	integMembers(t, p, convID, domain.RoleMember, now, u)

	runChat(t, begin, func(dbtx tx.Tx) error {
		m, err := repo.FindActive(context.Background(), convID, u)
		if err != nil {
			return err
		}
		m.Role = domain.RoleAdmin
		mute := at(60)
		pin := at(61)
		m.MutedUntil = &mute
		m.PinnedAt = &pin
		return repo.Update(context.Background(), dbtx, m)
	})

	m, err := repo.FindActive(context.Background(), convID, u)
	if err != nil {
		t.Fatalf("FindActive after update: %v", err)
	}
	if m.Role != domain.RoleAdmin {
		t.Errorf("role = %s, want admin", m.Role)
	}
	if m.MutedUntil == nil || !m.MutedUntil.Equal(at(60)) || m.PinnedAt == nil || !m.PinnedAt.Equal(at(61)) {
		t.Errorf("prefs not persisted: %+v", m)
	}
}

func TestIntegListMembers(t *testing.T) {
	p := integPool(t)
	repo := NewMembershipRepo(p)
	u := integUser(t, p)
	now := at(0)

	convID := integConversation(t, p, domain.ConversationGroup, u, strptr("G"), now)
	// Distinct joined times so the keyset order is deterministic.
	integMembers(t, p, convID, domain.RoleOwner, now, u)
	integMembers(t, p, convID, domain.RoleMember, now.Add(time.Minute), integUser(t, p))
	integMembers(t, p, convID, domain.RoleMember, now.Add(2*time.Minute), integUser(t, p))

	rows, err := repo.ListMembers(context.Background(), convID, domain.MemberListQuery{Limit: 10})
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("ListMembers = %d rows, want 3", len(rows))
	}
	// Newest joined first.
	if rows[0].JoinedAt.Before(rows[1].JoinedAt) || rows[1].JoinedAt.Before(rows[2].JoinedAt) {
		t.Errorf("members not ordered joined_at DESC: %+v", rows)
	}

	// Display-name substring filter (all names are "integration").
	filtered, err := repo.ListMembers(context.Background(), convID, domain.MemberListQuery{Limit: 10, Q: "integ"})
	if err != nil || len(filtered) != 3 {
		t.Errorf("q filter = %d rows, %v; want 3", len(filtered), err)
	}
	exact, err := repo.ListMembers(context.Background(), convID, domain.MemberListQuery{Limit: 10, Q: "zzz-none"})
	if err != nil || len(exact) != 0 {
		t.Errorf("q filter none = %d rows, %v; want 0", len(exact), err)
	}

	// Cursor pagination: page of 1 (limit=2 +1) then the rest.
	page1, err := repo.ListMembers(context.Background(), convID, domain.MemberListQuery{Limit: 2})
	if err != nil || len(page1) != 2 {
		t.Fatalf("members page1 = %d rows, %v; want 2", len(page1), err)
	}
	last := page1[len(page1)-1]
	page2, err := repo.ListMembers(context.Background(), convID, domain.MemberListQuery{
		Limit:  2,
		Cursor: &domain.MemberCursor{JoinedAt: last.JoinedAt, UserID: last.UserID},
	})
	if err != nil || len(page2) != 1 {
		t.Fatalf("members page2 = %d rows, %v; want 1", len(page2), err)
	}
}

func TestIntegSequenceInit(t *testing.T) {
	p := integPool(t)
	repo := NewSequenceRepo()
	begin := platformpg.NewBeginner(p)
	u := integUser(t, p)
	now := at(0)

	convID := integConversation(t, p, domain.ConversationGroup, u, strptr("G"), now)

	runChat(t, begin, func(dbtx tx.Tx) error {
		return repo.Init(context.Background(), dbtx, convID)
	})
	// Idempotent: a second init must not error.
	runChat(t, begin, func(dbtx tx.Tx) error {
		return repo.Init(context.Background(), dbtx, convID)
	})

	var seq int64
	if err := p.QueryRow(context.Background(),
		`SELECT last_sequence FROM conversation_sequences WHERE conversation_id = $1`, convID).Scan(&seq); err != nil {
		t.Fatalf("read sequence: %v", err)
	}
	if seq != 0 {
		t.Errorf("last_sequence = %d, want 0", seq)
	}
}

func TestIntegChangeLogAppend(t *testing.T) {
	p := integPool(t)
	repo := NewChangeLogRepo(p)
	begin := platformpg.NewBeginner(p)
	u := integUser(t, p)
	convID := integConversation(t, p, domain.ConversationGroup, u, strptr("G"), at(0))

	// Empty append is a no-op.
	if err := runChatErr(begin, func(dbtx tx.Tx) error {
		return repo.Append(context.Background(), dbtx, nil)
	}); err != nil {
		t.Fatalf("append nil: %v", err)
	}

	convIDp := convID
	entry := domain.ChangeLogEntry{
		EventType:       "conversation.created",
		ConversationID:  &convIDp,
		EntityID:        &convIDp,
		ActorUserID:     &u,
		AffectedUserIDs: []int64{u},
		Payload:         []byte(`{"type":"group"}`),
	}
	if err := runChatErr(begin, func(dbtx tx.Tx) error {
		return repo.Append(context.Background(), dbtx, []domain.ChangeLogEntry{entry})
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	var globalSeq int64
	var eventType string
	var payload []byte
	if err := p.QueryRow(context.Background(),
		`SELECT global_seq, event_type, payload FROM change_log
		 WHERE conversation_id = $1 ORDER BY global_seq DESC LIMIT 1`, convID).
		Scan(&globalSeq, &eventType, &payload); err != nil {
		t.Fatalf("read change_log: %v", err)
	}
	if eventType != "conversation.created" {
		t.Errorf("entry type = %s, want conversation.created (seq=%d)", eventType, globalSeq)
	}
	// JSONB normalizes whitespace, so compare semantically.
	var got, want map[string]any
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if err := json.Unmarshal([]byte(`{"type":"group"}`), &want); err != nil {
		t.Fatal(err)
	}
	if got["type"] != want["type"] {
		t.Errorf("payload = %s, want {\"type\":\"group\"}", payload)
	}

	// A bad event type must be rejected by the CHECK constraint.
	bad := domain.ChangeLogEntry{EventType: "bogus.event"}
	if err := runChatErr(begin, func(dbtx tx.Tx) error {
		return repo.Append(context.Background(), dbtx, []domain.ChangeLogEntry{bad})
	}); err == nil {
		t.Error("invalid event_type accepted")
	}
}

// runChatErr runs fn in a transaction, committing on success and returning the
// error (no t.Fatal); a failed fn is rolled back.
func runChatErr(begin tx.Beginner, fn func(dbtx tx.Tx) error) error {
	dbtx, err := begin.Begin(context.Background())
	if err != nil {
		return err
	}
	defer dbtx.Rollback(context.Background()) // no-op after a successful Commit
	if err := fn(dbtx); err != nil {
		return err
	}
	return dbtx.Commit(context.Background())
}

// strptr returns a pointer to s.
func strptr(s string) *string { return &s }

// ptr64 returns a pointer to n.
func ptr64(n int64) *int64 { return &n }
