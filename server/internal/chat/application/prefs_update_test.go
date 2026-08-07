package application

import (
	"errors"
	"testing"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/domain"
)

// ---- PATCH /v1/conversations/{id} (§7.4) ----

func TestUpdateConversationSettings(t *testing.T) {
	h := newHarness(t)
	h.seedGroup(1, 1001, [2]any{int64(1002), domain.RoleAdmin})

	title := "Renamed"
	slow := 30
	anyone := true
	hv := "from_join"
	detail, err := h.svc.UpdateConversation(t.Context(), UpdateConversationCommand{
		UserID: 1001, ConversationID: 1,
		Title:    &title,
		TitleSet: true,
		Settings: &SettingsPatch{SlowModeSeconds: &slow, AnyoneCanAdd: &anyone, HistoryVisible: &hv},
	})
	if err != nil {
		t.Fatalf("UpdateConversation error: %v", err)
	}
	if detail.Title == nil || *detail.Title != "Renamed" {
		t.Errorf("title = %v, want Renamed", detail.Title)
	}
	if detail.Settings.SlowModeSeconds != 30 || !detail.Settings.AnyoneCanAdd || detail.Settings.HistoryVisible != "from_join" {
		t.Errorf("settings = %+v", detail.Settings)
	}
	if !hasEvent(h.changelog.types(), domain.EventConversationSettings) {
		t.Errorf("outbox = %v, want conversation.settings", h.changelog.types())
	}
}

func TestUpdateConversationRequiresAdmin(t *testing.T) {
	h := newHarness(t)
	h.seedGroup(1, 1001, [2]any{int64(1002), domain.RoleMember})
	title := "x"
	_, err := h.svc.UpdateConversation(t.Context(), UpdateConversationCommand{
		UserID: 1002, ConversationID: 1, Title: &title, TitleSet: true,
	})
	if !errors.Is(err, domain.ErrInsufficientRole) {
		t.Errorf("err = %v, want ErrInsufficientRole", err)
	}
}

func TestUpdateConversationDirectTitleForbidden(t *testing.T) {
	h := newHarness(t)
	h.seedConversation(1, 1001, domain.ConversationDirect, nil, nil)
	title := "x"
	_, err := h.svc.UpdateConversation(t.Context(), UpdateConversationCommand{
		UserID: 1001, ConversationID: 1, Title: &title, TitleSet: true,
	})
	ve, ok := err.(*domain.ValidationError)
	if !ok || ve.Field != "title" {
		t.Errorf("err = %v, want title validation", err)
	}
}

func TestUpdateConversationInvalidHistoryVisible(t *testing.T) {
	h := newHarness(t)
	h.seedGroup(1, 1001)
	bad := "everything"
	_, err := h.svc.UpdateConversation(t.Context(), UpdateConversationCommand{
		UserID: 1001, ConversationID: 1,
		Settings: &SettingsPatch{HistoryVisible: &bad},
	})
	if !errors.Is(err, domain.ErrInvalidHistoryVisible) {
		t.Errorf("err = %v, want ErrInvalidHistoryVisible", err)
	}
}

// ---- PUT /v1/conversations/{id}/mute (§7.9) ----

func TestSetMute(t *testing.T) {
	h := newHarness(t)
	h.seedGroup(1, 1001)

	until := h.now.Add(2 * time.Hour)
	res, err := h.svc.SetMute(t.Context(), SetMuteCommand{UserID: 1001, ConversationID: 1, Until: &until})
	if err != nil {
		t.Fatalf("SetMute error: %v", err)
	}
	if res.MutedUntil == nil || !res.MutedUntil.Equal(until) {
		t.Errorf("muted_until = %v, want %v", res.MutedUntil, until)
	}

	// Unmute.
	res, err = h.svc.SetMute(t.Context(), SetMuteCommand{UserID: 1001, ConversationID: 1, Until: nil})
	if err != nil {
		t.Fatalf("unmute error: %v", err)
	}
	if res.MutedUntil != nil {
		t.Errorf("muted_until = %v, want nil", res.MutedUntil)
	}
}

func TestSetMutePastDeadlineRejected(t *testing.T) {
	h := newHarness(t)
	h.seedGroup(1, 1001)
	past := h.now.Add(-time.Hour)
	_, err := h.svc.SetMute(t.Context(), SetMuteCommand{UserID: 1001, ConversationID: 1, Until: &past})
	ve, ok := err.(*domain.ValidationError)
	if !ok || ve.Field != "until" {
		t.Errorf("err = %v, want until validation", err)
	}
}

// ---- PUT /v1/conversations/{id}/pin (§7.10) & archive (§7.11) ----

func TestPinAndArchiveAreMutuallyExclusive(t *testing.T) {
	h := newHarness(t)
	h.seedGroup(1, 1001)

	// Pin, then archive: archiving clears the pin.
	pin, err := h.svc.SetPin(t.Context(), SetPinCommand{UserID: 1001, ConversationID: 1, Pinned: true})
	if err != nil {
		t.Fatalf("pin error: %v", err)
	}
	if !pin.IsPinned {
		t.Fatal("must be pinned")
	}
	arch, err := h.svc.SetArchive(t.Context(), SetArchiveCommand{UserID: 1001, ConversationID: 1, Archived: true})
	if err != nil {
		t.Fatalf("archive error: %v", err)
	}
	if !arch.IsArchived {
		t.Fatal("must be archived")
	}
	m, _ := h.members.FindActive(t.Context(), 1, 1001)
	if m.PinnedAt != nil || m.ArchivedAt == nil {
		t.Errorf("after archive: pinned_at=%v archived_at=%v, want pin cleared", m.PinnedAt, m.ArchivedAt)
	}

	// Unpin then archive again clears the archive.
	_, _ = h.svc.SetArchive(t.Context(), SetArchiveCommand{UserID: 1001, ConversationID: 1, Archived: false})
	pin2, _ := h.svc.SetPin(t.Context(), SetPinCommand{UserID: 1001, ConversationID: 1, Pinned: true})
	if !pin2.IsPinned {
		t.Fatal("must be pinned again")
	}
	m, _ = h.members.FindActive(t.Context(), 1, 1001)
	if m.ArchivedAt != nil || m.PinnedAt == nil {
		t.Errorf("after pin: pinned_at=%v archived_at=%v, want archive cleared", m.PinnedAt, m.ArchivedAt)
	}
}

func TestPrefsRequireMembership(t *testing.T) {
	h := newHarness(t)
	h.seedGroup(1, 1001)
	_, err := h.svc.SetPin(t.Context(), SetPinCommand{UserID: 9999, ConversationID: 1, Pinned: true})
	if !errors.Is(err, domain.ErrNotMember) {
		t.Errorf("err = %v, want ErrNotMember", err)
	}
}
