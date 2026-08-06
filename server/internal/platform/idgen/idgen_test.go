package idgen

import (
	"sync"
	"testing"
	"time"
)

func mustGen(t *testing.T, nodeID int) *Generator {
	t.Helper()
	g, err := New(nodeID, DefaultEpoch)
	if err != nil {
		t.Fatalf("New(%d) error: %v", nodeID, err)
	}
	return g
}

func TestNewRejectsInvalidNodeID(t *testing.T) {
	for _, id := range []int{-1, 1024} {
		if _, err := New(id, DefaultEpoch); err == nil {
			t.Errorf("New(%d) expected error for out-of-range node id", id)
		}
	}
}

func TestNextIDIsPositiveAndTimeOrdered(t *testing.T) {
	g := mustGen(t, 1)
	base := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	g.SetClock(func() time.Time { return base })

	var prev int64
	for i := 0; i < 1000; i++ {
		id, err := g.NextID()
		if err != nil {
			t.Fatalf("NextID error: %v", err)
		}
		if id <= 0 {
			t.Fatalf("NextID() = %d, want positive", id)
		}
		if i > 0 && id <= prev {
			t.Fatalf("NextID not strictly increasing: %d after %d", id, prev)
		}
		prev = id
	}
}

func TestNextIDEmbedsNodeIDAndTimestamp(t *testing.T) {
	g := mustGen(t, 42)
	base := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	g.SetClock(func() time.Time { return base })

	id, err := g.NextID()
	if err != nil {
		t.Fatalf("NextID error: %v", err)
	}
	if got := (uint64(id) >> seqBits) & maxNodeID; got != 42 {
		t.Errorf("embedded node id = %d, want 42", got)
	}
	// First ID in a fresh millisecond has sequence 0.
	if got := uint64(id) & maxSeq; got != 0 {
		t.Errorf("embedded sequence = %d, want 0", got)
	}
}

func TestNextIDUniqueAcrossGoroutines(t *testing.T) {
	g := mustGen(t, 7)
	const workers, perWorker = 16, 2000

	var mu sync.Mutex
	seen := make(map[int64]struct{})
	var wg sync.WaitGroup
	errs := make(chan error, workers)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				id, err := g.NextID()
				if err != nil {
					errs <- err
					return
				}
				mu.Lock()
				seen[id] = struct{}{}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent NextID error: %v", err)
	}
	if want := workers * perWorker; len(seen) != want {
		t.Errorf("generated %d unique ids, want %d (collisions)", len(seen), want)
	}
}

func TestNextIDFailsOnClockSkew(t *testing.T) {
	g := mustGen(t, 1)
	start := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	backwards := start.Add(-time.Second)
	g.SetClock(func() time.Time { return start })

	if _, err := g.NextID(); err != nil {
		t.Fatalf("first NextID error: %v", err)
	}
	g.SetClock(func() time.Time { return backwards })
	if _, err := g.NextID(); err == nil {
		t.Fatal("expected error when clock moves backwards")
	}
}

func TestNextIDAfterSequenceOverflow(t *testing.T) {
	g := mustGen(t, 1)
	base := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	next := base.Add(time.Second)
	var calls int
	g.SetClock(func() time.Time {
		if calls < maxSeq+1 {
			return base
		}
		return next
	})

	var prev int64
	for i := 0; i <= maxSeq+1; i++ {
		id, err := g.NextID()
		if err != nil {
			t.Fatalf("NextID error at iteration %d: %v", i, err)
		}
		if i > 0 && id <= prev {
			t.Fatalf("NextID not increasing across sequence rollover: %d after %d", id, prev)
		}
		prev = id
		calls++
	}
}
