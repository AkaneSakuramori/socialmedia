package application

import (
	"errors"
	"testing"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/domain"
)

func editCmd(msgID, userID int64, text string) EditMessageCommand {
	return EditMessageCommand{UserID: userID, MessageID: msgID, NewText: text}
}

// ---- PATCH /messages/{id} (§8.4) ----

func TestEditMessageBySender(t *testing.T) {
	h := newHarness(t)
	h.seedChat(1, 1001, 1002)
	msg := h.seedMessage(1, 1, 1, 1001, "original")

	res, err := h.svc.EditMessage(t.Context(), editCmd(msg.ID, 1001, "revised"))
	if err != nil {
		t.Fatalf("EditMessage: %v", err)
	}
	if res.Content == nil || res.Content.Text == nil || *res.Content.Text != "revised" {
		t.Errorf("content = %+v, want revised", res.Content)
	}
	if res.EditedAt == nil {
		t.Error("edited_at must be set")
	}

	// Append-only edit history recorded the prior body.
	if len(h.messages.edits) != 1 || h.messages.edits[0].OldContent != "original" {
		t.Errorf("edit history = %+v, want [old=original]", h.messages.edits)
	}
	if types := h.changelog.types(); len(types) != 1 || types[0] != domain.EventMessageEdited {
		t.Errorf("outbox = %v, want [message.edited]", types)
	}
}

func TestEditMessageAuthAndState(t *testing.T) {
	h := newHarness(t)
	h.seedChat(1, 1001, 1002)
	msg := h.seedMessage(1, 1, 1, 1001, "hi")

	// Non-sender is rejected even as an active member.
	if _, err := h.svc.EditMessage(t.Context(), editCmd(msg.ID, 1002, "x")); !errors.Is(err, domain.ErrNotSender) {
		t.Errorf("err = %v, want ErrNotSender", err)
	}

	// Out-of-window edits are rejected.
	old := h.now.Add(-(domain.EditWindow + time.Hour))
	h.messages.byID[msg.ID].CreatedAt = old
	if _, err := h.svc.EditMessage(t.Context(), editCmd(msg.ID, 1001, "x")); !errors.Is(err, domain.ErrEditWindowExpired) {
		t.Errorf("err = %v, want ErrEditWindowExpired", err)
	}

	// Tombstoned messages cannot be edited.
	h.messages.byID[msg.ID].CreatedAt = h.now
	del := h.now
	h.messages.byID[msg.ID].DeletedAt = &del
	if _, err := h.svc.EditMessage(t.Context(), editCmd(msg.ID, 1001, "x")); !errors.Is(err, domain.ErrMessageDeleted) {
		t.Errorf("err = %v, want ErrMessageDeleted", err)
	}
}

func TestEditMessageValidationAndEditability(t *testing.T) {
	h := newHarness(t)
	h.seedChat(1, 1001, 1002)

	// Empty text is a field validation error.
	msg := h.seedMessage(1, 1, 1, 1001, "hi")
	if _, err := h.svc.EditMessage(t.Context(), editCmd(msg.ID, 1001, "   ")); err == nil {
		t.Error("empty edit must fail")
	}

	// Media (no content) messages are not editable.
	med := h.seedMessage(1, 2, 2, 1001, "")
	med.Content = nil
	h.messages.byID[med.ID].Content = nil
	if _, err := h.svc.EditMessage(t.Context(), editCmd(med.ID, 1001, "x")); !errors.Is(err, domain.ErrMessageNotEditable) {
		t.Errorf("err = %v, want ErrMessageNotEditable", err)
	}
}

// ---- DELETE /messages/{id} (§8.5) ----

func TestDeleteMessageAllBySender(t *testing.T) {
	h := newHarness(t)
	h.seedChat(1, 1001, 1002)
	msg := h.seedMessage(1, 1, 1, 1001, "hi")

	res, err := h.svc.DeleteMessage(t.Context(), DeleteMessageCommand{UserID: 1001, MessageID: msg.ID})
	if err != nil {
		t.Fatalf("DeleteMessage: %v", err)
	}
	if res.Deleted != "all" {
		t.Errorf("deleted = %q, want all", res.Deleted)
	}

	// Row is tombstoned, not removed.
	if h.messages.byID[msg.ID].DeletedAt == nil {
		t.Error("message must be tombstoned")
	}
	if types := h.changelog.types(); len(types) != 1 || types[0] != domain.EventMessageDeleted {
		t.Errorf("outbox = %v, want [message.deleted]", types)
	}

	// Deleting again is idempotent 200 and writes no new outbox row.
	if _, err := h.svc.DeleteMessage(t.Context(), DeleteMessageCommand{UserID: 1001, MessageID: msg.ID}); err != nil {
		t.Fatalf("double delete: %v", err)
	}
	if types := h.changelog.types(); len(types) != 1 {
		t.Errorf("outbox = %v, want still one row after double delete", types)
	}
}

func TestDeleteMessageAdminMayDeleteOthers(t *testing.T) {
	h := newHarness(t)
	h.seedChat(1, 1001, 1002, 1003)
	msg := h.seedMessage(1, 1, 1, 1002, "hi") // sent by a member

	// A plain member cannot delete another's message.
	if _, err := h.svc.DeleteMessage(t.Context(), DeleteMessageCommand{UserID: 1003, MessageID: msg.ID}); !errors.Is(err, domain.ErrNotSender) {
		t.Errorf("member delete = %v, want ErrNotSender", err)
	}

	// Promote that member to admin: they may delete-for-all.
	h.members.rows[1][1003].Role = domain.RoleAdmin
	if _, err := h.svc.DeleteMessage(t.Context(), DeleteMessageCommand{UserID: 1003, MessageID: msg.ID}); err != nil {
		t.Errorf("admin delete = %v, want nil", err)
	}
	if h.messages.byID[msg.ID].DeletedAt == nil {
		t.Error("admin delete must tombstone")
	}
}

func TestDeleteMessageSelfIsNoop(t *testing.T) {
	h := newHarness(t)
	h.seedChat(1, 1001, 1002)
	msg := h.seedMessage(1, 1, 1, 1001, "hi")

	res, err := h.svc.DeleteMessage(t.Context(), DeleteMessageCommand{UserID: 1001, MessageID: msg.ID, Mode: "self"})
	if err != nil {
		t.Fatalf("delete self: %v", err)
	}
	if res.Deleted != "self" {
		t.Errorf("deleted = %q, want self", res.Deleted)
	}
	if h.messages.byID[msg.ID].DeletedAt != nil {
		t.Error("self-delete must not tombstone on the server")
	}
	if len(h.changelog.entries) != 0 {
		t.Errorf("outbox = %v, want empty for self-delete", h.changelog.entries)
	}
}

func TestDeleteMessageInvalidMode(t *testing.T) {
	h := newHarness(t)
	h.seedChat(1, 1001, 1002)
	msg := h.seedMessage(1, 1, 1, 1001, "hi")

	_, err := h.svc.DeleteMessage(t.Context(), DeleteMessageCommand{UserID: 1001, MessageID: msg.ID, Mode: "everyone"})
	if err == nil {
		t.Error("invalid mode must fail")
	}
}

func TestDeleteMessageNotFoundAndNotMember(t *testing.T) {
	h := newHarness(t)
	h.seedChat(1, 1001, 1002)
	msg := h.seedMessage(1, 1, 1, 1002, "hi")

	if _, err := h.svc.DeleteMessage(t.Context(), DeleteMessageCommand{UserID: 1001, MessageID: 424242}); !errors.Is(err, domain.ErrMessageNotFound) {
		t.Errorf("err = %v, want ErrMessageNotFound", err)
	}
	if _, err := h.svc.DeleteMessage(t.Context(), DeleteMessageCommand{UserID: 9999, MessageID: msg.ID}); !errors.Is(err, domain.ErrNotMember) {
		t.Errorf("err = %v, want ErrNotMember", err)
	}
}
