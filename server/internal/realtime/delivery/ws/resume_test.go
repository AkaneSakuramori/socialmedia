package ws

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/application"
	chatdomain "github.com/AkaneSakuramori/socialmedia/server/internal/chat/domain"
	"github.com/AkaneSakuramori/socialmedia/server/internal/platform/observability"
	"github.com/AkaneSakuramori/socialmedia/server/internal/realtime/domain"
	"github.com/AkaneSakuramori/socialmedia/server/internal/realtime/presence"
	"github.com/AkaneSakuramori/socialmedia/server/internal/realtime/typing"
)

// seedReplay appends n message.created events to a replay buffer under conv.
func seedReplay(b *replayBuffer, conv int64, from, n int64) {
	for i := from; i < from+n; i++ {
		b.append(&domain.Event{
			GlobalSeq:      i,
			EventType:      "message.created",
			ConversationID: &conv,
			Payload:        []byte(`{"id":1,"conversation_id":2001,"sequence":1}`),
		})
	}
}

func TestHandlerResumeReplaysGap(t *testing.T) {
	replay := newReplayBuffer(ReplayConfig{MaxEventsPerConversation: 10, TTL: time.Hour})
	conv := int64(2001)
	seedReplay(replay, conv, 11, 3) // global_seqs 11, 12, 13

	h := NewHandler(&fakeChatService{}, observability.NewLogger("test")).WithReplayer(replay)
	_, client := dialTestHandler(t, h)

	writeFrame(t, client, domain.EventResume, "op-1", `{"last_seq":42,"last_global_seq":10,"session_id":7001}`)
	f := readFrame(t, client)
	if f.Type != domain.EventResumeAck {
		t.Fatalf("type = %q, want resume_ack", f.Type)
	}
	var body struct {
		ConnectionID string `json:"connection_id"`
		FromSeq      int64  `json:"from_seq"`
		GlobalSeq    int64  `json:"global_seq"`
		Replay       []struct {
			Type string          `json:"type"`
			Seq  int64           `json:"seq"`
			Data json.RawMessage `json:"data"`
		} `json:"replay"`
	}
	if err := jsonUnmarshal(f.Data, &body); err != nil {
		t.Fatalf("resume_ack data: %v", err)
	}
	if body.FromSeq != 42 {
		t.Errorf("from_seq = %d, want 42", body.FromSeq)
	}
	if len(body.Replay) != 3 {
		t.Fatalf("replay len = %d, want 3", len(body.Replay))
	}
	// Replay frames are the missed gap, in global_seq order, with fresh
	// contiguous per-connection seqs following the ack's own seq.
	if body.Replay[0].Type != domain.EventMessageCreated {
		t.Errorf("replay[0].type = %q, want message.created", body.Replay[0].Type)
	}
	if body.Replay[0].Seq != body.FromSeq && body.Replay[0].Seq != 2 {
		// ack is seq 1; replay frames are 2,3,4 (fresh connection).
		t.Errorf("replay seqs = %d/%d/%d, want contiguous after the ack",
			body.Replay[0].Seq, body.Replay[1].Seq, body.Replay[2].Seq)
	}
	if body.Replay[2].Seq-body.Replay[0].Seq != 2 {
		t.Errorf("replay seqs not contiguous: %d..%d", body.Replay[0].Seq, body.Replay[2].Seq)
	}
}

func TestHandlerResumeEmptyReplayWhenCaughtUp(t *testing.T) {
	replay := newReplayBuffer(ReplayConfig{MaxEventsPerConversation: 10, TTL: time.Hour})
	conv := int64(2001)
	seedReplay(replay, conv, 11, 2)

	h := NewHandler(&fakeChatService{}, observability.NewLogger("test")).WithReplayer(replay)
	_, client := dialTestHandler(t, h)

	// Client is caught up (acked beyond the buffer): valid resume, empty replay.
	writeFrame(t, client, domain.EventResume, "op-1", `{"last_seq":5,"last_global_seq":99,"session_id":7001}`)
	f := readFrame(t, client)
	if f.Type != domain.EventResumeAck {
		t.Fatalf("type = %q, want resume_ack", f.Type)
	}
	var body struct {
		Replay []json.RawMessage `json:"replay"`
	}
	if err := jsonUnmarshal(f.Data, &body); err != nil {
		t.Fatalf("resume_ack data: %v", err)
	}
	if len(body.Replay) != 0 {
		t.Errorf("replay len = %d, want 0 (caught up)", len(body.Replay))
	}
}

func TestHandlerResumeSessionMismatchRejected(t *testing.T) {
	replay := newReplayBuffer(ReplayConfig{MaxEventsPerConversation: 10, TTL: time.Hour})
	conv := int64(2001)
	seedReplay(replay, conv, 11, 2)

	h := NewHandler(&fakeChatService{}, observability.NewLogger("test")).WithReplayer(replay)
	_, client := dialTestHandler(t, h)

	// Bound session is 7001; the resume claims 9999 → session_revoked.
	writeFrame(t, client, domain.EventResume, "op-1", `{"last_seq":1,"last_global_seq":10,"session_id":9999}`)
	f := readFrame(t, client)
	if f.Type != domain.EventResumeRejected {
		t.Fatalf("type = %q, want resume_rejected", f.Type)
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if err := jsonUnmarshal(f.Data, &body); err != nil {
		t.Fatalf("data: %v", err)
	}
	if body.Reason != "session_revoked" {
		t.Errorf("reason = %q, want session_revoked", body.Reason)
	}
}

func TestHandlerResumeGapOutsideReplayWindow(t *testing.T) {
	replay := newReplayBuffer(ReplayConfig{MaxEventsPerConversation: 10, TTL: time.Hour})
	conv := int64(2001)
	seedReplay(replay, conv, 5, 3) // ring floor is seq 5

	h := NewHandler(&fakeChatService{}, observability.NewLogger("test")).WithReplayer(replay)
	_, client := dialTestHandler(t, h)

	// Client cursor (2) predates the ring floor (5): events 3–4 were evicted,
	// the gap cannot be replayed → buffer_expired → full resync fallback.
	writeFrame(t, client, domain.EventResume, "op-1", `{"last_seq":1,"last_global_seq":2,"session_id":7001}`)
	f := readFrame(t, client)
	if f.Type != domain.EventResumeRejected {
		t.Fatalf("type = %q, want resume_rejected", f.Type)
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if err := jsonUnmarshal(f.Data, &body); err != nil {
		t.Fatalf("data: %v", err)
	}
	if body.Reason != "buffer_expired" {
		t.Errorf("reason = %q, want buffer_expired", body.Reason)
	}
}

func TestHandlerResumeNegativeCursorClosesSocket(t *testing.T) {
	replay := newReplayBuffer(ReplayConfig{MaxEventsPerConversation: 10, TTL: time.Hour})
	conv := int64(2001)
	seedReplay(replay, conv, 11, 2)

	h := NewHandler(&fakeChatService{}, observability.NewLogger("test")).WithReplayer(replay)
	hub, client := dialTestHandler(t, h)

	writeFrame(t, client, domain.EventResume, "op-1", `{"last_seq":-1,"last_global_seq":10,"session_id":7001}`)
	_, _, err := client.Read(context.Background())
	if err == nil {
		t.Fatal("negative resume cursor must be a protocol violation")
	}
	waitConnCount(t, hub, 0)
}

func TestHandlerResumeNoReplayConfiguredRejected(t *testing.T) {
	// Legacy behavior: no replay source wired → always rejected.
	h := NewHandler(&fakeChatService{}, observability.NewLogger("test"))
	_, client := dialTestHandler(t, h)

	writeFrame(t, client, domain.EventResume, "op-1", `{"last_seq":1,"last_global_seq":10,"session_id":7001}`)
	f := readFrame(t, client)
	if f.Type != domain.EventResumeRejected {
		t.Fatalf("type = %q, want resume_rejected", f.Type)
	}
}

// newRecordingEphemeral builds typing+presence services over miniredis with a
// recording notifier, so handler tests can assert fan-out without Redis.
func newRecordingEphemeral(t *testing.T) (*typing.Service, *presence.Service, *recordingEphemeralNotifier) {
	t.Helper()
	n := &recordingEphemeralNotifier{}

	tr := newTypingTestStore(t)
	ts := typing.NewService(tr, typing.DefaultConfig(), n, observability.NewLogger("test"))

	pr := newPresenceTestStore(t)
	pc := presence.DefaultConfig()
	pc.Instance = "node-0"
	ps := presence.NewService(pr, pc, n, observability.NewLogger("test"))
	return ts, ps, n
}

func TestHandlerTypingStartBroadcastsAndAcks(t *testing.T) {
	chat := &fakeChatService{
		onGetConversation: func(context.Context, application.GetConversationCommand) (*application.ConversationDetail, error) {
			return &application.ConversationDetail{}, nil
		},
	}
	ts, ps, _ := newRecordingEphemeral(t)
	h := NewHandler(chat, observability.NewLogger("test")).WithTyping(ts).WithPresence(ps)
	_, client := dialTestHandler(t, h)

	writeFrame(t, client, domain.EventTypingStart, "op-1", `{"conversation_id":2001}`)
	f := readFrame(t, client)
	if f.Type != domain.EventServerAck {
		t.Fatalf("type = %q, want ack", f.Type)
	}
	// A "stopped" must follow the same path.
	writeFrame(t, client, domain.EventTypingStop, "op-2", `{"conversation_id":2001}`)
	if f := readFrame(t, client); f.Type != domain.EventServerAck {
		t.Fatalf("stop type = %q, want ack", f.Type)
	}
}

func TestHandlerTypingNonMemberNoBroadcast(t *testing.T) {
	chat := &fakeChatService{
		onGetConversation: func(context.Context, application.GetConversationCommand) (*application.ConversationDetail, error) {
			return nil, chatdomain.ErrNotMember
		},
	}
	ts, _, n := newRecordingEphemeral(t)
	h := NewHandler(chat, observability.NewLogger("test")).WithTyping(ts)
	_, client := dialTestHandler(t, h)

	writeFrame(t, client, domain.EventTypingStart, "op-1", `{"conversation_id":2001}`)
	f := readFrame(t, client)
	if f.Type != domain.EventServerAck {
		t.Fatalf("type = %q, want ack (NOT_A_MEMBER keeps socket open)", f.Type)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.typing) != 0 {
		t.Errorf("typing broadcasts = %d, want 0 for a non-member", len(n.typing))
	}
}

func TestHandlerPresenceUpdateBroadcasts(t *testing.T) {
	chat := &fakeChatService{}
	ts, ps, n := newRecordingEphemeral(t)
	h := NewHandler(chat, observability.NewLogger("test")).WithTyping(ts).WithPresence(ps)
	_, client := dialTestHandler(t, h)

	writeFrame(t, client, domain.EventPresenceUpdate, "op-1", `{"status":"busy","custom_status":"focusing"}`)
	if f := readFrame(t, client); f.Type != domain.EventServerAck {
		t.Fatalf("type = %q, want ack", f.Type)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.presence) != 1 || n.presence[0].Status != "busy" || n.presence[0].CustomStatus != "focusing" {
		t.Errorf("presence changes = %+v, want one busy/focusing", n.presence)
	}
}

func TestHandlerPresenceUpdateInvalidStatus(t *testing.T) {
	chat := &fakeChatService{}
	ts, ps, n := newRecordingEphemeral(t)
	h := NewHandler(chat, observability.NewLogger("test")).WithTyping(ts).WithPresence(ps)
	_, client := dialTestHandler(t, h)

	writeFrame(t, client, domain.EventPresenceUpdate, "op-1", `{"status":"zombie"}`)
	f := readFrame(t, client)
	if f.Type != domain.EventServerAck {
		t.Fatalf("type = %q, want ack (validation error)", f.Type)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.presence) != 0 {
		t.Errorf("invalid status must not broadcast presence (got %+v)", n.presence)
	}
}
