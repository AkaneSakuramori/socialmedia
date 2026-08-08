//go:build integration

package ws

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/AkaneSakuramori/socialmedia/server/internal/platform/observability"
	"github.com/AkaneSakuramori/socialmedia/server/internal/realtime/domain"
	"github.com/AkaneSakuramori/socialmedia/server/internal/realtime/infra"
	"github.com/AkaneSakuramori/socialmedia/server/internal/realtime/presence"
	"github.com/AkaneSakuramori/socialmedia/server/internal/realtime/typing"
)

func integBackplane(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	rc := redis.NewClient(&redis.Options{Addr: addr})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rc.Ping(ctx).Err(); err != nil {
		t.Skipf("redis not reachable at %s, skipping: %v", addr, err)
	}
	t.Cleanup(func() { _ = rc.Close() })
	return rc
}

// startDispatcher runs a dispatcher over the real Redis backplane, returning a
// cancel that stops it.
func startDispatcher(t *testing.T, hub *Hub, replay *replayBuffer) (context.CancelFunc, *redis.Client) {
	t.Helper()
	rc := integBackplane(t)
	ctx, cancel := context.WithCancel(context.Background())
	d := NewDispatcher(hub, replay, observability.NewLogger("test"))
	go func() {
		_ = d.Run(ctx, rc)
	}()
	t.Cleanup(cancel)
	return cancel, rc
}

// publishUntilReceived publishes fn repeatedly until the connection's send
// buffer yields a frame of the expected type. Redis pub/sub delivers only to
// subscribers registered at publish time, and other subscribers (the running
// container's dispatcher) can satisfy a naive subscriber count, so the tests
// must retry until their own subscriber has actually delivered.
func publishUntilReceived(t *testing.T, rc *redis.Client, conn *Connection, fn func(), expectType string, timeout time.Duration) *domain.Frame {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		fn()
		select {
		case raw := <-conn.send:
			f, err := domain.Decode(raw)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if f.Type == expectType {
				return f
			}
		case <-time.After(150 * time.Millisecond):
		}
	}
	t.Fatalf("never received %s over the backplane", expectType)
	return nil
}

func TestIntegrationBackplanePresenceReachesSubscriber(t *testing.T) {
	hub, _ := newDispatcherTestHub(t)
	cancel, rc := startDispatcher(t, hub, newReplayBuffer(DefaultReplayConfig()))
	defer cancel()

	log := observability.NewLogger("test")
	notifier := NewPresenceNotifier(infra.NewRedisPublisher(rc), log)
	notify := func() {
		notifier.NotifyPresence(context.Background(), presence.ChangeEvent{
			UserID:          1002,
			Status:          "busy",
			CustomStatus:    "deep work",
			ConversationIDs: []int64{2001},
		})
	}

	f := publishUntilReceived(t, rc, hub.conns["c-d"], notify, domain.EventPresenceChanged, 10*time.Second)
	var pbody struct {
		UserID string `json:"user_id"`
	}
	if err := json.Unmarshal(f.Data, &pbody); err != nil {
		t.Fatalf("presence.changed data: %v", err)
	}
	if pbody.UserID != "1002" {
		t.Errorf("presence user_id = %q, want 1002", pbody.UserID)
	}
}

func TestIntegrationBackplaneTypingReachesSubscriber(t *testing.T) {
	hub, _ := newDispatcherTestHub(t)
	cancel, rc := startDispatcher(t, hub, newReplayBuffer(DefaultReplayConfig()))
	defer cancel()

	log := observability.NewLogger("test")
	notifier := NewTypingNotifier(infra.NewRedisPublisher(rc), log)
	notify := func() {
		notifier.NotifyTyping(context.Background(), typing.ChangeEvent{
			UserID:         1002,
			ConversationID: 2001,
			Status:         "typing",
		})
	}

	f := publishUntilReceived(t, rc, hub.conns["c-d"], notify, domain.EventTypingIndicator, 10*time.Second)
	var tbody struct {
		ConversationID string `json:"conversation_id"`
		UserID         string `json:"user_id"`
		Status         string `json:"status"`
	}
	if err := json.Unmarshal(f.Data, &tbody); err != nil {
		t.Fatalf("typing.indicator data: %v", err)
	}
	if tbody.UserID != "1002" || tbody.Status != "typing" {
		t.Errorf("typing = %+v, want user 1002 typing", tbody)
	}
}

func TestIntegrationResumeReplaysGapOverBackplane(t *testing.T) {
	// Dispatcher and handler share one replay buffer. Durable events committed
	// to the backplane are buffered; a resuming connection replays the gap.
	replay := newReplayBuffer(DefaultReplayConfig())
	cancel, rc := startDispatcher(t, NewHub(DefaultConfig(), newRecordingHandler(), observability.NewLogger("test")), replay)
	defer cancel()

	conv := int64(2001)
	encode := func(seq int64) []byte {
		e := domain.Event{
			GlobalSeq:      seq,
			EventType:      domain.EventMessageCreated,
			ConversationID: &conv,
			Payload:        []byte(`{"id":1,"conversation_id":2001}`),
		}
		b, err := e.Encode()
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		return b
	}

	// Re-publish until the dispatcher has buffered all three (pub/sub only
	// delivers to subscribers registered at publish time; duplicates collapse
	// in the dispatcher's dedupe set).
	deadline := time.Now().Add(10 * time.Second)
	for {
		for i := int64(11); i <= 13; i++ {
			_ = rc.Publish(context.Background(), dispatcherChannel, encode(i)).Err()
		}
		if len(replay.since(conv, 0)) == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("dispatcher never buffered the events (have %d)",
				len(replay.since(conv, 0)))
		}
		time.Sleep(50 * time.Millisecond)
	}

	// A fresh connection resuming from last_global_seq 10 replays 11–13.
	h := NewHandler(&fakeChatService{}, observability.NewLogger("test")).WithReplayer(replay)
	_, client := dialTestHandler(t, h)
	writeFrame(t, client, domain.EventResume, "op-1", `{"last_seq":42,"last_global_seq":10,"session_id":7001}`)
	f := readFrame(t, client)
	if f.Type != domain.EventResumeAck {
		t.Fatalf("type = %q, want resume_ack", f.Type)
	}
	if got := string(f.Data); len(got) == 0 {
		t.Fatal("resume_ack must carry the replay frames")
	}
}
