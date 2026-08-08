package infra

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/AkaneSakuramori/socialmedia/server/internal/chat/domain"
	"github.com/AkaneSakuramori/socialmedia/server/internal/platform/observability"
	rt "github.com/AkaneSakuramori/socialmedia/server/internal/realtime/domain"
)

type fakeStore struct {
	mu      sync.Mutex
	rows    []domain.ChangeLogRow
	head    int64
	listErr error
	headErr error
	calls   int
}

func newFakeStore() *fakeStore {
	return &fakeStore{rows: make([]domain.ChangeLogRow, 0)}
}

func (s *fakeStore) Head(context.Context) (int64, error) {
	if s.headErr != nil {
		return 0, s.headErr
	}
	return s.head, nil
}

func (s *fakeStore) ListAfter(_ context.Context, after, limit int64) ([]domain.ChangeLogRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.listErr != nil {
		return nil, s.listErr
	}
	out := make([]domain.ChangeLogRow, 0)
	for _, r := range s.rows {
		if r.GlobalSeq > after && int64(len(out)) < limit {
			out = append(out, r)
		}
	}
	return out, nil
}

type fakePublisher struct {
	mu       sync.Mutex
	msgs     []pubMsg
	pubErr   error
	pubCount int
}

type pubMsg struct {
	channel string
	data    []byte
}

func (p *fakePublisher) Publish(_ context.Context, channel string, msg []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pubCount++
	if p.pubErr != nil {
		return p.pubErr
	}
	p.msgs = append(p.msgs, pubMsg{channel: channel, data: append([]byte(nil), msg...)})
	return nil
}

func (p *fakePublisher) channels() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, m := range p.msgs {
		out = append(out, m.channel)
	}
	return out
}

func seedStore(s *fakeStore, n int) {
	conv := int64(2001)
	for i := 1; i <= n; i++ {
		id := int64(i)
		s.rows = append(s.rows, domain.ChangeLogRow{
			GlobalSeq:      id,
			EventType:      "message.created",
			ConversationID: &conv,
			EntityID:       &id,
			ActorUserID:    &id,
			Payload:        []byte(`{"id":` + itoa(int(i)) + `}`),
		})
	}
	s.head = int64(n)
}

func TestRelayPublishesCommittedRowsInOrder(t *testing.T) {
	store := newFakeStore()
	seedStore(store, 3)
	store.head = 0 // relay starts at the empty head and publishes all rows
	pub := &fakePublisher{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := NewRelay(store, pub, RelayConfig{PollInterval: 10 * time.Millisecond, BatchSize: 10}, observability.NewLogger("test"))

	// Run until the relay has published all three rows.
	done := make(chan struct{})
	go func() {
		_ = r.Run(ctx)
		close(done)
	}()
	waitFor(t, func() bool {
		pub.mu.Lock()
		n := len(pub.msgs)
		pub.mu.Unlock()
		return n == 3
	}, "relay publishes all rows")

	cancel()
	<-done

	pub.mu.Lock()
	defer pub.mu.Unlock()
	if len(pub.msgs) != 3 {
		t.Fatalf("published %d, want 3", len(pub.msgs))
	}
	for i, m := range pub.msgs {
		if m.channel != relayChannel {
			t.Errorf("msg %d channel = %q, want %q", i, m.channel, relayChannel)
		}
		e, err := rt.DecodeEvent(m.data)
		if err != nil {
			t.Fatalf("decode %d: %v", i, err)
		}
		if e.GlobalSeq != int64(i+1) {
			t.Errorf("msg %d global_seq = %d, want %d (in order)", i, e.GlobalSeq, i+1)
		}
		if e.EventType != "message.created" {
			t.Errorf("msg %d type = %q", i, e.EventType)
		}
		if e.ConversationID == nil || *e.ConversationID != 2001 {
			t.Errorf("msg %d conversation = %v", i, e.ConversationID)
		}
	}
	if r.cursor != 3 {
		t.Errorf("relay cursor = %d, want 3", r.cursor)
	}
}

func TestRelayStartsAtHead(t *testing.T) {
	// Existing history (seq 1-5) must NOT be re-published: the relay starts at
	// the current head. New rows after that are published.
	store := newFakeStore()
	seedStore(store, 5)
	conv := int64(2001)
	store.rows = append(store.rows, domain.ChangeLogRow{
		GlobalSeq: 6, EventType: "message.created", ConversationID: &conv, Payload: []byte(`{"id":6}`),
	})
	// head = 5: rows 1-5 are existing history (skipped), row 6 is new.
	store.head = 5
	pub := &fakePublisher{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := NewRelay(store, pub, RelayConfig{PollInterval: 10 * time.Millisecond, BatchSize: 10}, observability.NewLogger("test"))

	done := make(chan struct{})
	go func() {
		_ = r.Run(ctx)
		close(done)
	}()
	waitFor(t, func() bool {
		pub.mu.Lock()
		n := len(pub.msgs)
		pub.mu.Unlock()
		return n == 1
	}, "relay publishes only new rows")

	cancel()
	<-done

	pub.mu.Lock()
	defer pub.mu.Unlock()
	if len(pub.msgs) != 1 {
		t.Fatalf("published %d, want 1 (existing history skipped)", len(pub.msgs))
	}
	e, _ := rt.DecodeEvent(pub.msgs[0].data)
	if e.GlobalSeq != 6 {
		t.Errorf("published global_seq = %d, want 6", e.GlobalSeq)
	}
}

func TestRelayContinuesAfterPublishError(t *testing.T) {
	store := newFakeStore()
	seedStore(store, 2)
	// Rows beyond the initial head (3, 4) are the ones the relay will publish.
	conv := int64(2001)
	store.rows = append(store.rows,
		domain.ChangeLogRow{GlobalSeq: 3, EventType: "message.created", ConversationID: &conv, Payload: []byte(`{"id":3}`)},
		domain.ChangeLogRow{GlobalSeq: 4, EventType: "message.created", ConversationID: &conv, Payload: []byte(`{"id":4}`)},
	)
	// head = 2: rows 1-2 are existing history (skipped), rows 3-4 are new.
	store.head = 2
	pub := &fakePublisher{pubErr: errBoom()}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := NewRelay(store, pub, RelayConfig{PollInterval: 10 * time.Millisecond, BatchSize: 10}, observability.NewLogger("test"))

	done := make(chan struct{})
	go func() {
		_ = r.Run(ctx)
		close(done)
	}()
	// Let a few failed polls happen, then clear the error.
	time.Sleep(50 * time.Millisecond)
	pub.mu.Lock()
	pub.pubErr = nil
	pub.mu.Unlock()

	waitFor(t, func() bool {
		pub.mu.Lock()
		n := len(pub.msgs)
		pub.mu.Unlock()
		return n == 2
	}, "relay recovers after publish error")

	cancel()
	<-done
}

func TestRelayStopsOnContextCancel(t *testing.T) {
	store := newFakeStore()
	seedStore(store, 1)
	pub := &fakePublisher{}
	ctx, cancel := context.WithCancel(context.Background())
	r := NewRelay(store, pub, RelayConfig{PollInterval: 10 * time.Millisecond, BatchSize: 10}, observability.NewLogger("test"))

	done := make(chan struct{})
	go func() { _ = r.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("relay did not stop on cancel")
	}
}

func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", msg)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

type boomErr struct{}

func (boomErr) Error() string { return "boom" }

func errBoom() error { return boomErr{} }
