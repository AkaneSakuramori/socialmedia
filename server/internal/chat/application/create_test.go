package application

import (
	"errors"
	"testing"

	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/domain"
)

// ---- POST /v1/conversations (§7.2) ----

func TestCreateDirectConversation(t *testing.T) {
	h := newHarness(t)
	h.users.seed(1001, "Aya")
	h.users.seed(1002, "Sami")

	res, err := h.svc.CreateConversation(t.Context(), CreateConversationCommand{
		UserID:         1001,
		Type:           "direct",
		ParticipantIDs: []int64{1002},
	})
	if err != nil {
		t.Fatalf("CreateConversation error: %v", err)
	}
	if !res.Created {
		t.Fatal("direct conversation must be created")
	}
	conv := h.convos.byID[9000000001]
	if conv == nil {
		t.Fatal("conversation row must exist")
	}
	if conv.Type != domain.ConversationDirect {
		t.Errorf("type = %s, want direct", conv.Type)
	}
	if conv.CreatedBy != 1001 {
		t.Errorf("created_by = %d, want 1001", conv.CreatedBy)
	}

	// Both members, both role member for direct.
	if n, _ := h.members.CountActive(t.Context(), conv.ID); n != 2 {
		t.Errorf("active members = %d, want 2", n)
	}
	caller, _ := h.members.FindActive(t.Context(), conv.ID, 1001)
	if caller.Role != domain.RoleMember {
		t.Errorf("caller role = %s, want member", caller.Role)
	}

	// Durable sequence row initialized (DATABASE.md §5.4).
	if _, ok := h.sequences.byID[conv.ID]; !ok {
		t.Error("conversation_sequences row must be initialized")
	}

	// Outbox rows written in the same transaction (ARCHITECTURE.md §37.4).
	if got := h.changelog.types(); !hasEvent(got, domain.EventConversationCreated) {
		t.Errorf("outbox = %v, want conversation.created", got)
	}

	// The list-view title derives from the counterpart's display name.
	if res.View.Title == nil || *res.View.Title != "Sami" {
		t.Errorf("direct title = %v, want Sami", res.View.Title)
	}
}

func TestCreateDirectDedupReturnsExisting(t *testing.T) {
	h := newHarness(t)
	h.users.seed(1001, "Aya")
	h.users.seed(1002, "Sami")
	if _, err := h.svc.CreateConversation(t.Context(), CreateConversationCommand{
		UserID: 1001, Type: "direct", ParticipantIDs: []int64{1002},
	}); err != nil {
		t.Fatalf("first create: %v", err)
	}

	// Same pair, opposite direction: returns the existing conversation (200).
	res, err := h.svc.CreateConversation(t.Context(), CreateConversationCommand{
		UserID: 1002, Type: "direct", ParticipantIDs: []int64{1001},
	})
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if res.Created {
		t.Error("dedup must return the existing conversation, not create")
	}
	if res.View.ID != "9000000001" {
		t.Errorf("view id = %s, want the original conversation", res.View.ID)
	}
	if n := len(h.convos.byID); n != 1 {
		t.Errorf("conversation rows = %d, want 1 (no duplicate)", n)
	}
	if res.View.Title == nil || *res.View.Title != "Aya" {
		t.Errorf("counterpart title = %v, want Aya", res.View.Title)
	}
}

func TestCreateGroup(t *testing.T) {
	h := newHarness(t)
	h.users.seed(1001, "Aya")
	h.users.seed(1002, "Sami")
	h.users.seed(1003, "Ravi")

	title := "Weekend trip"
	res, err := h.svc.CreateConversation(t.Context(), CreateConversationCommand{
		UserID:         1001,
		Type:           "group",
		ParticipantIDs: []int64{1002, 1003},
		Title:          &title,
	})
	if err != nil {
		t.Fatalf("CreateConversation error: %v", err)
	}
	if !res.Created {
		t.Fatal("group must be created")
	}
	if n, _ := h.members.CountActive(t.Context(), 9000000001); n != 3 {
		t.Errorf("active members = %d, want 3", n)
	}
	caller, _ := h.members.FindActive(t.Context(), 9000000001, 1001)
	if caller.Role != domain.RoleOwner {
		t.Errorf("creator role = %s, want owner", caller.Role)
	}
	if res.View.Title == nil || *res.View.Title != "Weekend trip" {
		t.Errorf("group title = %v, want Weekend trip", res.View.Title)
	}
}

func TestCreateGroupRequiresTitle(t *testing.T) {
	h := newHarness(t)
	h.users.seed(1001, "Aya")
	h.users.seed(1002, "Sami")

	_, err := h.svc.CreateConversation(t.Context(), CreateConversationCommand{
		UserID:         1001,
		Type:           "group",
		ParticipantIDs: []int64{1002},
	})
	// DATABASE.md §5.1 CHECK (type='direct' OR title IS NOT NULL).
	if !errors.Is(err, domain.ErrGroupTitleRequired) {
		t.Errorf("err = %v, want ErrGroupTitleRequired", err)
	}
}

func TestCreateConversationValidation(t *testing.T) {
	h := newHarness(t)
	h.users.seed(1001, "Aya")
	h.users.seed(1002, "Sami")

	cases := []struct {
		name   string
		cmd    CreateConversationCommand
		field  string
		reason string
	}{
		{"invalid type", CreateConversationCommand{UserID: 1001, Type: "channel", ParticipantIDs: []int64{1002}}, "type", "invalid_conversation_type"},
		{"empty participants", CreateConversationCommand{UserID: 1001, Type: "direct"}, "participant_ids", "must_not_be_empty"},
		{"self included", CreateConversationCommand{UserID: 1001, Type: "direct", ParticipantIDs: []int64{1001}}, "participant_ids", "must_not_include_self"},
		{"direct needs one other", CreateConversationCommand{UserID: 1001, Type: "direct", ParticipantIDs: []int64{1002, 1003}}, "participant_ids", "direct_requires_exactly_one_other"},
	}
	h.users.seed(1003, "Bob")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := h.svc.CreateConversation(t.Context(), tc.cmd)
			ve, ok := err.(*domain.ValidationError)
			if !ok {
				t.Fatalf("err = %v (%T), want *domain.ValidationError", err, err)
			}
			if ve.Field != tc.field || ve.Reason != tc.reason {
				t.Errorf("validation = %s/%s, want %s/%s", ve.Field, ve.Reason, tc.field, tc.reason)
			}
		})
	}

	// Unknown participant.
	_, err := h.svc.CreateConversation(t.Context(), CreateConversationCommand{
		UserID: 1001, Type: "direct", ParticipantIDs: []int64{999999},
	})
	ve, ok := err.(*domain.ValidationError)
	if !ok || ve.Reason != "unknown_user" {
		t.Errorf("err = %v, want unknown_user validation", err)
	}
}

func hasEvent(events []string, want string) bool {
	for _, e := range events {
		if e == want {
			return true
		}
	}
	return false
}
