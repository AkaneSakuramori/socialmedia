package application

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/domain"
)

// seedMessage inserts a message directly into the fake repo (bypassing the
// send use-case) so history ordering can be controlled precisely.
func (h *harness) seedMessage(convID, seq, globalSeq, senderID int64, text string) domain.Message {
	now := h.now.Add(time.Duration(seq) * time.Minute)
	sid := senderID
	id, _ := h.ids.NextID()
	m := domain.Message{
		ID:             id,
		ConversationID: convID,
		Sequence:       seq,
		GlobalSeq:      globalSeq,
		SenderID:       &sid,
		Type:           domain.MessageTypeText,
		Content:        &text,
		CreatedAt:      now,
	}
	h.messages.byID[m.ID] = &m
	return m
}

func TestDecodeMessageCursor(t *testing.T) {
	before, after, err := decodeMessageCursor("s:5")
	if err != nil || before == nil || *before != 5 || after != nil {
		t.Errorf("s:5 → %v %v %v, want before=5", before, after, err)
	}
	before, after, err = decodeMessageCursor("g:10")
	if err != nil || after == nil || *after != 10 || before != nil {
		t.Errorf("g:10 → %v %v %v, want after=10", before, after, err)
	}

	invalid := []string{"x:5", "s:abc", "s:-1", "s:0", "bogus", "s:1:2"}
	for _, in := range invalid {
		if b, a, err := decodeMessageCursor(in); err == nil {
			t.Errorf("decode(%q) = %v %v, want ErrInvalidCursor", in, b, a)
		}
	}
}

func TestListMessagesNewestPageAscending(t *testing.T) {
	h := newHarness(t)
	h.seedChat(1, 1001, 1002)
	for seq := int64(1); seq <= 5; seq++ {
		h.seedMessage(1, seq, seq, 1001, "m")
	}

	res, err := h.svc.ListMessages(t.Context(), ListMessagesCommand{UserID: 1001, ConversationID: 1})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if res.Limit != defaultLimit || res.HasMore {
		t.Errorf("limit=%d has_more=%v, want %d/false", res.Limit, res.HasMore, defaultLimit)
	}
	if len(res.Items) != 5 {
		t.Fatalf("items = %d, want 5", len(res.Items))
	}
	// Newest page is returned in ascending sequence order (strict ordering).
	for i, it := range res.Items {
		if it.Sequence != string(rune('0'+i+1)) {
			t.Errorf("item[%d].sequence = %s, want %d", i, it.Sequence, i+1)
		}
	}
}

func TestListMessagesScrollBack(t *testing.T) {
	h := newHarness(t)
	h.seedChat(1, 1001, 1002)
	for seq := int64(1); seq <= 6; seq++ {
		h.seedMessage(1, seq, seq, 1001, "m")
	}

	// First page (limit 2, no cursor) returns the two newest, ascending.
	first, err := h.svc.ListMessages(t.Context(), ListMessagesCommand{UserID: 1001, ConversationID: 1, Limit: 2})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if !first.HasMore || first.Next == nil {
		t.Fatalf("first page has_more=%v next=%v, want true/non-nil", first.HasMore, first.Next)
	}
	if got := []string{first.Items[0].Sequence, first.Items[1].Sequence}; got[0] != "5" || got[1] != "6" {
		t.Errorf("first page sequences = %v, want [5 6]", got)
	}

	// Scroll back with the returned cursor: the next two older messages.
	second, err := h.svc.ListMessages(t.Context(), ListMessagesCommand{UserID: 1001, ConversationID: 1, Limit: 2, Cursor: *first.Next})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if !second.HasMore || second.Next == nil {
		t.Fatalf("second page has_more=%v next=%v, want true/non-nil", second.HasMore, second.Next)
	}
	if got := []string{second.Items[0].Sequence, second.Items[1].Sequence}; got[0] != "3" || got[1] != "4" {
		t.Errorf("second page sequences = %v, want [3 4]", got)
	}

	// Walk to the oldest page; it has no next cursor.
	third, err := h.svc.ListMessages(t.Context(), ListMessagesCommand{UserID: 1001, ConversationID: 1, Limit: 2, Cursor: *second.Next})
	if err != nil {
		t.Fatalf("third page: %v", err)
	}
	if third.HasMore || third.Next != nil {
		t.Fatalf("third page has_more=%v next=%v, want false/nil", third.HasMore, third.Next)
	}
	if got := []string{third.Items[0].Sequence, third.Items[1].Sequence}; got[0] != "1" || got[1] != "2" {
		t.Errorf("third page sequences = %v, want [1 2]", got)
	}
}

func TestListMessagesDeltaPollAscending(t *testing.T) {
	h := newHarness(t)
	h.seedChat(1, 1001, 1002)
	// Global seqs are dense here; poll strictly after the given cursor.
	for seq := int64(1); seq <= 4; seq++ {
		h.seedMessage(1, seq, seq, 1001, "m")
	}

	res, err := h.svc.ListMessages(t.Context(), ListMessagesCommand{
		UserID: 1001, ConversationID: 1, Limit: 2, AfterGlobalSeq: &[]int64{0}[0],
	})
	if err != nil {
		t.Fatalf("delta poll: %v", err)
	}
	if !res.HasMore || res.Next == nil {
		t.Fatalf("has_more=%v next=%v, want true/non-nil", res.HasMore, res.Next)
	}
	if got := []string{res.Items[0].GlobalSeq, res.Items[1].GlobalSeq}; got[0] != "1" || got[1] != "2" {
		t.Errorf("delta items = %v, want [1 2]", got)
	}

	// Continue with the returned cursor.
	res2, err := h.svc.ListMessages(t.Context(), ListMessagesCommand{
		UserID: 1001, ConversationID: 1, Limit: 2, Cursor: *res.Next,
	})
	if err != nil {
		t.Fatalf("delta poll 2: %v", err)
	}
	// The final delta page reports no more items, but still carries a watermark
	// cursor; a subsequent poll past it returns nothing.
	if res2.HasMore {
		t.Fatalf("delta poll 2 has_more=true, want false")
	}
	if len(res2.Items) != 2 || res2.Items[0].GlobalSeq != "3" || res2.Items[1].GlobalSeq != "4" {
		t.Errorf("delta poll 2 = %+v, want [3 4]", res2.Items)
	}
	if res2.Next == nil {
		t.Fatal("final delta page must still return a watermark cursor")
	}
	res3, err := h.svc.ListMessages(t.Context(), ListMessagesCommand{UserID: 1001, ConversationID: 1, Cursor: *res2.Next})
	if err != nil {
		t.Fatalf("delta poll 3: %v", err)
	}
	if len(res3.Items) != 0 || res3.HasMore {
		t.Errorf("delta poll 3 = %d items (has_more=%v), want empty", len(res3.Items), res3.HasMore)
	}
}

func TestListMessagesGatesMembership(t *testing.T) {
	h := newHarness(t)
	h.seedChat(1, 1001, 1002)

	_, err := h.svc.ListMessages(t.Context(), ListMessagesCommand{UserID: 9999, ConversationID: 1})
	if !errors.Is(err, domain.ErrNotMember) {
		t.Errorf("err = %v, want ErrNotMember", err)
	}
}

func TestListMessagesBadCursor(t *testing.T) {
	h := newHarness(t)
	h.seedChat(1, 1001, 1002)

	_, err := h.svc.ListMessages(t.Context(), ListMessagesCommand{UserID: 1001, ConversationID: 1, Cursor: "x:9"})
	if !errors.Is(err, domain.ErrInvalidCursor) {
		t.Errorf("err = %v, want ErrInvalidCursor", err)
	}
}

func TestGetMessage(t *testing.T) {
	h := newHarness(t)
	h.seedChat(1, 1001, 1002)
	msg := h.seedMessage(1, 1, 1, 1001, "hi")

	// Member reads; a tombstoned sibling also fetches.
	res, err := h.svc.GetMessage(t.Context(), GetMessageCommand{UserID: 1002, MessageID: msg.ID})
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if res.ID != strconv.FormatInt(msg.ID, 10) {
		t.Errorf("id = %s, want %d", res.ID, msg.ID)
	}

	// Non-member is gated even with a valid message id.
	if _, err := h.svc.GetMessage(t.Context(), GetMessageCommand{UserID: 9999, MessageID: msg.ID}); !errors.Is(err, domain.ErrNotMember) {
		t.Errorf("err = %v, want ErrNotMember", err)
	}
}
