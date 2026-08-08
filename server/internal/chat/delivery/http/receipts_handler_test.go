package http

import (
	"net/http"
	"testing"

	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/application"
)

// ---- PUT /v1/conversations/{id}/receipts (§10.1) ----

func TestMarkRead(t *testing.T) {
	svc := &fakeService{markReadRes: &application.ReceiptResult{LastReadSeq: "5", LastDeliveredSeq: "3"}}
	h := newTestRouter(t, svc)

	rec := do(t, h, http.MethodPut, "/v1/conversations/1/receipts",
		`{"last_read_seq":"5","deliver_up_to_seq":"3"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if svc.markReadCmd.ConversationID != 1 || svc.markReadCmd.ReadSeq != 5 {
		t.Errorf("cmd = %+v, want conv 1 / read 5", svc.markReadCmd)
	}
	if svc.markReadCmd.DeliveredSeq == nil || *svc.markReadCmd.DeliveredSeq != 3 {
		t.Errorf("delivered = %v, want 3", svc.markReadCmd.DeliveredSeq)
	}
}

func TestMarkReadDefaultsDeliveredNil(t *testing.T) {
	svc := &fakeService{markReadRes: &application.ReceiptResult{LastReadSeq: "1"}}
	h := newTestRouter(t, svc)

	rec := do(t, h, http.MethodPut, "/v1/conversations/1/receipts", `{"last_read_seq":"1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if svc.markReadCmd.DeliveredSeq != nil {
		t.Errorf("delivered = %v, want nil when omitted", svc.markReadCmd.DeliveredSeq)
	}
}

func TestMarkReadValidation(t *testing.T) {
	h := newTestRouter(t, &fakeService{})

	for _, body := range []string{
		`{}`,                      // missing last_read_seq
		`{"last_read_seq":"abc"}`, // non-numeric
		`{"last_read_seq":"0"}`,   // zero
		`{"last_read_seq":"1","deliver_up_to_seq":"-2"}`, // negative delivered
	} {
		rec := do(t, h, http.MethodPut, "/v1/conversations/1/receipts", body)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("body %s → status %d, want 422", body, rec.Code)
		}
	}
}

// ---- GET /v1/conversations/{id}/receipts (§10.2) ----

func TestGetReceipts(t *testing.T) {
	svc := &fakeService{getReceiptsRes: &application.ReceiptsResult{
		ConversationID: "1",
		Readers:        []application.ReaderView{{UserID: "1002", LastReadSeq: "5"}},
	}}
	h := newTestRouter(t, svc)

	rec := do(t, h, http.MethodGet, "/v1/conversations/1/receipts", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if svc.getReceiptsCmd.ConversationID != 1 {
		t.Errorf("cmd = %+v, want conv 1", svc.getReceiptsCmd)
	}
}
