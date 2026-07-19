package strategy_test

import (
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/arsanantha1890-prog/load_balancing/internal/backend"
	"github.com/arsanantha1890-prog/load_balancing/internal/strategy"
)

// weightedBackends builds n backends with the given weights, named b0, b1, ...
func weightedBackends(t *testing.T, weights ...int) []*backend.Backend {
	t.Helper()

	out := make([]*backend.Backend, 0, len(weights))
	for i, w := range weights {
		b, err := backend.New(
			fmt.Sprintf("b%d", i),
			fmt.Sprintf("http://127.0.0.1:%d", 9001+i),
			w, time.Second, time.Second,
		)
		if err != nil {
			t.Fatalf("backend.New: %v", err)
		}
		out = append(out, b)
	}
	return out
}

// setInflight drives a backend's in-flight counter to an exact value.
func setInflight(b *backend.Backend, n int) {
	for i := 0; i < n; i++ {
		b.IncInflight()
	}
}

func mustGet(t *testing.T, name string) strategy.Strategy {
	t.Helper()
	s, err := strategy.Get(name)
	if err != nil {
		t.Fatalf("strategy.Get(%q): %v", name, err)
	}
	return s
}

// --- weighted round robin -------------------------------------------------

func TestWRRRespectsWeightRatio(t *testing.T) {
	s := mustGet(t, "weighted_round_robin")
	backends := weightedBackends(t, 5, 1, 1)
	req := httptest.NewRequest("GET", "/", nil)

	counts := map[string]int{}
	const rounds = 100 // 100 * (5+1+1) = 700 requests, a whole number of cycles
	for i := 0; i < rounds*7; i++ {
		b, err := s.Pick(req, backends)
		if err != nil {
			t.Fatalf("Pick: %v", err)
		}
		counts[b.Name]++
	}

	want := map[string]int{"b0": 500, "b1": 100, "b2": 100}
	for name, w := range want {
		if counts[name] != w {
			t.Errorf("backend %s got %d requests, want exactly %d", name, counts[name], w)
		}
	}
}

// The whole point of SMOOTH weighted round-robin is the ordering. Naive weight
// expansion would produce b0,b0,b0,b0,b0,b1,b2 -- a burst of five. The smooth
// algorithm must never produce a long run of the same backend.
func TestWRRIsSmoothNotBursty(t *testing.T) {
	s := mustGet(t, "weighted_round_robin")
	backends := weightedBackends(t, 5, 1, 1)
	req := httptest.NewRequest("GET", "/", nil)

	last := ""
	run, maxRun := 0, 0
	for i := 0; i < 700; i++ {
		b, _ := s.Pick(req, backends)
		if b.Name == last {
			run++
		} else {
			run, last = 1, b.Name
		}
		if run > maxRun {
			maxRun = run
		}
	}

	if maxRun > 4 {
		t.Errorf("longest consecutive run was %d; smooth WRR should never burst like that", maxRun)
	}
	t.Logf("longest consecutive run: %d (naive weight expansion would give 5)", maxRun)
}

func TestWRRSingleBackend(t *testing.T) {
	s := mustGet(t, "weighted_round_robin")
	backends := weightedBackends(t, 3)
	req := httptest.NewRequest("GET", "/", nil)

	for i := 0; i < 10; i++ {
		b, err := s.Pick(req, backends)
		if err != nil || b.Name != "b0" {
			t.Fatalf("iteration %d: got (%v, %v), want b0", i, b, err)
		}
	}
}

// --- least connections ----------------------------------------------------

func TestLeastConnectionsPicksLeastLoaded(t *testing.T) {
	s := mustGet(t, "least_connections")
	backends := weightedBackends(t, 1, 1, 1)
	req := httptest.NewRequest("GET", "/", nil)

	setInflight(backends[0], 5)
	setInflight(backends[1], 1)
	setInflight(backends[2], 3)

	for i := 0; i < 50; i++ {
		b, err := s.Pick(req, backends)
		if err != nil {
			t.Fatalf("Pick: %v", err)
		}
		if b.Name != "b1" {
			t.Fatalf("picked %s with in-flight 5/1/3, want b1", b.Name)
		}
	}
}

// Weight normalises load: a backend with weight 4 and 4 in flight is exactly as
// busy as a backend with weight 1 and 1 in flight.
func TestLeastConnectionsRespectsWeight(t *testing.T) {
	s := mustGet(t, "least_connections")
	backends := weightedBackends(t, 1, 4)
	req := httptest.NewRequest("GET", "/", nil)

	setInflight(backends[0], 2) // load 2.0
	setInflight(backends[1], 4) // load 1.0

	b, _ := s.Pick(req, backends)
	if b.Name != "b1" {
		t.Errorf("picked %s; the weight-4 backend at load 1.0 should beat the weight-1 backend at load 2.0", b.Name)
	}
}

// With everything idle, every backend ties. If tie-breaking were biased the
// balancer would send all traffic to healthy[0] at low load -- a real bug that
// only shows up when the system is NOT busy.
func TestLeastConnectionsBreaksTiesUniformly(t *testing.T) {
	s := mustGet(t, "least_connections")
	backends := weightedBackends(t, 1, 1, 1)
	req := httptest.NewRequest("GET", "/", nil)

	counts := map[string]int{}
	const n = 3000
	for i := 0; i < n; i++ {
		b, _ := s.Pick(req, backends)
		counts[b.Name]++
	}

	for _, b := range backends {
		share := float64(counts[b.Name]) / float64(n)
		if share < 0.28 || share > 0.39 {
			t.Errorf("backend %s got %.1f%% of tied picks, want roughly 33%%", b.Name, share*100)
		}
	}
}

// --- power of two choices -------------------------------------------------

// With exactly two backends, P2C samples both, so its choice is deterministic.
// That makes the core comparison testable without any statistics.
func TestP2CAlwaysPicksLighterOfTwo(t *testing.T) {
	s := mustGet(t, "p2c")
	backends := weightedBackends(t, 1, 1)
	req := httptest.NewRequest("GET", "/", nil)

	setInflight(backends[0], 10)

	for i := 0; i < 200; i++ {
		b, err := s.Pick(req, backends)
		if err != nil {
			t.Fatalf("Pick: %v", err)
		}
		if b.Name != "b1" {
			t.Fatalf("iteration %d picked the busy backend %s", i, b.Name)
		}
	}
}

// With three backends P2C is probabilistic, but a heavily loaded backend only
// wins when it is sampled against another heavily loaded one -- which cannot
// happen here, so it should receive nothing.
func TestP2CAvoidsTheOverloadedBackend(t *testing.T) {
	s := mustGet(t, "p2c")
	backends := weightedBackends(t, 1, 1, 1)
	req := httptest.NewRequest("GET", "/", nil)

	setInflight(backends[2], 50)

	counts := map[string]int{}
	for i := 0; i < 1000; i++ {
		b, _ := s.Pick(req, backends)
		counts[b.Name]++
	}

	if counts["b2"] != 0 {
		t.Errorf("overloaded backend received %d requests, want 0", counts["b2"])
	}
	t.Logf("distribution with one overloaded backend: %v", counts)
}

func TestP2CSingleBackend(t *testing.T) {
	s := mustGet(t, "p2c")
	backends := weightedBackends(t, 1)
	req := httptest.NewRequest("GET", "/", nil)

	b, err := s.Pick(req, backends)
	if err != nil || b.Name != "b0" {
		t.Fatalf("got (%v, %v), want b0 with no error", b, err)
	}
}

// --- ewma -----------------------------------------------------------------

func TestEWMAImplementsObserver(t *testing.T) {
	s := mustGet(t, "ewma")
	if _, ok := s.(strategy.Observer); !ok {
		t.Fatal("EWMA must implement Observer -- it is the only strategy that needs measured latency")
	}
}

func TestEWMAPrefersTheFasterBackend(t *testing.T) {
	s := mustGet(t, "ewma")
	obs := s.(strategy.Observer)

	backends := weightedBackends(t, 1, 1)
	req := httptest.NewRequest("GET", "/", nil)

	// Teach it that b0 is slow and b1 is fast, mirroring gamma vs alpha.
	obs.OnFinish(backends[0], 92*time.Millisecond, nil)
	obs.OnFinish(backends[1], 13*time.Millisecond, nil)

	for i := 0; i < 200; i++ {
		b, err := s.Pick(req, backends)
		if err != nil {
			t.Fatalf("Pick: %v", err)
		}
		if b.Name != "b1" {
			t.Fatalf("iteration %d picked the slow backend %s", i, b.Name)
		}
	}
}

// Peak EWMA must jump immediately on a slow response rather than averaging it
// away. React instantly to bad news; decay gradually on good news.
func TestEWMAReactsInstantlyToSlowdown(t *testing.T) {
	s := mustGet(t, "ewma")
	obs := s.(strategy.Observer)
	backends := weightedBackends(t, 1, 1)

	obs.OnFinish(backends[0], 10*time.Millisecond, nil)
	obs.OnFinish(backends[0], 500*time.Millisecond, nil)

	e, ok := s.(interface {
		Score(string) (time.Duration, bool)
	})
	if !ok {
		t.Skip("Score accessor not available")
	}

	score, found := e.Score("b0")
	if !found {
		t.Fatal("no score recorded for b0")
	}
	if score < 400*time.Millisecond {
		t.Errorf("score is %v after a 500ms response; peak EWMA should jump straight up, not average down", score)
	}
}

// In-flight requests must raise the cost even when history says a backend is
// fast, otherwise the strategy is blind to bursts it just dispatched.
func TestEWMAAccountsForInflight(t *testing.T) {
	s := mustGet(t, "ewma")
	obs := s.(strategy.Observer)

	backends := weightedBackends(t, 1, 1)
	req := httptest.NewRequest("GET", "/", nil)

	// Identical history: both look equally fast.
	obs.OnFinish(backends[0], 10*time.Millisecond, nil)
	obs.OnFinish(backends[1], 10*time.Millisecond, nil)

	// But b0 already has 20 requests queued on it.
	setInflight(backends[0], 20)

	for i := 0; i < 100; i++ {
		b, _ := s.Pick(req, backends)
		if b.Name != "b1" {
			t.Fatalf("iteration %d picked b0, which has 20 requests in flight", i)
		}
	}
}

// --- lru ------------------------------------------------------------------

func TestLRUImplementsObserver(t *testing.T) {
	s := mustGet(t, "lru")
	if _, ok := s.(strategy.Observer); !ok {
		t.Fatal("the corrected LRU must implement Observer -- that was the whole lesson")
	}
}

func TestLRUPrefersTheLongestIdleBackend(t *testing.T) {
	s := mustGet(t, "lru")
	obs := s.(strategy.Observer)

	backends := weightedBackends(t, 1, 1, 1)
	req := httptest.NewRequest("GET", "/", nil)

	obs.OnFinish(backends[0], time.Millisecond, nil)
	time.Sleep(2 * time.Millisecond)
	obs.OnFinish(backends[1], time.Millisecond, nil)
	time.Sleep(2 * time.Millisecond)
	obs.OnFinish(backends[2], time.Millisecond, nil)

	b, err := s.Pick(req, backends)
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if b.Name != "b0" {
		t.Errorf("picked %s; b0 completed longest ago and should win", b.Name)
	}
}

// --- registry -------------------------------------------------------------

func TestAllWeek2StrategiesAreRegistered(t *testing.T) {
	want := []string{"ewma", "least_connections", "lru", "p2c", "random", "round_robin", "weighted_round_robin"}

	available := map[string]bool{}
	for _, n := range strategy.Available() {
		available[n] = true
	}

	for _, n := range want {
		if !available[n] {
			t.Errorf("strategy %q is not registered", n)
		}
	}
	t.Logf("registered strategies: %v", strategy.Available())
}
