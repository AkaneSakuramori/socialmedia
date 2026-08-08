package application

import (
	"errors"
	"strconv"
	"testing"

	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/domain"
)

func reactCmd(msgID, userID int64, emoji string) ReactionCommand {
	return ReactionCommand{UserID: userID, MessageID: msgID, Emoji: emoji}
}

// ---- POST /messages/{id}/reactions (§8.6) ----

func TestAddReaction(t *testing.T) {
	h := newHarness(t)
	h.seedChat(1, 1001, 1002)
	msg := h.seedMessage(1, 1, 1, 1001, "hi")

	res, err := h.svc.AddReaction(t.Context(), reactCmd(msg.ID, 1002, "👍"))
	if err != nil {
		t.Fatalf("AddReaction: %v", err)
	}
	if res.Count != 1 || res.MessageID != strconv.FormatInt(msg.ID, 10) {
		t.Errorf("result = %+v, want count 1", res)
	}
	if types := h.changelog.types(); len(types) != 1 || types[0] != domain.EventMessageReaction {
		t.Errorf("outbox = %v, want [message.reaction]", types)
	}

	// A duplicate (message,user,emoji) is a no-op 200 with no outbox row.
	res2, err := h.svc.AddReaction(t.Context(), reactCmd(msg.ID, 1002, "👍"))
	if err != nil {
		t.Fatalf("duplicate AddReaction: %v", err)
	}
	if res2.Count != 1 {
		t.Errorf("duplicate count = %d, want 1", res2.Count)
	}
	if types := h.changelog.types(); len(types) != 1 {
		t.Errorf("outbox = %v, want still one row for a duplicate", types)
	}
}

func TestAddReactionDistinctEmojiLimit(t *testing.T) {
	h := newHarness(t)
	h.seedChat(1, 1001, 1002)
	msg := h.seedMessage(1, 1, 1, 1001, "hi")

	for i := 0; i < maxDistinctReactions; i++ {
		emoji := "e" + strconv.Itoa(i)
		if _, err := h.svc.AddReaction(t.Context(), reactCmd(msg.ID, 1002, emoji)); err != nil {
			t.Fatalf("reaction %d: %v", i, err)
		}
	}

	// The 21st distinct emoji is rejected; re-adding an existing one is not.
	if _, err := h.svc.AddReaction(t.Context(), reactCmd(msg.ID, 1002, "e21")); !errors.Is(err, domain.ErrReactionLimit) {
		t.Errorf("err = %v, want ErrReactionLimit", err)
	}
	if _, err := h.svc.AddReaction(t.Context(), reactCmd(msg.ID, 1002, "e0")); err != nil {
		t.Errorf("re-add existing emoji: %v", err)
	}
}

func TestAddReactionAuth(t *testing.T) {
	h := newHarness(t)
	h.seedChat(1, 1001, 1002)
	msg := h.seedMessage(1, 1, 1, 1001, "hi")

	// Non-member is gated.
	if _, err := h.svc.AddReaction(t.Context(), reactCmd(msg.ID, 9999, "👍")); !errors.Is(err, domain.ErrNotMember) {
		t.Errorf("err = %v, want ErrNotMember", err)
	}

	// Invalid emoji is a field validation error.
	if _, err := h.svc.AddReaction(t.Context(), reactCmd(msg.ID, 1002, "   ")); err == nil {
		t.Error("empty emoji must fail")
	}

	// Tombstoned messages cannot be reacted to.
	del := h.now
	h.messages.byID[msg.ID].DeletedAt = &del
	if _, err := h.svc.AddReaction(t.Context(), reactCmd(msg.ID, 1002, "👍")); !errors.Is(err, domain.ErrMessageDeleted) {
		t.Errorf("err = %v, want ErrMessageDeleted", err)
	}
}

// ---- DELETE /messages/{id}/reactions/{emoji} (§8.7) ----

func TestRemoveReaction(t *testing.T) {
	h := newHarness(t)
	h.seedChat(1, 1001, 1002)
	msg := h.seedMessage(1, 1, 1, 1001, "hi")

	if _, err := h.svc.AddReaction(t.Context(), reactCmd(msg.ID, 1002, "🔥")); err != nil {
		t.Fatalf("add: %v", err)
	}

	res, err := h.svc.RemoveReaction(t.Context(), reactCmd(msg.ID, 1002, "🔥"))
	if err != nil {
		t.Fatalf("RemoveReaction: %v", err)
	}
	if res.Count != 0 {
		t.Errorf("count = %d, want 0", res.Count)
	}

	// Removing a reaction that does not exist is a no-op 200.
	res2, err := h.svc.RemoveReaction(t.Context(), reactCmd(msg.ID, 1002, "🔥"))
	if err != nil {
		t.Fatalf("no-op remove: %v", err)
	}
	if res2.Count != 0 {
		t.Errorf("count = %d, want 0", res2.Count)
	}
}

// ---- GET /messages/{id}/reactions/{emoji} (§8.8) ----

func TestListReactions(t *testing.T) {
	h := newHarness(t)
	h.seedChat(1, 1001, 1002, 1003)
	msg := h.seedMessage(1, 1, 1, 1001, "hi")
	h.users.seed(1002, "Sami")
	h.users.seed(1003, "Ravi")

	for _, uid := range []int64{1002, 1003} {
		if _, err := h.svc.AddReaction(t.Context(), reactCmd(msg.ID, uid, "🎉")); err != nil {
			t.Fatalf("add by %d: %v", uid, err)
		}
	}

	res, err := h.svc.ListReactions(t.Context(), ListReactionsCommand{UserID: 1001, MessageID: msg.ID, Emoji: "🎉"})
	if err != nil {
		t.Fatalf("ListReactions: %v", err)
	}
	if len(res.Reactors) != 2 {
		t.Fatalf("reactors = %d, want 2", len(res.Reactors))
	}
	names := map[string]string{}
	for _, r := range res.Reactors {
		names[r.UserID] = r.DisplayName
	}
	if names["1002"] != "Sami" || names["1003"] != "Ravi" {
		t.Errorf("display names = %v, want Sami/Ravi", names)
	}
}
