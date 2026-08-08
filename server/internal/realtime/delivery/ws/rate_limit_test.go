package ws

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/application"
	"github.com/AkaneSakuramori/socialmedia/server/internal/realtime/domain"
)

func TestTokenBucketRefills(t *testing.T) {
	now := time.Now()
	b := newTokenBucket(1, time.Second)
	if !b.take(now) {
		t.Fatal("bucket must start full")
	}
	if b.take(now) {
		t.Fatal("bucket must be empty after one take")
	}
	if !b.take(now.Add(2 * time.Second)) {
		t.Fatal("bucket must refill after the interval")
	}
}

func TestLimiterPerKey(t *testing.T) {
	l := newLimiter(nil)
	// ws_read: 1 per 500ms per conversation — two different conversations are
	// independent buckets.
	if !l.allow("read", "10") {
		t.Fatal("first read in conv 10 must pass")
	}
	if l.allow("read", "10") {
		t.Fatal("second read in conv 10 within 500ms must fail")
	}
	if !l.allow("read", "20") {
		t.Fatal("read in a different conversation must pass (per-key bucket)")
	}
}

func TestLimiterTypingBudget(t *testing.T) {
	l := newLimiter(func() time.Time { return time.Unix(100, 0) })
	if !l.allow("typing", "7") {
		t.Fatal("first typing must pass")
	}
	if l.allow("typing", "7") {
		t.Fatal("second typing within 2s must fail")
	}
}

func TestLimiterUnknownClassAlwaysPasses(t *testing.T) {
	l := newLimiter(nil)
	if !l.allow("bogus", "x") {
		t.Fatal("unknown class must not be throttled")
	}
}

func TestConnRateSustainedAbuse(t *testing.T) {
	cr := newConnRate()
	cr.now = func() time.Time { return time.Now() }
	for i := 0; i < rateLimitMaxViolations+1; i++ {
		cr.allow("read", "1") // bucket empty after the first take, so each fails
	}
	if !cr.abusing() {
		t.Fatal("connection must be flagged abusing at the threshold")
	}
}

func TestHandlerRateLimitAcksThenCloses(t *testing.T) {
	chat := &fakeChatService{
		onMarkRead: func(_ context.Context, cmd application.MarkReadCommand) (*application.ReceiptResult, error) {
			return &application.ReceiptResult{}, nil
		},
	}
	hub, client := dialTestHandler(t, newTestHandler(chat))
	_ = hub

	// Blast receipt.read frames in the same conversation faster than the
	// 500ms/1 budget. The handler acks RATE_LIMITED and, after the sustained
	// abuse threshold, closes with 4501.
	for i := 0; i < rateLimitMaxViolations+2; i++ {
		writeFrame(t, client, domain.EventReceiptRead, "rl-"+strconv.Itoa(i), `{"conversation_id":2001,"last_read_seq":1}`)
	}

	// Drain acks until the socket closes.
	deadline := time.Now().Add(5 * time.Second)
	gotRateLimit, gotClose := 0, false
	for time.Now().Before(deadline) {
		f, err := readFrameAllowClose(t, client)
		if err != nil {
			if websocket.CloseStatus(err) == websocket.StatusCode(domain.CloseProtocol) {
				gotClose = true
				break
			}
			t.Fatalf("read: %v", err)
		}
		if f.Type == domain.EventServerAck {
			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := jsonUnmarshal(f.Data, &body); err != nil {
				continue
			}
			if body.Error.Code == "RATE_LIMITED" {
				gotRateLimit++
			}
		}
	}
	if !gotClose {
		t.Fatal("socket must be closed after sustained rate-limit abuse")
	}
	if gotRateLimit == 0 {
		t.Fatal("expected at least one RATE_LIMITED ack")
	}
}

// readFrameAllowClose is like readFrame but returns the close error instead of
// failing the test.
func readFrameAllowClose(t *testing.T, c *websocket.Conn) (*domain.Frame, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, b, err := c.Read(ctx)
	if err != nil {
		return nil, err
	}
	return domain.Decode(b)
}
