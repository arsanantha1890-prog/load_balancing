package proxy_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/arsanantha1890-prog/load_balancing/internal/backend"
	"github.com/arsanantha1890-prog/load_balancing/internal/pool"
	"github.com/arsanantha1890-prog/load_balancing/internal/proxy"
	"github.com/arsanantha1890-prog/load_balancing/internal/strategy"
)

// discardLogger keeps test output clean.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newFakeUpstream spins up a real HTTP server that identifies itself.
//
// GO NOTE: httptest.NewServer starts a genuine server on a random free port.
// These are real network calls, not mocks — which is exactly what you want when
// testing a proxy.
func newFakeUpstream(t *testing.T, name string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream", name)
		_, _ = io.WriteString(w, "hello from "+name)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func buildBalancer(t *testing.T, strategyName string, upstreams ...*httptest.Server) *proxy.Balancer {
	t.Helper()

	backends := make([]*backend.Backend, 0, len(upstreams))
	for i, u := range upstreams {
		b, err := backend.New(string(rune('a'+i)), u.URL, 1, time.Second, time.Second)
		if err != nil {
			t.Fatalf("backend.New: %v", err)
		}
		backends = append(backends, b)
	}

	s, err := strategy.Get(strategyName)
	if err != nil {
		t.Fatalf("strategy.Get: %v", err)
	}
	return proxy.New(pool.New(backends), s, discardLogger())
}

// The end-to-end test: two real upstreams, round-robin, alternating responses.
func TestProxyForwardsAndAlternates(t *testing.T) {
	up1 := newFakeUpstream(t, "one")
	up2 := newFakeUpstream(t, "two")
	lb := buildBalancer(t, "round_robin", up1, up2)

	got := make([]string, 0, 4)
	for i := 0; i < 4; i++ {
		rec := httptest.NewRecorder()
		lb.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: status %d, want 200", i, rec.Code)
		}
		got = append(got, rec.Header().Get("X-Upstream"))
	}

	want := []string{"one", "two", "one", "two"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("request %d served by %q, want %q", i, got[i], want[i])
		}
	}
}

func TestProxySetsIdentifyingHeaders(t *testing.T) {
	lb := buildBalancer(t, "round_robin", newFakeUpstream(t, "one"))

	rec := httptest.NewRecorder()
	lb.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if rec.Header().Get("X-Gobalance-Backend") != "a" {
		t.Errorf("X-Gobalance-Backend = %q, want \"a\"", rec.Header().Get("X-Gobalance-Backend"))
	}
	if rec.Header().Get("X-Gobalance-Strategy") != "round_robin" {
		t.Errorf("X-Gobalance-Strategy = %q, want \"round_robin\"", rec.Header().Get("X-Gobalance-Strategy"))
	}
}

// An empty pool must fail fast with 503, never hang and never panic.
func TestEmptyPoolReturns503(t *testing.T) {
	lb := proxy.New(pool.New(nil), mustStrategy(t), discardLogger())

	rec := httptest.NewRecorder()
	lb.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", rec.Code)
	}
}

// A dead upstream must become a 502, not a panic or a hang.
func TestDeadUpstreamReturns502(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := dead.URL
	dead.Close() // nothing is listening on that port any more

	b, err := backend.New("dead", url, 1, 200*time.Millisecond, 200*time.Millisecond)
	if err != nil {
		t.Fatalf("backend.New: %v", err)
	}
	lb := proxy.New(pool.New([]*backend.Backend{b}), mustStrategy(t), discardLogger())

	rec := httptest.NewRecorder()
	lb.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", rec.Code)
	}
	if b.Failures() != 1 {
		t.Errorf("failure counter = %d, want 1", b.Failures())
	}
}

// In-flight must return to zero after every request. If this test ever fails,
// least-connections in week 2 would slowly poison itself.
func TestInflightReturnsToZero(t *testing.T) {
	up := newFakeUpstream(t, "one")
	b, err := backend.New("a", up.URL, 1, time.Second, time.Second)
	if err != nil {
		t.Fatalf("backend.New: %v", err)
	}
	lb := proxy.New(pool.New([]*backend.Backend{b}), mustStrategy(t), discardLogger())

	for i := 0; i < 10; i++ {
		lb.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	}

	if got := b.Inflight(); got != 0 {
		t.Fatalf("in-flight = %d after 10 completed requests, want 0", got)
	}
	if got := b.Total(); got != 10 {
		t.Errorf("total = %d, want 10", got)
	}
}

func mustStrategy(t *testing.T) strategy.Strategy {
	t.Helper()
	s, err := strategy.Get("round_robin")
	if err != nil {
		t.Fatalf("strategy.Get: %v", err)
	}
	return s
}
