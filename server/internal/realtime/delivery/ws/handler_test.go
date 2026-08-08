package ws

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/application"
	chatdomain "github.com/AkaneSakuramori/socialmedia/server/internal/chat/domain"
	"github.com/AkaneSakuramori/socialmedia/server/internal/platform/observability"
	"github.com/AkaneSakuramori/socialmedia/server/internal/realtime/domain"
)

// fakeChatService is a minimal chat application.Service stub for handler tests.
// Only the methods the frame handler calls are exercised; the rest panic on
// touch.
type fakeChatService struct {
	application.Service
	onGetConversation func(ctx context.Context, cmd application.GetConversationCommand) (*application.ConversationDetail, error)
	onSendMessage     func(ctx context.Context, cmd application.SendMessageCommand) (*application.SendMessageResult, error)
	onEditMessage     func(ctx context.Context, cmd application.EditMessageCommand) (*application.MessageView, error)
	onDeleteMessage   func(ctx context.Context, cmd application.DeleteMessageCommand) (*application.DeleteMessageResult, error)
	onAddReaction     func(ctx context.Context, cmd application.ReactionCommand) (*application.ReactionResult, error)
	onRemoveReaction  func(ctx context.Context, cmd application.ReactionCommand) (*application.ReactionResult, error)
	onMarkRead        func(ctx context.Context, cmd application.MarkReadCommand) (*application.ReceiptResult, error)
}

func (f *fakeChatService) GetConversation(ctx context.Context, cmd application.GetConversationCommand) (*application.ConversationDetail, error) {
	if f.onGetConversation != nil {
		return f.onGetConversation(ctx, cmd)
	}
	panic("GetConversation not stubbed")
}

func (f *fakeChatService) SendMessage(ctx context.Context, cmd application.SendMessageCommand) (*application.SendMessageResult, error) {
	if f.onSendMessage != nil {
		return f.onSendMessage(ctx, cmd)
	}
	panic("SendMessage not stubbed")
}

func (f *fakeChatService) EditMessage(ctx context.Context, cmd application.EditMessageCommand) (*application.MessageView, error) {
	if f.onEditMessage != nil {
		return f.onEditMessage(ctx, cmd)
	}
	panic("EditMessage not stubbed")
}

func (f *fakeChatService) DeleteMessage(ctx context.Context, cmd application.DeleteMessageCommand) (*application.DeleteMessageResult, error) {
	if f.onDeleteMessage != nil {
		return f.onDeleteMessage(ctx, cmd)
	}
	panic("DeleteMessage not stubbed")
}

func (f *fakeChatService) AddReaction(ctx context.Context, cmd application.ReactionCommand) (*application.ReactionResult, error) {
	if f.onAddReaction != nil {
		return f.onAddReaction(ctx, cmd)
	}
	panic("AddReaction not stubbed")
}

func (f *fakeChatService) RemoveReaction(ctx context.Context, cmd application.ReactionCommand) (*application.ReactionResult, error) {
	if f.onRemoveReaction != nil {
		return f.onRemoveReaction(ctx, cmd)
	}
	panic("RemoveReaction not stubbed")
}

func (f *fakeChatService) MarkRead(ctx context.Context, cmd application.MarkReadCommand) (*application.ReceiptResult, error) {
	if f.onMarkRead != nil {
		return f.onMarkRead(ctx, cmd)
	}
	panic("MarkRead not stubbed")
}

func newTestHandler(chat *fakeChatService) *Handler {
	return NewHandler(chat, observability.NewLogger("test"))
}

// dialTestHandler opens a websocket against a hub wired with the given handler.
// The connection is bound to user 1001 / session 7001 and registered before the
// test proceeds.
func dialTestHandler(t *testing.T, h *Handler) (*Hub, *websocket.Conn) {
	t.Helper()
	cfg := DefaultConfig()
	hub := NewHub(cfg, h, observability.NewLogger("test"))
	ready := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{domain.Subprotocol}})
		if err != nil {
			return
		}
		c := newConnection("conn-h", 1001, 7001, "d-1", ws, hub)
		hub.register(c)
		close(ready)
		go func() { defer hub.unregister(c); c.Run(context.Background()) }()
	}))
	t.Cleanup(srv.Close)

	client, _, err := websocket.Dial(context.Background(), srv.URL, &websocket.DialOptions{
		Subprotocols: []string{domain.Subprotocol},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = client.CloseNow() })
	<-ready
	return hub, client
}

func writeFrame(t *testing.T, c *websocket.Conn, typ, id, data string) {
	t.Helper()
	if data == "" {
		data = "{}"
	}
	raw := `{"v":1,"id":"` + id + `","type":"` + typ + `","data":` + data + `}`
	if err := c.Write(context.Background(), websocket.MessageText, []byte(raw)); err != nil {
		t.Fatalf("write %s: %v", typ, err)
	}
}

func TestHandlerSubscribeVerifiesMembership(t *testing.T) {
	chat := &fakeChatService{
		onGetConversation: func(_ context.Context, cmd application.GetConversationCommand) (*application.ConversationDetail, error) {
			if cmd.ConversationID == 2001 {
				return &application.ConversationDetail{}, nil
			}
			return nil, chatdomain.ErrNotMember
		},
	}
	_, client := dialTestHandler(t, newTestHandler(chat))

	writeFrame(t, client, domain.EventSubscribe, "op-1", `{"conversation_ids":[2001,2002]}`)

	ack := readFrame(t, client)
	if ack.Type != domain.EventServerAck {
		t.Fatalf("type = %q, want ack", ack.Type)
	}
	var body struct {
		ID     string `json:"id"`
		Result struct {
			Subscribed []int64 `json:"subscribed"`
		} `json:"result"`
	}
	if err := jsonUnmarshal(ack.Data, &body); err != nil {
		t.Fatalf("ack data: %v", err)
	}
	if body.ID != "op-1" {
		t.Errorf("ack id = %q, want op-1", body.ID)
	}
	if len(body.Result.Subscribed) != 1 || body.Result.Subscribed[0] != 2001 {
		t.Errorf("subscribed = %v, want [2001] (2002 is not a member)", body.Result.Subscribed)
	}
}

func TestHandlerUnsubscribe(t *testing.T) {
	chat := &fakeChatService{}
	hub, client := dialTestHandler(t, newTestHandler(chat))

	// Subscribe first so there is a subscription to remove.
	chat.onGetConversation = func(_ context.Context, cmd application.GetConversationCommand) (*application.ConversationDetail, error) {
		return &application.ConversationDetail{}, nil
	}
	writeFrame(t, client, domain.EventSubscribe, "s-1", `{"conversation_ids":[2001]}`)
	readFrame(t, client)

	writeFrame(t, client, domain.EventUnsubscribe, "u-1", `{"conversation_id":2001}`)
	ack := readFrame(t, client)
	if ack.Type != domain.EventServerAck {
		t.Fatalf("type = %q, want ack", ack.Type)
	}

	// After unsubscribe the connection must not receive fan-out.
	writeFrame(t, client, domain.EventUnsubscribe, "u-2", `{"conversation_id":2001}`)
	readFrame(t, client)
	hub.DeliverToConversation(2001, domain.EventMessageCreated, map[string]any{"m": 1})
	// Give any spurious delivery time to arrive.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, _, err := client.Read(ctx)
	if err == nil {
		t.Fatal("received fan-out after unsubscribe")
	}
}

func TestHandlerSendMessageAck(t *testing.T) {
	chat := &fakeChatService{
		onSendMessage: func(_ context.Context, cmd application.SendMessageCommand) (*application.SendMessageResult, error) {
			return &application.SendMessageResult{
				View: application.MessageView{ID: "m-1", ConversationID: "2001"},
			}, nil
		},
	}
	_, client := dialTestHandler(t, newTestHandler(chat))

	writeFrame(t, client, domain.EventMessageSend, "op-1", `{"conversation_id":2001,"client_msg_id":"cm-1","type":"text","text":"hi"}`)
	ack := readFrame(t, client)
	if ack.Type != domain.EventServerAck {
		t.Fatalf("type = %q, want ack", ack.Type)
	}
	var body struct {
		Result struct {
			Status string `json:"status"`
		} `json:"result"`
	}
	if err := jsonUnmarshal(ack.Data, &body); err != nil {
		t.Fatalf("ack data: %v", err)
	}
	if body.Result.Status != "sent" {
		t.Errorf("status = %q, want sent", body.Result.Status)
	}
}

func TestHandlerBusinessErrorKeepsSocketOpen(t *testing.T) {
	chat := &fakeChatService{
		onSendMessage: func(context.Context, application.SendMessageCommand) (*application.SendMessageResult, error) {
			return nil, chatdomain.ErrNotMember
		},
	}
	hub, client := dialTestHandler(t, newTestHandler(chat))

	writeFrame(t, client, domain.EventMessageSend, "op-1", `{"conversation_id":9999,"client_msg_id":"cm-1","type":"text","text":"x"}`)
	ack := readFrame(t, client)
	if ack.Type != domain.EventServerAck {
		t.Fatalf("type = %q, want ack", ack.Type)
	}
	// Non-fatal: socket stays open.
	if hub.ConnCount() != 1 {
		t.Fatal("connection must stay open after a business error")
	}
}

func TestHandlerUnknownFrameClosesSocket(t *testing.T) {
	chat := &fakeChatService{}
	hub, client := dialTestHandler(t, newTestHandler(chat))

	if err := client.Write(context.Background(), websocket.MessageText, []byte(`{"v":1,"id":"x","type":"no.such.event"}`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Client must observe the 4502 close to complete the graceful handshake.
	_, _, err := client.Read(context.Background())
	if websocket.CloseStatus(err) != websocket.StatusCode(domain.CloseProtocol) {
		t.Fatalf("close status = %v, want 4502", err)
	}
	waitConnCount(t, hub, 0)
}

func TestHandlerPingGetsPong(t *testing.T) {
	chat := &fakeChatService{}
	_, client := dialTestHandler(t, newTestHandler(chat))

	if err := client.Write(context.Background(), websocket.MessageText, []byte(`{"v":1,"type":"ping","data":{"ts":1}}`)); err != nil {
		t.Fatalf("write ping: %v", err)
	}
	f := readFrame(t, client)
	if f.Type != domain.EventPong {
		t.Fatalf("type = %q, want pong", f.Type)
	}
}

func TestHandlerReactionAck(t *testing.T) {
	chat := &fakeChatService{
		onAddReaction: func(_ context.Context, cmd application.ReactionCommand) (*application.ReactionResult, error) {
			return &application.ReactionResult{MessageID: "m-1"}, nil
		},
	}
	_, client := dialTestHandler(t, newTestHandler(chat))

	writeFrame(t, client, domain.EventReactionAdd, "op-1", `{"message_id":42,"conversation_id":2001,"emoji":"thumbsup"}`)
	ack := readFrame(t, client)
	if ack.Type != domain.EventServerAck {
		t.Fatalf("type = %q, want ack", ack.Type)
	}
}

func TestHandlerReceiptReadAck(t *testing.T) {
	chat := &fakeChatService{
		onMarkRead: func(_ context.Context, cmd application.MarkReadCommand) (*application.ReceiptResult, error) {
			return &application.ReceiptResult{}, nil
		},
	}
	_, client := dialTestHandler(t, newTestHandler(chat))

	writeFrame(t, client, domain.EventReceiptRead, "op-1", `{"conversation_id":2001,"last_read_seq":10}`)
	ack := readFrame(t, client)
	if ack.Type != domain.EventServerAck {
		t.Fatalf("type = %q, want ack", ack.Type)
	}
}

func TestHandlerResumeRejected(t *testing.T) {
	chat := &fakeChatService{}
	_, client := dialTestHandler(t, newTestHandler(chat))

	writeFrame(t, client, domain.EventResume, "op-1", `{"last_seq":42,"last_global_seq":100,"session_id":7001}`)
	f := readFrame(t, client)
	if f.Type != domain.EventResumeRejected {
		t.Fatalf("type = %q, want resume_rejected", f.Type)
	}
}

// jsonUnmarshal decodes a frame payload without mutating the raw bytes.
func jsonUnmarshal(b []byte, v any) error {
	return json.Unmarshal(b, v)
}

func TestHandlerInvalidDataClosesSocket(t *testing.T) {
	chat := &fakeChatService{}
	hub, client := dialTestHandler(t, newTestHandler(chat))

	writeFrame(t, client, domain.EventSubscribe, "op-1", `{"conversation_ids":"not-an-array"}`)
	_, _, err := client.Read(context.Background())
	if websocket.CloseStatus(err) != websocket.StatusCode(domain.CloseProtocol) {
		t.Fatalf("close status = %v, want 4502", err)
	}
	waitConnCount(t, hub, 0)
	if hub.ConnCount() != 0 {
		t.Fatal("invalid frame data must close the socket")
	}
}

// waitConnCount polls until the hub has the expected connection count.
func waitConnCount(t *testing.T, hub *Hub, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if hub.ConnCount() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("connection count = %d, want %d", hub.ConnCount(), want)
}
