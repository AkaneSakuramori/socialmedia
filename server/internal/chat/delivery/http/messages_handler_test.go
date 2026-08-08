package http

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/application"
	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/domain"
)

func stringID(n int64) string { return strconv.FormatInt(n, 10) }

// ---- POST /v1/conversations/{id}/messages (§8.2) ----

func TestSendMessage201AndReplay200(t *testing.T) {
	svc := &fakeService{
		sendRes: &application.SendMessageResult{
			View:    application.MessageView{ID: "5001", ConversationID: "1", Sequence: "1", Type: "text"},
			Created: true,
		},
	}
	h := newTestRouter(t, svc)

	rec := do(t, h, http.MethodPost, "/v1/conversations/1/messages",
		`{"client_msg_id":"cm-9","type":"text","content":{"text":"On my way"}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	if svc.sendCmd.UserID != 42 || svc.sendCmd.ConversationID != 1 {
		t.Errorf("cmd = %+v, want user 42 / conv 1", svc.sendCmd)
	}
	if svc.sendCmd.ClientMsgID != "cm-9" || svc.sendCmd.Text == nil || *svc.sendCmd.Text != "On my way" {
		t.Errorf("cmd = %+v, want cm-9 / text", svc.sendCmd)
	}

	// Idempotent replay returns the original with 200. Use a distinct HTTP
	// idempotency key so the DB-level replay (Created=false) is exercised, not
	// the HTTP response cache.
	svc.sendRes.Created = false
	req := httptest.NewRequest(http.MethodPost, "/v1/conversations/1/messages",
		bytes.NewReader([]byte(`{"client_msg_id":"cm-9","type":"text","content":{"text":"On my way"}}`)))
	req.Header.Set("Authorization", "Bearer token-1")
	req.Header.Set("X-Device-Id", "dev-1")
	req.Header.Set("Idempotency-Key", "req-replay-1")
	replayRec := httptest.NewRecorder()
	h.ServeHTTP(replayRec, req)
	if replayRec.Code != http.StatusOK {
		t.Fatalf("replay status = %d, want 200", replayRec.Code)
	}
}

func TestSendMessageReplyAndMentionsParsing(t *testing.T) {
	svc := &fakeService{sendRes: &application.SendMessageResult{View: application.MessageView{ID: "5001"}, Created: true}}
	h := newTestRouter(t, svc)

	rec := do(t, h, http.MethodPost, "/v1/conversations/1/messages",
		`{"client_msg_id":"cm-9","type":"text","content":{"text":"hey"},`+
			`"reply_to_seq":"410","mentions":["1003"]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	if svc.sendCmd.ReplyToSeq == nil || *svc.sendCmd.ReplyToSeq != 410 {
		t.Errorf("reply_to_seq = %v, want 410", svc.sendCmd.ReplyToSeq)
	}
	if len(svc.sendCmd.Mentions) != 1 || svc.sendCmd.Mentions[0] != 1003 {
		t.Errorf("mentions = %v, want [1003]", svc.sendCmd.Mentions)
	}
}

func TestSendMessageInvalidReplySeq(t *testing.T) {
	h := newTestRouter(t, &fakeService{})
	rec := do(t, h, http.MethodPost, "/v1/conversations/1/messages",
		`{"client_msg_id":"cm-9","type":"text","content":{"text":"hey"},"reply_to_seq":"abc"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
}

// ---- GET /v1/conversations/{id}/messages (§8.1) ----

func TestListMessages(t *testing.T) {
	svc := &fakeService{
		listMessagesRes: &application.MessageListResult{
			Items:   []application.MessageView{{ID: "5001", Sequence: "1"}},
			HasMore: true,
			Limit:   50,
		},
	}
	h := newTestRouter(t, svc)

	rec := do(t, h, http.MethodGet, "/v1/conversations/1/messages?before=10&limit=5", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if svc.listMessagesCmd.ConversationID != 1 || svc.listMessagesCmd.BeforeSeq == nil || *svc.listMessagesCmd.BeforeSeq != 10 {
		t.Errorf("cmd = %+v, want conv 1 / before 10", svc.listMessagesCmd)
	}
	if svc.listMessagesCmd.Limit != 5 {
		t.Errorf("limit = %d, want 5", svc.listMessagesCmd.Limit)
	}

	var env struct {
		Data       []json.RawMessage `json:"data"`
		Pagination struct {
			NextCursor *string `json:"next_cursor"`
			HasMore    bool    `json:"has_more"`
		} `json:"pagination"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if len(env.Data) != 1 || !env.Pagination.HasMore {
		t.Errorf("envelope = %+v, want 1 item + has_more", env)
	}
}

// ---- GET/PATCH/DELETE /v1/messages/{id} (§8.3–§8.5) ----

func TestGetEditDeleteMessage(t *testing.T) {
	svc := &fakeService{
		getMessageRes:    &application.MessageView{ID: "5001", Type: "text"},
		editMessageRes:   &application.MessageView{ID: "5001", Type: "text"},
		deleteMessageRes: &application.DeleteMessageResult{Deleted: "all", MessageID: "5001"},
	}
	h := newTestRouter(t, svc)

	getRec := do(t, h, http.MethodGet, "/v1/messages/5001", "")
	if getRec.Code != http.StatusOK || svc.getMessageCmd.MessageID != 5001 {
		t.Errorf("get: status=%d cmd=%+v", getRec.Code, svc.getMessageCmd)
	}

	editRec := do(t, h, http.MethodPatch, "/v1/messages/5001", `{"content":{"text":"revised"}}`)
	if editRec.Code != http.StatusOK || svc.editMessageCmd.NewText != "revised" {
		t.Errorf("edit: status=%d cmd=%+v", editRec.Code, svc.editMessageCmd)
	}

	delRec := do(t, h, http.MethodDelete, "/v1/messages/5001", `{"mode":"all"}`)
	if delRec.Code != http.StatusOK || svc.deleteMessageCmd.Mode != "all" {
		t.Errorf("delete: status=%d cmd=%+v", delRec.Code, svc.deleteMessageCmd)
	}
}

// ---- Reactions (§8.6–§8.8) ----

func TestReactionRoutes(t *testing.T) {
	svc := &fakeService{
		addReactionRes:    &application.ReactionResult{MessageID: "5001", Emoji: "👍", Count: 2},
		removeReactionRes: &application.ReactionResult{MessageID: "5001", Emoji: "👍", Count: 1},
		listReactionsRes: &application.ReactionsResult{
			Emoji:    "👍",
			Reactors: []application.ReactorView{{UserID: "1001", DisplayName: "Aya"}},
		},
	}
	h := newTestRouter(t, svc)

	emoji := "%F0%9F%91%8D" // URL-encoded 👍

	addRec := do(t, h, http.MethodPut, "/v1/messages/5001/reactions/"+emoji, "")
	if addRec.Code != http.StatusOK || svc.addReactionCmd.Emoji != "👍" || svc.addReactionCmd.MessageID != 5001 {
		t.Errorf("add: status=%d cmd=%+v", addRec.Code, svc.addReactionCmd)
	}

	delRec := do(t, h, http.MethodDelete, "/v1/messages/5001/reactions/"+emoji, "")
	if delRec.Code != http.StatusOK || svc.removeReactionCmd.Emoji != "👍" {
		t.Errorf("remove: status=%d cmd=%+v", delRec.Code, svc.removeReactionCmd)
	}

	listRec := do(t, h, http.MethodGet, "/v1/messages/5001/reactions?emoji=%F0%9F%91%8D", "")
	if listRec.Code != http.StatusOK || svc.listReactionsCmd.Emoji != "👍" {
		t.Errorf("list: status=%d cmd=%+v", listRec.Code, svc.listReactionsCmd)
	}
}

func TestHandlerMapsMessageErrors(t *testing.T) {
	svc := &fakeService{
		sendErr:        domain.ErrMentionNotMember,
		getMessageErr:  domain.ErrMessageNotFound,
		editMessageErr: domain.ErrEditWindowExpired,
		addReactionErr: domain.ErrReactionLimit,
	}
	h := newTestRouter(t, svc)

	rec := do(t, h, http.MethodPost, "/v1/conversations/1/messages",
		`{"client_msg_id":"cm-9","type":"text","content":{"text":"hi"},"mentions":["9999"]}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("mention: status = %d, want 422", rec.Code)
	}

	rec = do(t, h, http.MethodGet, "/v1/messages/99999", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("get: status = %d, want 404", rec.Code)
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Code != "MESSAGE_NOT_FOUND" {
		t.Errorf("get code = %q, want MESSAGE_NOT_FOUND", body.Code)
	}

	rec = do(t, h, http.MethodPatch, "/v1/messages/5001", `{"content":{"text":"x"}}`)
	if rec.Code != http.StatusForbidden {
		t.Errorf("edit: status = %d, want 403", rec.Code)
	}

	rec = do(t, h, http.MethodPut, "/v1/messages/5001/reactions/%F0%9F%91%8D", "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("reaction limit: status = %d, want 422", rec.Code)
	}
}
