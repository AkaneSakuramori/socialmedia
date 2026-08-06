package health

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRegistryReady(t *testing.T) {
	reg := NewRegistry()
	reg.Register("postgres", CheckFunc(func(context.Context) error { return nil }))
	reg.Register("redis", CheckFunc(func(context.Context) error { return errors.New("connection refused") }))

	ok, results := reg.Ready(context.Background())
	if ok {
		t.Fatal("Ready() = true, want false with failing dependency")
	}
	if len(results) != 2 {
		t.Fatalf("Ready() results = %d, want 2", len(results))
	}
	byName := map[string]Result{}
	for _, r := range results {
		byName[r.Name] = r
	}
	if byName["postgres"].Status != "ok" {
		t.Error("postgres check should be ok")
	}
	if byName["redis"].Status != "failing" || !strings.Contains(byName["redis"].Error, "connection refused") {
		t.Errorf("redis check should fail with cause, got %+v", byName["redis"])
	}
}

func TestRegistryAllHealthy(t *testing.T) {
	reg := NewRegistry()
	reg.Register("postgres", CheckFunc(func(context.Context) error { return nil }))
	ok, results := reg.Ready(context.Background())
	if !ok {
		t.Fatal("Ready() = false, want true when all healthy")
	}
	if len(results) != 1 || results[0].Status != "ok" {
		t.Fatalf("unexpected results: %+v", results)
	}
}

func TestAlive(t *testing.T) {
	results := NewRegistry().Alive()
	if len(results) != 1 || results[0].Status != "ok" {
		t.Fatalf("Alive() = %+v, want process ok", results)
	}
}

func TestErrNotReady(t *testing.T) {
	if err := ErrNotReady([]Result{{Name: "redis", Status: "failing"}}); err == nil {
		t.Fatal("ErrNotReady() = nil, want error")
	}
}
