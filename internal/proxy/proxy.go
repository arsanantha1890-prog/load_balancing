// Package proxy is the request path: the code that runs on every single
// request. Everything here should be cheap and allocation-light.
//
// The flow is deliberately small and readable:
//
//	healthy backends -> strategy.Pick -> count in-flight -> ReverseProxy -> observe
package proxy

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/arsanantha1890-prog/load_balancing/internal/pool"
	"github.com/arsanantha1890-prog/load_balancing/internal/strategy"
)

// Balancer is an http.Handler that forwards requests to a pool of backends.
//
// GO NOTE: implementing ServeHTTP(http.ResponseWriter, *http.Request) is all it
// takes to be an http.Handler. That is why Balancer can be handed straight to
// http.Server with no adapter.
type Balancer struct {
	pool  *pool.Pool
	strat strategy.Strategy
	log   *slog.Logger

	// observer is non-nil only when the chosen strategy wants feedback.
	// Resolved once at construction so the hot path does no type assertions.
	observer strategy.Observer
}

// New builds a Balancer.
func New(p *pool.Pool, s strategy.Strategy, log *slog.Logger) *Balancer {
	b := &Balancer{pool: p, strat: s, log: log}

	// GO NOTE: this is a type assertion with the "comma ok" form. It asks
	// "does the concrete value inside this interface also satisfy Observer?"
	// Round-robin says no; least-connections (week 2) will say yes.
	if obs, ok := s.(strategy.Observer); ok {
		b.observer = obs
	}
	return b
}

// Strategy exposes the active strategy (used by the admin endpoint).
func (b *Balancer) Strategy() strategy.Strategy { return b.strat }

// Pool exposes the backend pool (used by the admin endpoint).
func (b *Balancer) Pool() *pool.Pool { return b.pool }

func (b *Balancer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	healthy := b.pool.Healthy()
	if len(healthy) == 0 {
		http.Error(w, "503 service unavailable: no healthy backends", http.StatusServiceUnavailable)
		b.log.Warn("rejected request: pool is empty", "path", r.URL.Path)
		return
	}

	target, err := b.strat.Pick(r, healthy)
	if err != nil {
		http.Error(w, "503 service unavailable: "+err.Error(), http.StatusServiceUnavailable)
		return
	}

	target.IncInflight()
	if b.observer != nil {
		b.observer.OnStart(target)
	}
	// GO NOTE: defer guarantees the counter comes back down even if the handler
	// panics. Forgetting this is the classic way to leak in-flight counts until
	// least-connections believes every backend is permanently overloaded.
	defer target.DecInflight()

	// Tell the client which backend served it. Invaluable when demoing, and the
	// week-6 benchmark scripts read this header to verify the distribution.
	w.Header().Set("X-Gobalance-Backend", target.Name)
	w.Header().Set("X-Gobalance-Strategy", b.strat.Name())

	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	start := time.Now()

	target.Proxy.ServeHTTP(rec, r)

	latency := time.Since(start)
	failed := rec.status >= 500
	if failed {
		target.RecordFailure()
	}

	if b.observer != nil {
		var obsErr error
		if failed {
			obsErr = errUpstreamFailure
		}
		b.observer.OnFinish(target, latency, obsErr)
	}

	b.log.Debug("proxied request",
		"backend", target.Name,
		"method", r.Method,
		"path", r.URL.Path,
		"status", rec.status,
		"latency_ms", latency.Milliseconds(),
	)
}

// errUpstreamFailure is passed to observers when the upstream returned 5xx.
var errUpstreamFailure = errors.New("upstream returned a 5xx response")

// statusRecorder wraps http.ResponseWriter so we can see the status code that
// was written. The standard library gives you no way to read it back.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wroteHeader {
		s.status = code
		s.wroteHeader = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(p []byte) (int, error) {
	if !s.wroteHeader {
		s.wroteHeader = true // an implicit 200
	}
	return s.ResponseWriter.Write(p)
}

// Unwrap lets http.ResponseController reach the real ResponseWriter, which is
// what keeps flushing, hijacking and streaming working through the wrapper.
//
// This is a real bug you would otherwise hit the first time you proxy a
// server-sent-events or streaming endpoint, and it is a good interview story.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// Compile-time check.
var _ http.Handler = (*Balancer)(nil)
