package application

import (
	"errors"
	"testing"

	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/domain"
)

func markReadCmd(convID, userID, seq int64) MarkReadCommand {
	return MarkReadCommand{UserID: userID, ConversationID: convID, ReadSeq: seq}
}

// ---- PUT /conversations/{id}/read (§10.1) ----

func TestMarkReadAdvances(t *testing.T) {
	h := newHarness(t)
	h.seedChat(1, 1001, 1002)
	h.seedMessage(1, 1, 1, 1002, "m1")
	h.seedMessage(1, 2, 2, 1002, "m2")
	h.seedMessage(1, 3, 3, 1002, "m3")

	res, err := h.svc.MarkRead(t.Context(), markReadCmd(1, 1001, 3))
	if err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	// The data model forbids a read cursor ahead of the delivered cursor
	// (migration 000006 CHECK), so a read receipt implies delivery: omitting
	// deliver_up_to_seq still advances delivered up to the read cursor.
	if res.LastReadSeq != "3" || res.LastDeliveredSeq != "3" {
		t.Errorf("result = %+v, want read 3 / delivered 3", res)
	}

	// The read cursor persisted.
	mem := h.members.rows[1][1001]
	if mem.LastReadSeq != 3 || mem.LastReadAt == nil || !mem.LastReadAt.Equal(h.now) {
		t.Errorf("membership cursors = %+v, want read 3 at now", mem)
	}

	// receipt.read fanned out to the senders of the newly-read messages (1002).
	var readEntries int
	for _, e := range h.changelog.entries {
		if e.EventType == domain.EventReceiptRead {
			readEntries++
			if len(e.AffectedUserIDs) != 1 || e.AffectedUserIDs[0] != 1002 {
				t.Errorf("receipt.read affected = %v, want [1002]", e.AffectedUserIDs)
			}
		}
	}
	if readEntries != 1 {
		t.Errorf("receipt.read rows = %d, want 1", readEntries)
	}

	// The implied delivery advance emits a receipt.delivered row for the reader.
	var delEntries int
	for _, e := range h.changelog.entries {
		if e.EventType == domain.EventReceiptDelivered {
			delEntries++
			if len(e.AffectedUserIDs) != 1 || e.AffectedUserIDs[0] != 1001 {
				t.Errorf("receipt.delivered affected = %v, want [1001]", e.AffectedUserIDs)
			}
		}
	}
	if delEntries != 1 {
		t.Errorf("receipt.delivered rows = %d, want 1", delEntries)
	}
}

func TestMarkReadMonotonicNeverRegresses(t *testing.T) {
	h := newHarness(t)
	h.seedChat(1, 1001, 1002)
	h.seedMessage(1, 1, 1, 1002, "m1")
	h.seedMessage(1, 2, 2, 1002, "m2")
	h.seedMessage(1, 3, 3, 1002, "m3")

	// Two devices race: device A reads 5, then device B reports 2 (stale).
	if _, err := h.svc.MarkRead(t.Context(), markReadCmd(1, 1001, 5)); err != nil {
		t.Fatalf("advance to 5: %v", err)
	}
	readRows := func() int {
		n := 0
		for _, e := range h.changelog.entries {
			if e.EventType == domain.EventReceiptRead {
				n++
			}
		}
		return n
	}
	before := readRows()
	res, err := h.svc.MarkRead(t.Context(), markReadCmd(1, 1001, 2))
	if err != nil {
		t.Fatalf("stale report: %v", err)
	}
	if res.LastReadSeq != "5" {
		t.Errorf("cursor = %s, want 5 (GREATEST max-merge)", res.LastReadSeq)
	}
	if h.members.rows[1][1001].LastReadSeq != 5 {
		t.Error("persisted cursor must not regress")
	}

	// A stale report produces no new receipt.read outbox delta.
	if got := readRows(); got != before {
		t.Errorf("stale report added receipt.read rows: %d → %d, want no change", before, got)
	}
}

func TestMarkReadDeliveredCursor(t *testing.T) {
	h := newHarness(t)
	h.seedChat(1, 1001, 1002)

	cmd := markReadCmd(1, 1001, 1)
	delivered := int64(2)
	cmd.DeliveredSeq = &delivered
	res, err := h.svc.MarkRead(t.Context(), cmd)
	if err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	if res.LastDeliveredSeq != "2" {
		t.Errorf("delivered = %s, want 2", res.LastDeliveredSeq)
	}

	var delEntries int
	for _, e := range h.changelog.entries {
		if e.EventType == domain.EventReceiptDelivered {
			delEntries++
			if len(e.AffectedUserIDs) != 1 || e.AffectedUserIDs[0] != 1001 {
				t.Errorf("receipt.delivered affected = %v, want [1001]", e.AffectedUserIDs)
			}
		}
	}
	if delEntries != 1 {
		t.Errorf("receipt.delivered rows = %d, want 1", delEntries)
	}
}

func TestMarkReadValidationAndAuth(t *testing.T) {
	h := newHarness(t)
	h.seedChat(1, 1001, 1002)

	if _, err := h.svc.MarkRead(t.Context(), markReadCmd(1, 1001, 0)); err == nil {
		t.Error("read_seq 0 must fail")
	}
	bad := markReadCmd(1, 1001, 1)
	badDelivered := int64(-1)
	bad.DeliveredSeq = &badDelivered
	if _, err := h.svc.MarkRead(t.Context(), bad); err == nil {
		t.Error("negative delivered seq must fail")
	}
	if _, err := h.svc.MarkRead(t.Context(), markReadCmd(1, 9999, 1)); !errors.Is(err, domain.ErrNotMember) {
		t.Errorf("err = %v, want ErrNotMember", err)
	}
}

// ---- GET /conversations/{id}/receipts (§10.2) ----

func TestGetReceipts(t *testing.T) {
	h := newHarness(t)
	h.seedChat(1, 1001, 1002)
	h.seedMessage(1, 1, 1, 1002, "m1")
	seq := int64(1)
	h.convos.byID[1].LastMessageSeq = &seq

	if _, err := h.svc.MarkRead(t.Context(), markReadCmd(1, 1002, 1)); err != nil {
		t.Fatalf("mark read: %v", err)
	}

	res, err := h.svc.GetReceipts(t.Context(), GetReceiptsCommand{UserID: 1001, ConversationID: 1})
	if err != nil {
		t.Fatalf("GetReceipts: %v", err)
	}
	if res.LastMessageSeq == nil || *res.LastMessageSeq != "1" {
		t.Errorf("last_message_seq = %v, want 1", res.LastMessageSeq)
	}
	if len(res.Readers) != 2 {
		t.Fatalf("readers = %d, want 2", len(res.Readers))
	}
	for _, r := range res.Readers {
		if r.UserID == "1002" && r.LastReadSeq != "1" {
			t.Errorf("reader 1002 last_read_seq = %s, want 1", r.LastReadSeq)
		}
		if r.UserID == "1001" && r.LastReadSeq != "0" {
			t.Errorf("reader 1001 last_read_seq = %s, want 0", r.LastReadSeq)
		}
	}

	if _, err := h.svc.GetReceipts(t.Context(), GetReceiptsCommand{UserID: 9999, ConversationID: 1}); !errors.Is(err, domain.ErrNotMember) {
		t.Errorf("err = %v, want ErrNotMember", err)
	}
}
