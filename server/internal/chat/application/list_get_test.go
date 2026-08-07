package application

import (
	"errors"
	"testing"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/domain"
)

// seedConversation inserts a conversation + membership directly into the fakes
// (bypassing the create use-case) so list ordering can be controlled.
func (h *harness) seedConversation(id, userID int64, ctype domain.ConversationType, title *string, lastMessageAt *time.Time) *domain.Conversation {
	c := &domain.Conversation{
		ID:            id,
		Type:          ctype,
		Title:         title,
		CreatedBy:     userID,
		Settings:      domain.DefaultSettings(),
		CreatedAt:     h.now,
		UpdatedAt:     h.now,
		LastMessageAt: lastMessageAt,
	}
	if lastMessageAt != nil {
		seq := int64(1)
		c.LastMessageSeq = &seq
	}
	h.convos.byID[id] = c
	h.members.rows[id] = map[int64]*domain.Membership{
		userID: newMembership(id, userID, domain.RoleOwner, h.now),
	}
	return c
}

// ---- GET /v1/conversations (§7.1) ----

func TestListConversationsEmpty(t *testing.T) {
	h := newHarness(t)
	res, err := h.svc.ListConversations(t.Context(), ListConversationsCommand{UserID: 1001})
	if err != nil {
		t.Fatalf("ListConversations error: %v", err)
	}
	if len(res.Items) != 0 || res.HasMore || res.Next != nil {
		t.Errorf("got %d items (has_more=%v next=%v), want empty", len(res.Items), res.HasMore, res.Next)
	}
	if res.Limit != defaultLimit {
		t.Errorf("limit = %d, want default %d", res.Limit, defaultLimit)
	}
}

func TestListOrdersByLastActivityDesc(t *testing.T) {
	h := newHarness(t)
	t1 := h.now.Add(-10 * time.Minute) // oldest activity
	t2 := h.now.Add(-5 * time.Minute)
	t3 := h.now.Add(-2 * time.Minute) // newest activity

	// Newer activity must sort first: id=3 has the newest last message.
	h.seedConversation(1, 1001, domain.ConversationGroup, strptr("a"), &t1)
	h.seedConversation(2, 1001, domain.ConversationGroup, strptr("b"), &t2)
	h.seedConversation(3, 1001, domain.ConversationDirect, nil, &t3)

	res, err := h.svc.ListConversations(t.Context(), ListConversationsCommand{UserID: 1001})
	if err != nil {
		t.Fatalf("ListConversations error: %v", err)
	}
	if len(res.Items) != 3 {
		t.Fatalf("items = %d, want 3", len(res.Items))
	}
	wantOrder := []string{"3", "2", "1"}
	for i, w := range wantOrder {
		if res.Items[i].ID != w {
			t.Errorf("item[%d].id = %s, want %s", i, res.Items[i].ID, w)
		}
	}
	// No next cursor when the page is complete.
	if res.HasMore || res.Next != nil {
		t.Errorf("has_more=%v next=%v, want false/nil", res.HasMore, res.Next)
	}
}

func TestListNoMessagesSortsByCreatedAt(t *testing.T) {
	h := newHarness(t)
	// Conversations without messages sort by created_at (newest first), with
	// id as the tiebreaker when created times collide.
	c1 := h.seedConversation(1, 1001, domain.ConversationGroup, strptr("a"), nil)
	c1.CreatedAt = h.now.Add(-30 * time.Minute)
	c2 := h.seedConversation(2, 1001, domain.ConversationGroup, strptr("b"), nil)
	c2.CreatedAt = h.now.Add(-10 * time.Minute)
	c3 := h.seedConversation(3, 1001, domain.ConversationGroup, strptr("c"), nil)
	c3.CreatedAt = h.now.Add(-10 * time.Minute) // ties with c2 → id desc wins

	res, err := h.svc.ListConversations(t.Context(), ListConversationsCommand{UserID: 1001})
	if err != nil {
		t.Fatalf("ListConversations error: %v", err)
	}
	wantOrder := []string{"3", "2", "1"}
	for i, w := range wantOrder {
		if res.Items[i].ID != w {
			t.Errorf("item[%d].id = %s, want %s", i, res.Items[i].ID, w)
		}
	}
}

func TestListPaginationCursor(t *testing.T) {
	h := newHarness(t)
	// Six conversations, page size 2 → three pages.
	for i := 1; i <= 6; i++ {
		id := int64(i)
		at := h.now.Add(-time.Duration(i) * time.Minute)
		h.seedConversation(id, 1001, domain.ConversationGroup, strptr("g"), &at)
	}

	var got []string
	cursor := ""
	for {
		res, err := h.svc.ListConversations(t.Context(), ListConversationsCommand{
			UserID: 1001, Limit: 2, Cursor: cursor,
		})
		if err != nil {
			t.Fatalf("ListConversations error: %v", err)
		}
		for _, it := range res.Items {
			got = append(got, it.ID)
		}
		if res.Next == nil {
			if res.HasMore {
				t.Fatal("has_more without a next cursor")
			}
			break
		}
		if !res.HasMore {
			t.Fatal("next cursor without has_more")
		}
		cursor = *res.Next
	}
	want := []string{"1", "2", "3", "4", "5", "6"}
	if len(got) != len(want) {
		t.Fatalf("collected %d ids %v, want %v", len(got), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("page order %v, want %v", got, want)
			break
		}
	}
}

func TestListFilters(t *testing.T) {
	h := newHarness(t)
	at := h.now.Add(-time.Minute)
	h.seedConversation(1, 1001, domain.ConversationGroup, strptr("g"), &at)
	h.seedConversation(2, 1001, domain.ConversationDirect, nil, nil)
	h.members.rows[2][1002] = newMembership(2, 1002, domain.RoleMember, h.now)

	// pinned
	h.members.rows[1][1001].PinnedAt = &h.now
	res, _ := h.svc.ListConversations(t.Context(), ListConversationsCommand{UserID: 1001, Filter: "pinned"})
	if len(res.Items) != 1 || res.Items[0].ID != "1" {
		t.Errorf("pinned = %v, want [1]", res.Items)
	}
	if !res.Items[0].IsPinned {
		t.Error("pinned item must be IsPinned")
	}

	// archived
	h.members.rows[1][1001].PinnedAt = nil
	h.members.rows[1][1001].ArchivedAt = &h.now
	res, _ = h.svc.ListConversations(t.Context(), ListConversationsCommand{UserID: 1001, Filter: "archived"})
	if len(res.Items) != 1 || res.Items[0].ID != "1" {
		t.Errorf("archived = %v, want [1]", res.Items)
	}
	if !res.Items[0].IsArchived {
		t.Error("archived item must be IsArchived")
	}

	// groups / direct
	h.members.rows[1][1001].ArchivedAt = nil
	res, _ = h.svc.ListConversations(t.Context(), ListConversationsCommand{UserID: 1001, Filter: "groups"})
	if len(res.Items) != 1 || res.Items[0].ID != "1" {
		t.Errorf("groups = %v, want [1]", res.Items)
	}
	res, _ = h.svc.ListConversations(t.Context(), ListConversationsCommand{UserID: 1001, Filter: "direct"})
	if len(res.Items) != 1 || res.Items[0].ID != "2" {
		t.Errorf("direct = %v, want [2]", res.Items)
	}

	// unread_only: group has last_message_seq=1, read 0 → unread.
	res, _ = h.svc.ListConversations(t.Context(), ListConversationsCommand{UserID: 1001, UnreadOnly: true})
	if len(res.Items) != 1 || res.Items[0].ID != "1" {
		t.Errorf("unread_only = %v, want [1]", res.Items)
	}
	if res.Items[0].UnreadCount != 1 {
		t.Errorf("unread_count = %d, want 1", res.Items[0].UnreadCount)
	}
}

func TestListInvalidFilter(t *testing.T) {
	h := newHarness(t)
	_, err := h.svc.ListConversations(t.Context(), ListConversationsCommand{UserID: 1001, Filter: "bogus"})
	ve, ok := err.(*domain.ValidationError)
	if !ok || ve.Field != "filter" {
		t.Errorf("err = %v, want filter validation", err)
	}
}

// ---- GET /v1/conversations/{id} (§7.3) ----

func TestGetConversationDetail(t *testing.T) {
	h := newHarness(t)
	h.users.seed(1001, "Aya")
	h.users.seed(1002, "Sami")
	h.users.seed(1003, "Ravi")
	c := h.seedConversation(1, 1001, domain.ConversationGroup, strptr("Trip"), nil)
	h.members.rows[1][1002] = newMembership(1, 1002, domain.RoleMember, h.now.Add(time.Minute))
	h.members.rows[1][1003] = newMembership(1, 1003, domain.RoleMember, h.now.Add(2*time.Minute))

	detail, err := h.svc.GetConversation(t.Context(), GetConversationCommand{UserID: 1001, ConversationID: c.ID})
	if err != nil {
		t.Fatalf("GetConversation error: %v", err)
	}
	if detail.OwnerID != "1001" {
		t.Errorf("owner_id = %s, want 1001", detail.OwnerID)
	}
	if detail.MemberCount != 3 {
		t.Errorf("member_count = %d, want 3", detail.MemberCount)
	}
	if detail.Membership.Role != string(domain.RoleOwner) {
		t.Errorf("membership.role = %s, want owner", detail.Membership.Role)
	}
	if len(detail.MemberPreview) != 3 {
		t.Errorf("member_preview len = %d, want 3", len(detail.MemberPreview))
	}
	if detail.Settings.HistoryVisible != "all" {
		t.Errorf("history_visible = %s, want all", detail.Settings.HistoryVisible)
	}
}

func TestGetConversationNotMember(t *testing.T) {
	h := newHarness(t)
	h.seedConversation(1, 1001, domain.ConversationGroup, strptr("g"), nil)
	_, err := h.svc.GetConversation(t.Context(), GetConversationCommand{UserID: 9999, ConversationID: 1})
	if !errors.Is(err, domain.ErrNotMember) {
		t.Errorf("err = %v, want ErrNotMember", err)
	}
}

func TestGetConversationNotFound(t *testing.T) {
	h := newHarness(t)
	_, err := h.svc.GetConversation(t.Context(), GetConversationCommand{UserID: 1001, ConversationID: 424242})
	if !errors.Is(err, domain.ErrConversationNotFound) {
		t.Errorf("err = %v, want ErrConversationNotFound", err)
	}
}

func strptr(s string) *string { return &s }
