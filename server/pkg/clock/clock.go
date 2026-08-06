// Package clock provides an injectable time source (ENGINEERING.md §32.3:
// "inject a clock wherever time is business logic"). Tests replace the system
// clock with a stub so TTLs, expiry windows, and timestamps are deterministic.
package clock

import "time"

// Clock reports the current time. Service constructors accept a Clock instead
// of calling time.Now directly.
type Clock interface {
	Now() time.Time
}

type systemClock struct{}

// System returns the real wall clock. Production wiring passes this; tests
// pass a stub.
func System() Clock { return systemClock{} }

func (systemClock) Now() time.Time { return time.Now() }
