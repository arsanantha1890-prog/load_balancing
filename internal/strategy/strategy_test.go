package strategy_test

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/arsanantha1890-prog/gobalance/internal/backend"
	"github.com/arsanantha1890-prog/gobalance/internal/strategy"
)

func testBackends(t *testing.T, n int) []*backend.Backend {
	t.Helper()
	out := make([]*backend.Backend, 0, n)
	for i := 0; i < n; i++ {
		b, err := backend.New(
			string(rune('a'+i)),
			"http://127.0.0.1:900"+string(rune('0'+i)),
			1, time.Second, time.Second,
		)
		if err != nil {
			t.Fatalf("backend.New: %v", err)
		}
		out = append(out, b)
	}
	return out
}

// A load balancing strategy is defined by its DISTRIBUTION, not by any single
// choice. So that is what we assert: over 3000 requests across 3 backends,
// round-robin must be exactly uniform. Not approximately — exactly.
func TestRoundRobinIsExactlyUniform(t *testing.T) {
	s, err := strategy.Get("round_robin")
	if err != nil {
		t.Fatalf("strategy.Get: %v", err)
	}

	backends := testBackends(t, 3)
	req := httptest.NewRequest("GET", "/", nil)

	counts := map[string]int{}
	const total = 3000
	for i := 0; i < total; i++ {
		b, err := s.Pick(req, backends)
		if err != nil {
			t.Fatalf("Pick returned error on iteration %d: %v", i, err)
		}
		counts[b.Name]++
	}

	for _, b := range backends {
		if counts[b.Name] != total/3 {
			t.Errorf("backend %s received %d requests, want exactly %d",
				b.Name, counts[b.Name], total/3)
		}
	}
}

// Round-robin must also visit backends in order, not just in equal totals.
func TestRoundRobinCyclesInOrder(t *testing.T) {
	s, _ := strategy.Get("round_robin")
	backends := testBackends(t, 3)
	req := httptest.NewRequest("GET", "/", nil)

	want := []string{"a", "b", "c", "a", "b", "c"}
	for i, w := range want {
		b, _ := s.Pick(req, backends)
		if b.Name != w {
			t.Fatalf("request %d went to %q, want %q", i, b.Name, w)
		}
	}
}

func TestEmptyPoolReturnsErrNoBackends(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	for _, name := range strategy.Available() {
		s, err := strategy.Get(name)
		if err != nil {
			t.Fatalf("strategy.Get(%q): %v", name, err)
		}
		if _, err := s.Pick(req, nil); err != strategy.ErrNoBackends {
			t.Errorf("strategy %q with empty pool returned %v, want ErrNoBackends", name, err)
		}
	}
}

// Random should touch every backend given enough draws. This is a smoke test,
// not a statistics exam — the odds of a false failure are astronomically small.
func TestRandomEventuallyUsesEveryBackend(t *testing.T) {
	s, _ := strategy.Get("random")
	backends := testBackends(t, 3)
	req := httptest.NewRequest("GET", "/", nil)

	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		b, _ := s.Pick(req, backends)
		seen[b.Name] = true
	}
	if len(seen) != 3 {
		t.Errorf("random reached only %d of 3 backends in 500 draws", len(seen))
	}
}

func TestUnknownStrategyIsAnError(t *testing.T) {
	if _, err := strategy.Get("does_not_exist"); err == nil {
		t.Fatal("expected an error for an unknown strategy name, got nil")
	}
}
