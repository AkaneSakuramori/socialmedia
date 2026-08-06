// Package health implements the liveness and readiness probes required by
// DEVOPS.md §5 and ENGINEERING.md §1217: /healthz (process alive) and /readyz
// (dependencies reachable). Readiness failures mean the instance is not
// serving; load balancers and orchestrators use these endpoints to gate traffic.
package health

import (
	"context"
	"fmt"
	"sync"
)

// Checker reports whether a dependency is reachable.
type Checker interface {
	Check(context.Context) error
}

// CheckFunc adapts a function to the Checker interface.
type CheckFunc func(context.Context) error

// Check calls the underlying function.
func (f CheckFunc) Check(ctx context.Context) error { return f(ctx) }

// Result is the per-dependency readiness outcome.
type Result struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok | failing
	Error  string `json:"error,omitempty"`
}

// Registry holds the readiness checks registered at the composition root.
type Registry struct {
	mu     sync.RWMutex
	checks map[string]Checker
}

// NewRegistry returns an empty readiness registry.
func NewRegistry() *Registry {
	return &Registry{checks: make(map[string]Checker)}
}

// Register adds or replaces a named check.
func (r *Registry) Register(name string, c Checker) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checks[name] = c
}

// Ready runs every check in parallel (with the caller's timeout in ctx) and
// reports overall readiness plus per-dependency results.
func (r *Registry) Ready(ctx context.Context) (bool, []Result) {
	r.mu.RLock()
	names := make([]string, 0, len(r.checks))
	checkers := make([]Checker, 0, len(r.checks))
	for name, c := range r.checks {
		names = append(names, name)
		checkers = append(checkers, c)
	}
	r.mu.RUnlock()

	results := make([]Result, len(names))
	ok := true
	for i := range names {
		name, check := names[i], checkers[i]
		res := Result{Name: name, Status: "ok"}
		if err := check.Check(ctx); err != nil {
			res.Status = "failing"
			res.Error = err.Error()
			ok = false
		}
		results[i] = res
	}
	return ok, results
}

// Alive always reports the process as alive. Extension point for future
// liveness checks (e.g. goroutine count).
func (r *Registry) Alive() []Result {
	return []Result{{Name: "process", Status: "ok"}}
}

// ErrNotReady is returned when a readiness probe fails, for consistent error
// text in tests and logs.
func ErrNotReady(failed []Result) error {
	return fmt.Errorf("not ready: %d dependency check(s) failing", len(failed))
}
