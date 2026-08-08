package application

import (
	"errors"
	"testing"

	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/domain"
)

// seedChat wires a group conversation owned by ownerID with the given member
// ids (each seeded as a user + active membership).
func (h *harness) seedChat(convID, ownerID int64, memberIDs ...int64) *domain.Conversation {
	h.users.seed(ownerID, "Aya")
	c := h.seedConversation(convID, ownerID, domain.ConversationGroup, strptr("Trip"), nil)
	for _, uid := range memberIDs {
		h.users.seed(uid, nameFor(uid))
		h.members.rows[convID][uid] = newMembership(convID, uid, domain.RoleMember, h.now)
	}
	return c
}

func textCmd(convID, userID int64, clientID, text string) SendMessageCommand {
	return SendMessageCommand{
		UserID:         userID,
		ConversationID: convID,
		ClientMsgID:    clientID,
		Type:           "text",
		Text:           &text,
	}
}

// ---- POST /messages (§8.2) ----

func TestSendMessagePersistsAndBumps(t *testing.T) {
	h := newHarness(t)
	h.seedChat(1, 1001, 1002)

	res, err := h.svc.SendMessage(t.Context(), textCmd(1, 1001, "c-1", "hello"))
	if err != nil {
		t.Fatalf("SendMessage error: %v", err)
	}
	if !res.Created {
		t.Fatal("Created = false, want true on first send")
	}
	if res.View.Sequence != "1" || res.View.SenderID == nil || *res.View.SenderID != "1001" {
		t.Errorf("view = %+v, want seq 1 / sender 1001", res.View)
	}
	if res.View.Content == nil || res.View.Content.Text == nil || *res.View.Content.Text != "hello" {
		t.Errorf("content = %+v, want hello", res.View.Content)
	}
	if res.View.Status != "sent" {
		t.Errorf("status = %q, want sent", res.View.Status)
	}

	// The conversation's last-message denormalization advanced.
	c := h.convos.byID[1]
	if c.LastMessageSeq == nil || *c.LastMessageSeq != 1 || c.LastMessageSnippet == nil || *c.LastMessageSnippet != "hello" {
		t.Errorf("conversation bump = %+v, want seq 1 / snippet hello", c.LastMessageSeq)
	}

	// One message.created outbox row, fanned out to the active members.
	if types := h.changelog.types(); len(types) != 1 || types[0] != domain.EventMessageCreated {
		t.Fatalf("outbox = %v, want [message.created]", types)
	}
	if len(h.changelog.entries[0].AffectedUserIDs) != 2 {
		t.Errorf("affected = %v, want both members", h.changelog.entries[0].AffectedUserIDs)
	}
}

func TestSendMessageIdempotentReplay(t *testing.T) {
	h := newHarness(t)
	h.seedChat(1, 1001, 1002)

	first, err := h.svc.SendMessage(t.Context(), textCmd(1, 1001, "dup-1", "hello"))
	if err != nil {
		t.Fatalf("first send: %v", err)
	}

	// Same client_msg_id, even with a different body: the partial unique index
	// collapses the duplicate and the original is replayed (HTTP 200).
	second, err := h.svc.SendMessage(t.Context(), textCmd(1, 1001, "dup-1", "edited body"))
	if err != nil {
		t.Fatalf("replay send: %v", err)
	}
	if second.Created {
		t.Fatal("Created = true, want false on replay")
	}
	if second.View.ID != first.View.ID || second.View.Sequence != first.View.Sequence {
		t.Errorf("replay = id %s seq %s, want original id %s seq %s",
			second.View.ID, second.View.Sequence, first.View.ID, first.View.Sequence)
	}

	// Only one message row and one outbox row exist.
	if len(h.messages.byID) != 1 {
		t.Errorf("message rows = %d, want 1", len(h.messages.byID))
	}
	if types := h.changelog.types(); len(types) != 1 {
		t.Errorf("outbox = %v, want a single message.created", types)
	}
}

func TestSendMessageNotMember(t *testing.T) {
	h := newHarness(t)
	h.seedChat(1, 1001, 1002)

	_, err := h.svc.SendMessage(t.Context(), textCmd(1, 9999, "c-1", "hi"))
	if !errors.Is(err, domain.ErrNotMember) {
		t.Errorf("err = %v, want ErrNotMember", err)
	}

	// Left members are equally gated.
	h.members.rows[1][1002].LeftAt = &h.now
	_, err = h.svc.SendMessage(t.Context(), textCmd(1, 1002, "c-2", "hi"))
	if !errors.Is(err, domain.ErrNotMember) {
		t.Errorf("err = %v, want ErrNotMember for left member", err)
	}
}

func TestSendMessageValidation(t *testing.T) {
	h := newHarness(t)
	h.seedChat(1, 1001, 1002)
	ctx := t.Context()

	bad := []struct {
		name  string
		cmd   SendMessageCommand
		field string
	}{
		{"empty client id", textCmd(1, 1001, "", "hi"), "client_msg_id"},
		{"long client id", textCmd(1, 1001, "x"+string(make([]byte, 255)), "hi"), "client_msg_id"},
		{"bad type", func() SendMessageCommand {
			c := textCmd(1, 1001, "c", "hi")
			c.Type = "voice"
			return c
		}(), "type"},
		{"system type rejected", func() SendMessageCommand {
			c := textCmd(1, 1001, "c", "hi")
			c.Type = "system"
			return c
		}(), "type"},
		{"no content", SendMessageCommand{UserID: 1001, ConversationID: 1, ClientMsgID: "c", Type: "text"}, "content"},
		{"both text and media", func() SendMessageCommand {
			c := textCmd(1, 1001, "c", "hi")
			c.Media = []domain.Attachment{{MediaID: "m1"}}
			return c
		}(), "content"},
		{"long text", textCmd(1, 1001, "c", string(make([]byte, 4001))), "content.text"},
		{"too many media", SendMessageCommand{
			UserID: 1001, ConversationID: 1, ClientMsgID: "c", Type: "media",
			Media: make([]domain.Attachment, 11),
		}, "media"},
		{"missing media id", SendMessageCommand{
			UserID: 1001, ConversationID: 1, ClientMsgID: "c", Type: "media",
			Media: []domain.Attachment{{Kind: "image"}},
		}, "media"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			_, err := h.svc.SendMessage(ctx, tc.cmd)
			ve, ok := err.(*domain.ValidationError)
			if !ok {
				t.Fatalf("err = %v, want validation error", err)
			}
			if ve.Field != tc.field {
				t.Errorf("field = %q, want %q", ve.Field, tc.field)
			}
		})
	}

	// Media message with a proper id is accepted.
	_, err := h.svc.SendMessage(ctx, SendMessageCommand{
		UserID: 1001, ConversationID: 1, ClientMsgID: "c", Type: "media",
		Media: []domain.Attachment{{MediaID: "m1", Kind: "image"}},
	})
	if err != nil {
		t.Fatalf("valid media send: %v", err)
	}
}

func TestSendMessageMentionsMustBeMembers(t *testing.T) {
	h := newHarness(t)
	h.seedChat(1, 1001, 1002)

	cmd := textCmd(1, 1001, "c", "hi")
	cmd.Mentions = []int64{1002}
	if _, err := h.svc.SendMessage(t.Context(), cmd); err != nil {
		t.Fatalf("mention of member 1002: %v", err)
	}

	cmd2 := textCmd(1, 1001, "c2", "hi")
	cmd2.Mentions = []int64{7777}
	if _, err := h.svc.SendMessage(t.Context(), cmd2); !errors.Is(err, domain.ErrMentionNotMember) {
		t.Errorf("err = %v, want ErrMentionNotMember", err)
	}

	// Duplicate mentions are de-duplicated, not rejected.
	cmd3 := textCmd(1, 1001, "c3", "hi")
	cmd3.Mentions = []int64{1002, 1002}
	if _, err := h.svc.SendMessage(t.Context(), cmd3); err != nil {
		t.Errorf("duplicate mention: %v", err)
	}
}

func TestSendMessageReplyTarget(t *testing.T) {
	h := newHarness(t)
	h.seedChat(1, 1001, 1002)

	target, err := h.svc.SendMessage(t.Context(), textCmd(1, 1001, "c1", "original"))
	if err != nil {
		t.Fatalf("target send: %v", err)
	}

	cmd := textCmd(1, 1002, "c2", "replied")
	seq := int64(1)
	cmd.ReplyToSeq = &seq
	res, err := h.svc.SendMessage(t.Context(), cmd)
	if err != nil {
		t.Fatalf("reply send: %v", err)
	}
	if res.View.ReplyTo == nil || res.View.ReplyTo.ID != target.View.ID {
		t.Errorf("reply_to = %+v, want id %s", res.View.ReplyTo, target.View.ID)
	}

	// A reply to a nonexistent sequence is a validation error.
	cmd2 := textCmd(1, 1002, "c3", "bad reply")
	seq2 := int64(99)
	cmd2.ReplyToSeq = &seq2
	if _, err := h.svc.SendMessage(t.Context(), cmd2); err == nil {
		t.Error("reply to nonexistent seq must fail")
	}
}

func TestSendMessageRetriesSequenceConflict(t *testing.T) {
	h := newHarness(t)
	h.seedChat(1, 1001, 1002)
	h.messages.setConflict(1) // the first allocated sequence collides

	res, err := h.svc.SendMessage(t.Context(), textCmd(1, 1001, "c", "hi"))
	if err != nil {
		t.Fatalf("SendMessage error: %v", err)
	}
	if res.View.Sequence != "2" {
		t.Errorf("sequence = %s, want 2 (retried past the collision)", res.View.Sequence)
	}
	if !res.Created {
		t.Error("Created = false, want true after retry")
	}
}

func TestSendMessageSequenceExhausted(t *testing.T) {
	h := newHarness(t)
	h.seedChat(1, 1001, 1002)
	for i := int64(1); i <= maxSendRetries; i++ {
		h.messages.setConflict(i)
	}

	_, err := h.svc.SendMessage(t.Context(), textCmd(1, 1001, "c", "hi"))
	if !errors.Is(err, domain.ErrSequenceConflict) {
		t.Errorf("err = %v, want ErrSequenceConflict after %d retries", err, maxSendRetries)
	}
}

func TestSendMessagePushesDurableFloor(t *testing.T) {
	h := newHarness(t)
	h.seedChat(1, 1001, 1002)

	for i := 0; i < 3; i++ {
		if _, err := h.svc.SendMessage(t.Context(), textCmd(1, 1001, "c"+string(rune('a'+i)), "m")); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	floor, err := h.seqsource.Floor(t.Context(), 1)
	if err != nil {
		t.Fatalf("floor: %v", err)
	}
	if floor != 3 {
		t.Errorf("floor = %d, want 3", floor)
	}
}
