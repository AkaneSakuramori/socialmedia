package clock

import (
	"testing"
	"time"
)

func TestSystemClockReturnsCurrentTime(t *testing.T) {
	before := time.Now()
	got := System().Now()
	after := time.Now()
	if got.Before(before) || got.After(after) {
		t.Fatalf("System().Now() = %v, expected between %v and %v", got, before, after)
	}
}

type stubClock struct{ t time.Time }

func (s stubClock) Now() time.Time { return s.t }

func TestClockInterfaceIsSatisfiedByStub(t *testing.T) {
	want := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	var c Clock = stubClock{t: want}
	if got := c.Now(); !got.Equal(want) {
		t.Fatalf("stub clock Now() = %v, want %v", got, want)
	}
}
