package proxy_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arsanantha1890-prog/load_balancing/internal/backend"
	"github.com/arsanantha1890-prog/load_balancing/internal/breaker"
	"github.com/arsanantha1890-prog/load_balancing/internal/pool"
	"github.com/arsanantha1890-prog/load_balancing/internal/proxy"
	"github.com/arsanantha1890-prog/load_balancing/internal/retry"
	"github.com/arsanantha1890-prog/load_balancing/internal/strategy"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func mkBackend(t *testing.T, name, url string) *backend.Backend {
	t.Helper()
	b, err := backend.New(name, url, 1, 300*time.Millisecond, 300*time.Millisecond)
	if err != nil {
		t.Fatalf("backend.New: %v", err)
	}
	return b
}

func rrStrategy(t *testing.T) strategy.Strategy {
	t.Helper()
	s, err := strategy.Get("round_robin")
	if err != nil {
		t.Fatalf("strategy.Get: %v", err)
	}
	return s
}

// deadURL returns the address of a server that is no longer listening.
func deadURL(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	u := srv.URL
	srv.Close()
	return u
}

// --- retries --------------------------------------------------------------

// The headline week-3 behaviour: a dead backend must not become a client error
// when a healthy backend is available.
func TestRetryFailsOverToAHealthyBackend(t *testing.T) {
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "served")
	}))
	defer good.Close()

	dead := mkBackend(t, "dead", deadURL(t))
	alive := mkBackend(t, "alive", good.URL)

	lb := proxy.New(
		pool.New([]*backend.Backend{dead, alive}),
		rrStrategy(t),
		quietLogger(),
		proxy.WithRetry(proxy.RetryConfig{Enabled: true, MaxRetries: 2}, retry.NewBudget(100, 0.2)),
	)

	rec := httptest.NewRecorder()
	lb.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 -- the retry should have reached the healthy backend", rec.Code)
	}
	if rec.Body.String() != "served" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "served")
	}
	if dead.Failures() == 0 {
		t.Error("the dead backend's failure counter was not incremented")
	}
}

// A POST must never be retried: the request may not be idempotent, and replaying
// it could duplicate a side effect such as a payment.
func TestPostIsNeverRetried(t *testing.T) {
	dead := mkBackend(t, "dead", deadURL(t))
	alive := mkBackend(t, "alive", httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})).URL)

	lb := proxy.New(
		pool.New([]*backend.Backend{dead, alive}),
		rrStrategy(t),
		quietLogger(),
		proxy.WithRetry(proxy.RetryConfig{Enabled: true, MaxRetries: 2}, retry.NewBudget(100, 0.2)),
	)

	rec := httptest.NewRecorder()
	lb.ServeHTTP(rec, httptest.NewRequest("POST", "/", nil))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 -- a POST must not fail over", rec.Code)
	}
}

// A 5xx from the upstream has already been written to the client. Retrying is
// physically impossible at that point, and attempting it would corrupt the
// response.
func TestFiveHundredIsNotRetried(t *testing.T) {
	var hits atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	a := mkBackend(t, "a", srv.URL)
	b := mkBackend(t, "b", srv.URL)

	lb := proxy.New(
		pool.New([]*backend.Backend{a, b}),
		rrStrategy(t),
		quietLogger(),
		proxy.WithRetry(proxy.RetryConfig{Enabled: true, MaxRetries: 2}, retry.NewBudget(100, 0.2)),
	)

	rec := httptest.NewRecorder()
	lb.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want the upstream's 500 passed through", rec.Code)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("upstream was hit %d times, want exactly 1 -- a written response cannot be retried", got)
	}
}

// With retries disabled the balancer must behave exactly as it did in week 1.
func TestRetryDisabledGivesUpImmediately(t *testing.T) {
	dead := mkBackend(t, "dead", deadURL(t))
	alive := mkBackend(t, "alive", httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})).URL)

	lb := proxy.New(
		pool.New([]*backend.Backend{dead, alive}),
		rrStrategy(t),
		quietLogger(),
	)

	rec := httptest.NewRecorder()
	lb.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 with retries off", rec.Code)
	}
}

func TestAllBackendsDeadReturns502(t *testing.T) {
	a := mkBackend(t, "a", deadURL(t))
	b := mkBackend(t, "b", deadURL(t))

	lb := proxy.New(
		pool.New([]*backend.Backend{a, b}),
		rrStrategy(t),
		quietLogger(),
		proxy.WithRetry(proxy.RetryConfig{Enabled: true, MaxRetries: 3}, retry.NewBudget(100, 0.2)),
	)

	rec := httptest.NewRecorder()
	lb.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 when nothing can serve the request", rec.Code)
	}
}

// --- passive health -------------------------------------------------------

func TestPassiveHealthEjectsAfterConsecutiveFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	b := mkBackend(t, "bad", srv.URL)

	lb := proxy.New(
		pool.New([]*backend.Backend{b}),
		rrStrategy(t),
		quietLogger(),
		proxy.WithPassiveHealth(proxy.PassiveConfig{Enabled: true, ConsecutiveFailures: 3}),
	)

	for i := 0; i < 3; i++ {
		lb.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	}

	if b.Healthy() {
		t.Fatalf("backend still healthy after %d consecutive 5xx responses", b.ConsecutiveFailures())
	}
}

// A success in the middle of a run of failures resets the counter -- the
// backend is flaky, not down.
func TestPassiveHealthResetsOnSuccess(t *testing.T) {
	var failNext atomic.Bool
	failNext.Store(true)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failNext.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	b := mkBackend(t, "flaky", srv.URL)

	lb := proxy.New(
		pool.New([]*backend.Backend{b}),
		rrStrategy(t),
		quietLogger(),
		proxy.WithPassiveHealth(proxy.PassiveConfig{Enabled: true, ConsecutiveFailures: 3}),
	)

	lb.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	lb.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))

	failNext.Store(false)
	lb.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))

	if got := b.ConsecutiveFailures(); got != 0 {
		t.Fatalf("consecutive failures = %d after a success, want 0", got)
	}
	if !b.Healthy() {
		t.Error("a flaky backend was ejected; only an unbroken run should eject")
	}
}

// --- circuit breaker ------------------------------------------------------

// Once the breaker is open the backend must stop receiving traffic entirely,
// so a dead upstream stops costing a connect timeout per request.
func TestOpenBreakerStopsTrafficToTheBackend(t *testing.T) {
	var hits atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	bad := mkBackend(t, "bad", srv.URL)
	bad.Breaker = breaker.New(breaker.Config{
		FailureThreshold: 2,
		SuccessThreshold: 2,
		OpenTimeout:      time.Hour, // never recovers during the test
		MaxProbes:        1,
	})

	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer good.Close()

	alive := mkBackend(t, "alive", good.URL)
	alive.Breaker = breaker.New(breaker.Config{FailureThreshold: 100})

	lb := proxy.New(
		pool.New([]*backend.Backend{bad, alive}),
		rrStrategy(t),
		quietLogger(),
	)

	for i := 0; i < 20; i++ {
		lb.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	}

	if bad.Breaker.State() != breaker.StateOpen {
		t.Fatalf("breaker state = %v after repeated failures, want open", bad.Breaker.State())
	}

	before := hits.Load()
	for i := 0; i < 10; i++ {
		lb.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	}

	if after := hits.Load(); after != before {
		t.Errorf("the failing upstream received %d more requests after the breaker opened", after-before)
	}
}

// --- retry budget ---------------------------------------------------------

// An exhausted budget must stop retries even when a healthy backend exists.
// That is the point: protecting the survivors is more important than saving
// any individual request.
func TestExhaustedBudgetSuppressesRetries(t *testing.T) {
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer good.Close()

	dead := mkBackend(t, "dead", deadURL(t))
	alive := mkBackend(t, "alive", good.URL)

	budget := retry.NewBudget(10, 0.2)
	for i := 0; i < 10; i++ {
		budget.OnFailure()
	}
	if budget.Allow() {
		t.Fatal("test setup failed: the budget should be exhausted")
	}

	lb := proxy.New(
		pool.New([]*backend.Backend{dead, alive}),
		rrStrategy(t),
		quietLogger(),
		proxy.WithRetry(proxy.RetryConfig{Enabled: true, MaxRetries: 3}, budget),
	)

	rec := httptest.NewRecorder()
	lb.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 -- retries must be suppressed by an empty budget", rec.Code)
	}
}
