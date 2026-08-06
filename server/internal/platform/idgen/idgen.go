// Package idgen implements the platform's snowflake-style 64-bit identifier
// generator (ARCHITECTURE.md §2.2, DATABASE.md §4 conventions, ENGINEERING.md
// §3.4). IDs are time-ordered (sortable), which keyset pagination relies on
// (API.md §2.6), and fit in a positive int64 so they serialize as decimal
// strings without JavaScript precision loss.
//
// Layout: 41 bits milliseconds since epoch | 10 bits node id | 12 bits sequence.
package idgen

import (
	"fmt"
	"sync"
	"time"
)

const (
	nodeBits  = 10
	seqBits   = 12
	maxNodeID = (1 << nodeBits) - 1 // 1023
	maxSeq    = (1 << seqBits) - 1  // 4095
)

// DefaultEpoch is the custom epoch (ms) anchored at the 2020-01-01T00:00:00Z
// boundary, giving ~69 years of IDs before the timestamp field overflows.
var DefaultEpoch = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

// Generator produces sortable, unique snowflake IDs for one node.
type Generator struct {
	nodeID uint64
	epoch  int64

	mu    sync.Mutex
	last  int64 // last timestamp (ms) issued
	seq   int64 // sequence for the current millisecond
	nowFn func() time.Time
}

// New creates a Generator for the given node id (0–1023). Two generators with
// the same node id and overlapping lifetimes can collide; every instance in a
// deployment must get a distinct node id (config APP_IDGEN_NODE_ID).
func New(nodeID int, epoch time.Time) (*Generator, error) {
	if nodeID < 0 || nodeID > maxNodeID {
		return nil, fmt.Errorf("idgen: node id %d out of range [0, %d]", nodeID, maxNodeID)
	}
	return &Generator{
		nodeID: uint64(nodeID),
		epoch:  epoch.UnixMilli(),
		nowFn:  time.Now,
	}, nil
}

// NextID returns the next unique, time-ordered ID.
//
// It fails only on clock skew (system time moving backwards) or if the node
// produces more than 4096 IDs in a single millisecond without the clock
// advancing. Both conditions must be surfaced rather than silently reused.
func (g *Generator) NextID() (int64, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	now := g.nowFn().UnixMilli()
	if now < g.last {
		return 0, fmt.Errorf("idgen: clock moved backwards (last=%d now=%d)", g.last, now)
	}

	if now == g.last {
		g.seq++
		if g.seq > maxSeq {
			// Burn the remainder of the millisecond, then retry with the new time.
			for now <= g.last {
				time.Sleep(time.Millisecond)
				now = g.nowFn().UnixMilli()
			}
			g.seq = 0
		}
	} else {
		g.seq = 0
	}
	g.last = now

	elapsed := now - g.epoch
	if elapsed < 0 || elapsed >= (1<<41) {
		return 0, fmt.Errorf("idgen: timestamp %d out of 41-bit epoch window", elapsed)
	}

	id := uint64(elapsed)<<(nodeBits+seqBits) | g.nodeID<<seqBits | uint64(g.seq)
	return int64(id), nil
}

// SetClock replaces the time source (test hook). It must be called before any
// concurrent use.
func (g *Generator) SetClock(now func() time.Time) { g.nowFn = now }
