package health_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arsanantha1890-prog/load_balancing/internal/backend"
	"github.com/arsanantha1890-prog/load_balancing/internal/health"
	"github.com/arsanantha1890-prog/load_balancing/internal/pool"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// toggleServer is an upstream whose health can be flipped at will.
func toggleServer(t *testing.T) (*httptest.Server, *atomic.Bool) {
	t.Helper()

	healthy := &atomic.Bool{}
	healthy.Store(true)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" && !healthy.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	return srv, healthy
}

func mustBackend(t *testing.T, name, url string) *backend.Backend {
	t.Helper()
	b, err := backend.New(name, url, 1, time.Second, time.Second)
	if err != nil {
		t.Fatalf("backend.New: %v", err)
	}
	return b
}

// waitFor polls until cond is true or the deadline passes. Better than a fixed
// sleep: fast when things work, and it reports what it was waiting for.
func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for %s", timeout, what)
}

func TestEjectsUnhealthyBackend(t *testing.T) {
	srv, healthy := toggleServer(t)
	b := mustBackend(t, "a", srv.URL)
	p := pool.New([]*backend.Backend{b})

	checker := health.New(p, health.Config{
		Enabled:            true,
		Path:               "/healthz",
		Interval:           20 * time.Millisecond,
		Timeout:            500 * time.Millisecond,
		HealthyThreshold:   2,
		UnhealthyThreshold: 3,
	}, discardLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go checker.Run(ctx)

	waitFor(t, "the backend to be confirmed healthy", 2*time.Second, b.Healthy)

	healthy.Store(false)
	waitFor(t, "the backend to be ejected", 2*time.Second, func() bool { return !b.Healthy() })

	if len(p.Healthy()) != 0 {
		t.Fatal("pool still reports a healthy backend after ejection")
	}
}

// The half nobody implements: noticing that a dead backend came back. Passive
// checking cannot do this, because no traffic is being sent there to observe.
func TestReadmitsRecoveredBackend(t *testing.T) {
	srv, healthy := toggleServer(t)
	b := mustBackend(t, "a", srv.URL)
	p := pool.New([]*backend.Backend{b})

	checker := health.New(p, health.Config{
		Enabled:            true,
		Path:               "/healthz",
		Interval:           20 * time.Millisecond,
		Timeout:            500 * time.Millisecond,
		HealthyThreshold:   2,
		UnhealthyThreshold: 2,
	}, discardLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go checker.Run(ctx)

	healthy.Store(false)
	waitFor(t, "ejection", 2*time.Second, func() bool { return !b.Healthy() })

	healthy.Store(true)
	waitFor(t, "readmission", 2*time.Second, b.Healthy)
}

// Hysteresis: a single bad poll must not eject a backend when the threshold is 3.
func TestSingleFailureDoesNotEject(t *testing.T) {
	var failNext atomic.Bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failNext.CompareAndSwap(true, false) {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	b := mustBackend(t, "a", srv.URL)
	p := pool.New([]*backend.Backend{b})

	checker := health.New(p, health.Config{
		Enabled:            true,
		Path:               "/healthz",
		Interval:           20 * time.Millisecond,
		Timeout:            500 * time.Millisecond,
		HealthyThreshold:   2,
		UnhealthyThreshold: 3,
	}, discardLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go checker.Run(ctx)

	time.Sleep(100 * time.Millisecond)
	failNext.Store(true)
	time.Sleep(200 * time.Millisecond)

	if !b.Healthy() {
		t.Fatal("one isolated failure ejected the backend; the threshold is 3")
	}
}

func TestUnreachableBackendIsEjected(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := dead.URL
	dead.Close() // nothing listening any more

	b := mustBackend(t, "dead", url)
	p := pool.New([]*backend.Backend{b})

	checker := health.New(p, health.Config{
		Enabled:            true,
		Path:               "/healthz",
		Interval:           20 * time.Millisecond,
		Timeout:            200 * time.Millisecond,
		HealthyThreshold:   2,
		UnhealthyThreshold: 2,
	}, discardLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go checker.Run(ctx)

	waitFor(t, "the unreachable backend to be ejected", 3*time.Second, func() bool { return !b.Healthy() })
}

func TestDisabledCheckerReturnsImmediately(t *testing.T) {
	p := pool.New(nil)
	checker := health.New(p, health.Config{Enabled: false}, discardLogger())

	done := make(chan struct{})
	go func() {
		checker.Run(context.Background())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("a disabled checker should return immediately, not block")
	}
}
