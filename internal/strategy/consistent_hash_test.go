package strategy_test

import (
	"fmt"
	"math"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/arsanantha1890-prog/load_balancing/internal/backend"
	"github.com/arsanantha1890-prog/load_balancing/internal/strategy"
)

func chBackends(t *testing.T, n int) []*backend.Backend {
	t.Helper()
	out := make([]*backend.Backend, 0, n)
	for i := 0; i < n; i++ {
		b, err := backend.New(
			fmt.Sprintf("node-%d", i),
			fmt.Sprintf("http://127.0.0.1:%d", 9001+i),
			1, time.Second, time.Second,
		)
		if err != nil {
			t.Fatalf("backend.New: %v", err)
		}
		out = append(out, b)
	}
	return out
}

// The fundamental property of consistent hashing: the same key always goes to
// the same backend, as long as the backend set doesn't change.
func TestConsistentHashSameKeyGoesToSameBackend(t *testing.T) {
	s, err := strategy.Get("consistent_hash")
	if err != nil {
		t.Fatalf("strategy.Get: %v", err)
	}

	backends := chBackends(t, 5)

	// Pick the backend for /user/123 a hundred times. It must always be the same.
	req := httptest.NewRequest("GET", "/user/123", nil)
	first, _ := s.Pick(req, backends)

	for i := 0; i < 100; i++ {
		b, err := s.Pick(req, backends)
		if err != nil {
			t.Fatalf("Pick: %v", err)
		}
		if b.Name != first.Name {
			t.Fatalf("request %d went to %s, but the first went to %s — key affinity is broken",
				i, b.Name, first.Name)
		}
	}
	t.Logf("/user/123 consistently maps to %s", first.Name)
}

// Different keys should spread across backends, not all land on one.
func TestConsistentHashDistributesAcrossBackends(t *testing.T) {
	s, _ := strategy.Get("consistent_hash")
	backends := chBackends(t, 5)

	counts := map[string]int{}
	for i := 0; i < 1000; i++ {
		req := httptest.NewRequest("GET", fmt.Sprintf("/item/%d", i), nil)
		b, err := s.Pick(req, backends)
		if err != nil {
			t.Fatalf("Pick: %v", err)
		}
		counts[b.Name]++
	}

	// With 150 virtual nodes per backend, the distribution should be roughly
	// even. Allow 10%-30% per backend (perfect = 20%).
	for name, count := range counts {
		share := float64(count) / 1000
		if share < 0.10 || share > 0.30 {
			t.Errorf("backend %s got %.1f%% of keys; expected roughly 20%%", name, share*100)
		}
	}
	t.Logf("distribution across 1000 keys: %v", counts)
}

// When a backend is removed, most keys should still map to the same backend.
// This is the whole point of consistent hashing vs plain modulo.
func TestConsistentHashMinimalRemapping(t *testing.T) {
	s, _ := strategy.Get("consistent_hash")
	five := chBackends(t, 5)

	// Map 1000 keys with 5 backends.
	originalMapping := map[string]string{}
	for i := 0; i < 1000; i++ {
		path := fmt.Sprintf("/item/%d", i)
		req := httptest.NewRequest("GET", path, nil)
		b, _ := s.Pick(req, five)
		originalMapping[path] = b.Name
	}

	// Remove the last backend (simulate a crash).
	four := five[:4]

	remapped := 0
	for i := 0; i < 1000; i++ {
		path := fmt.Sprintf("/item/%d", i)
		req := httptest.NewRequest("GET", path, nil)
		b, _ := s.Pick(req, four)
		if b.Name != originalMapping[path] {
			remapped++
		}
	}

	// Ideal remapping = ~1/5 = 200 of 1000. Allow up to 350.
	if remapped > 350 {
		t.Errorf("%d of 1000 keys remapped when removing 1 of 5 backends; expected ~200",
			remapped)
	}
	t.Logf("%d of 1000 keys remapped (ideal ≈ 200)", remapped)
}

// The bounded-load property: when one backend is heavily loaded, requests that
// would normally hash to it spill to the next backend on the ring.
func TestBoundedLoadSpillsToNextBackend(t *testing.T) {
	s, _ := strategy.Get("consistent_hash")
	backends := chBackends(t, 3)

	// Find which backend /hot-key maps to.
	req := httptest.NewRequest("GET", "/hot-key", nil)
	target, _ := s.Pick(req, backends)

	// Load that backend well above the bound (epsilon=0.25, so bound ≈ ceil(avg*1.25)).
	// With 3 backends and 30 requests on one, avg=10, bound=ceil(12.5)=13.
	// The target has 30, which is far above 13 — it should spill.
	for i := 0; i < 30; i++ {
		target.IncInflight()
	}
	defer func() {
		for i := 0; i < 30; i++ {
			target.DecInflight()
		}
	}()

	spilledTo, _ := s.Pick(req, backends)
	if spilledTo.Name == target.Name {
		t.Fatalf("/hot-key still went to %s despite 30 in-flight; bounded load should have spilled",
			target.Name)
	}
	t.Logf("/hot-key spilled from %s (30 inflight) to %s", target.Name, spilledTo.Name)
}

// Under uniform load (no backend overloaded), bounded loads should not
// interfere — requests should still go to their hashed target.
func TestBoundedLoadDoesNotInterfereUnderUniformLoad(t *testing.T) {
	s, _ := strategy.Get("consistent_hash")
	backends := chBackends(t, 5)

	// No in-flight requests on any backend.
	req := httptest.NewRequest("GET", "/stable-key", nil)
	first, _ := s.Pick(req, backends)

	for i := 0; i < 50; i++ {
		b, _ := s.Pick(req, backends)
		if b.Name != first.Name {
			t.Fatalf("request %d went to %s instead of %s under zero load — bounded load is interfering when it shouldn't",
				i, b.Name, first.Name)
		}
	}
}

// Edge case: single backend must always be returned.
func TestConsistentHashSingleBackend(t *testing.T) {
	s, _ := strategy.Get("consistent_hash")
	backends := chBackends(t, 1)

	for i := 0; i < 50; i++ {
		req := httptest.NewRequest("GET", fmt.Sprintf("/path/%d", i), nil)
		b, err := s.Pick(req, backends)
		if err != nil || b.Name != "node-0" {
			t.Fatalf("single backend: got (%v, %v)", b, err)
		}
	}
}

// The registered strategy must be in the Available() list.
func TestConsistentHashIsRegistered(t *testing.T) {
	for _, name := range strategy.Available() {
		if name == "consistent_hash" {
			return
		}
	}
	t.Fatal("consistent_hash not found in strategy.Available()")
}

// Verify the distribution with many virtual nodes is reasonably uniform.
func TestConsistentHashUniformity(t *testing.T) {
	s, _ := strategy.Get("consistent_hash")
	backends := chBackends(t, 10)

	counts := map[string]int{}
	const total = 10000
	for i := 0; i < total; i++ {
		req := httptest.NewRequest("GET", fmt.Sprintf("/key/%d", i), nil)
		b, _ := s.Pick(req, backends)
		counts[b.Name]++
	}

	// Calculate coefficient of variation: stdev/mean. Should be < 0.3.
	mean := float64(total) / float64(len(backends))
	var variance float64
	for _, c := range counts {
		diff := float64(c) - mean
		variance += diff * diff
	}
	variance /= float64(len(backends))
	cv := math.Sqrt(variance) / mean

	if cv > 0.3 {
		t.Errorf("coefficient of variation %.3f is too high; distribution is uneven", cv)
	}
	t.Logf("CV = %.3f (lower is more uniform)", cv)
}
