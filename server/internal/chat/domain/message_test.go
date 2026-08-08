package domain

import (
	"testing"
	"time"
)

func TestParseMessageType(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want MessageType
		ok   bool
	}{
		{"text", MessageTypeText, true},
		{"media", MessageTypeMedia, true},
		{"system", MessageTypeSystem, true},
		{"reply", MessageTypeReply, true},
		{"forwarded", MessageTypeForwarded, true},
		{"deleted", "", false}, // rendered projection, never storable
		{"", "", false},
		{"voice", "", false},
	} {
		got, err := ParseMessageType(tc.in)
		if tc.ok {
			if err != nil || got != tc.want {
				t.Errorf("ParseMessageType(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
			}
		} else if err == nil {
			t.Errorf("ParseMessageType(%q) = nil error, want ErrInvalidMessageType", tc.in)
		}
	}
}

func TestRenderedTypeTombstone(t *testing.T) {
	m := &Message{Type: MessageTypeText}
	if m.RenderedType() != MessageTypeText {
		t.Fatalf("rendered = %q, want text", m.RenderedType())
	}
	at := time.Now()
	m.DeletedAt = &at
	if !m.Deleted() {
		t.Fatal("Deleted() = false, want true")
	}
	if m.RenderedType() != MessageTypeDeleted {
		t.Errorf("rendered = %q, want deleted", m.RenderedType())
	}
}

func TestEditableWindow(t *testing.T) {
	created := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	m := &Message{CreatedAt: created}

	if !m.Editable(created.Add(EditWindow - time.Second)) {
		t.Error("edit at window-1s must be allowed")
	}
	if m.Editable(created.Add(EditWindow)) {
		t.Error("edit exactly at window expiry must be rejected")
	}
	if m.Editable(created.Add(24*time.Hour + time.Second)) {
		t.Error("edit after the window must be rejected")
	}

	at := created.Add(-time.Hour)
	m.DeletedAt = &at
	if m.Editable(created.Add(time.Minute)) {
		t.Error("a tombstoned message must never be editable")
	}
}

func TestSenderOfAndZero(t *testing.T) {
	uid := int64(7)
	m := &Message{SenderID: &uid}
	if !m.SenderOf(7) || m.SenderOf(8) {
		t.Errorf("SenderOf mismatch: %v %v", m.SenderOf(7), m.SenderOf(8))
	}
	if m.SenderIDOrZero() != 7 {
		t.Errorf("SenderIDOrZero = %d, want 7", m.SenderIDOrZero())
	}

	sys := &Message{}
	if sys.SenderOf(7) || sys.SenderIDOrZero() != 0 {
		t.Error("system message must have no sender")
	}
}

func TestMaxMessageTextLenAndMedia(t *testing.T) {
	if MaxMessageTextLen != 4000 {
		t.Errorf("MaxMessageTextLen = %d, want 4000", MaxMessageTextLen)
	}
	if MaxMediaPerMessage != 10 {
		t.Errorf("MaxMediaPerMessage = %d, want 10", MaxMediaPerMessage)
	}
}
