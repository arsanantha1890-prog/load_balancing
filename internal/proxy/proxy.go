// Package proxy is the request path: the code that runs on every request.
//
// WEEK 3 turned this from a straight line into a small retry loop:
//
//	for each attempt:
//	    choose a backend  (healthy, breaker permits, not already tried)
//	    acquire a breaker slot
//	    forward
//	    on transport failure and nothing written yet -> try another backend
//	    on success or on any response already sent    -> stop
//
// Everything about correctness here comes from one question: HAS ANYTHING BEEN
// WRITTEN TO THE CLIENT YET? Once a byte is on the wire, retrying is impossible.
package proxy

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/arsanantha1890-prog/load_balancing/internal/backend"
	"github.com/arsanantha1890-prog/load_balancing/internal/pool"
	"github.com/arsanantha1890-prog/load_balancing/internal/retry"
	"github.com/arsanantha1890-prog/load_balancing/internal/retryctx"
	"github.com/arsanantha1890-prog/load_balancing/internal/strategy"
)

// RetryConfig controls request retries.
type RetryConfig struct {
	Enabled    bool
	MaxRetries int
}

// PassiveConfig controls passive health checking: ejecting a backend based on
// the failures of real traffic, without waiting for the next active probe.
type PassiveConfig struct {
	Enabled             bool
	ConsecutiveFailures uint32
}

// Balancer is an http.Handler that forwards requests to a pool of backends.
type Balancer struct {
	pool  *pool.Pool
	strat strategy.Strategy
	log   *slog.Logger

	observer strategy.Observer

	retryCfg RetryConfig
	budget   *retry.Budget
	passive  PassiveConfig
}

// Option configures a Balancer.
//
// GO NOTE: functional options. Week 1 called proxy.New(pool, strategy, log) and
// that still compiles unchanged -- new capabilities are added without breaking
// existing callers or tests. This is the standard Go answer to "my constructor
// keeps growing".
type Option func(*Balancer)

// WithRetry enables retries, throttled by the shared budget.
func WithRetry(cfg RetryConfig, budget *retry.Budget) Option {
	return func(b *Balancer) {
		b.retryCfg = cfg
		b.budget = budget
	}
}

// WithPassiveHealth enables ejection based on observed traffic failures.
func WithPassiveHealth(cfg PassiveConfig) Option {
	return func(b *Balancer) { b.passive = cfg }
}

// New builds a Balancer.
func New(p *pool.Pool, s strategy.Strategy, log *slog.Logger, opts ...Option) *Balancer {
	b := &Balancer{pool: p, strat: s, log: log}

	// Resolved once, not per request.
	if obs, ok := s.(strategy.Observer); ok {
		b.observer = obs
	}

	for _, opt := range opts {
		opt(b)
	}
	return b
}

// Strategy exposes the active strategy (admin endpoint).
func (b *Balancer) Strategy() strategy.Strategy { return b.strat }

// Pool exposes the backend pool (admin endpoint).
func (b *Balancer) Pool() *pool.Pool { return b.pool }

// Budget exposes the retry budget, or nil (admin endpoint).
func (b *Balancer) Budget() *retry.Budget { return b.budget }

func (b *Balancer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	maxAttempts := 1
	if b.retryCfg.Enabled && b.retryCfg.MaxRetries > 0 && isRetryable(r) {
		maxAttempts = 1 + b.retryCfg.MaxRetries
	}

	// One recorder for the whole request. If an attempt writes anything, later
	// attempts are forbidden -- and wroteHeader is how we know.
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	tried := make(map[string]bool, maxAttempts)
	dispatched := false

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			// RETRY BUDGET. Without this, one dead backend doubles the load on
			// the survivors, and two dead backends triple it -- a retry storm
			// that turns a partial fault into a full outage.
			if b.budget == nil || !b.budget.Allow() {
				b.log.Warn("retry suppressed by budget", "path", r.URL.Path)
				break
			}
		}

		target := b.choose(r, tried)
		if target == nil {
			break
		}
		tried[target.Name] = true
		dispatched = true

		failed, transportErr := b.attempt(rec, r, target)
		if !failed {
			return
		}

		// A 5xx response has already been written to the client, so the request
		// is finished whether we like it or not. Only a transport failure --
		// where nothing was written -- leaves retrying open.
		if transportErr == nil || rec.wroteHeader {
			return
		}

		b.log.Warn("attempt failed, will consider a retry",
			"backend", target.Name, "attempt", attempt+1, "error", transportErr)
	}

	if !rec.wroteHeader {
		if !dispatched {
			http.Error(w, "503 service unavailable: no healthy backends", http.StatusServiceUnavailable)
			return
		}
		http.Error(w, "502 bad gateway: no backend could serve this request", http.StatusBadGateway)
	}
}

// attempt forwards the request to one backend and reports what happened.
//
// It is a separate function purely so that DecInflight and Breaker.Release run
// under defer. In a loop body a defer would pile up until the whole handler
// returned, leaking in-flight counts and breaker probe slots.
func (b *Balancer) attempt(rec *statusRecorder, r *http.Request, target *backend.Backend) (failed bool, transportErr error) {
	target.IncInflight()
	defer target.DecInflight()

	if b.observer != nil {
		b.observer.OnStart(target)
	}

	// A fresh retry slot per attempt, so a failure from attempt 1 cannot be
	// mistaken for a failure of attempt 2.
	ctx, state := retryctx.With(r.Context())
	req := r.WithContext(ctx)

	rec.Header().Set("X-Gobalance-Backend", target.Name)
	rec.Header().Set("X-Gobalance-Strategy", b.strat.Name())

	start := time.Now()
	target.Proxy.ServeHTTP(rec, req)
	latency := time.Since(start)

	failed = state.Err != nil || rec.status >= 500

	if target.Breaker != nil {
		target.Breaker.Release(!failed)
	}

	if failed {
		n := target.RecordFailure()
		if b.budget != nil {
			b.budget.OnFailure()
		}

		// PASSIVE HEALTH CHECK. The active checker in internal/health polls on
		// an interval; this reacts to real traffic in between polls. Active
		// detects failure before a user is harmed and is the only thing that
		// can detect recovery. Passive costs nothing and is instant. Neither is
		// sufficient alone.
		if b.passive.Enabled && n >= b.passive.ConsecutiveFailures && target.Healthy() {
			target.SetHealthy(false)
			b.log.Warn("backend ejected by passive health check",
				"backend", target.Name, "consecutive_failures", n)
		}
	} else {
		target.RecordSuccess()
		if b.budget != nil {
			b.budget.OnSuccess()
		}
	}

	if b.observer != nil {
		b.observer.OnFinish(target, latency, state.Err)
	}

	b.log.Debug("proxied request",
		"backend", target.Name, "method", r.Method, "path", r.URL.Path,
		"status", rec.status, "latency_ms", latency.Milliseconds(), "failed", failed)

	return failed, state.Err
}

// choose picks an eligible backend and reserves a breaker slot for it.
//
// Eligible means: healthy, not already tried for this request, and the breaker
// is willing. The bounded loop handles the race where a breaker's state changes
// between the Ready check and the Acquire.
func (b *Balancer) choose(r *http.Request, tried map[string]bool) *backend.Backend {
	const maxPickAttempts = 8

	for i := 0; i < maxPickAttempts; i++ {
		healthy := b.pool.Healthy()

		candidates := make([]*backend.Backend, 0, len(healthy))
		for _, be := range healthy {
			if tried[be.Name] {
				continue
			}
			// Ready is deliberately side-effect free. Using Acquire here would
			// burn a half-open probe slot on every backend merely by looking.
			if be.Breaker != nil && !be.Breaker.Ready() {
				continue
			}
			candidates = append(candidates, be)
		}

		if len(candidates) == 0 {
			return nil
		}

		target, err := b.strat.Pick(r, candidates)
		if err != nil || target == nil {
			return nil
		}

		if target.Breaker == nil || target.Breaker.Acquire() {
			return target
		}

		// Lost the probe slot to a concurrent request. Exclude and re-pick.
		tried[target.Name] = true
	}

	return nil
}

// isRetryable decides whether a request may legally be sent twice.
//
// Two conditions, both non-negotiable:
//
//  1. THE METHOD MUST BE IDEMPOTENT. Retrying a POST could charge a card twice.
//     GET, HEAD, OPTIONS and TRACE are safe by definition in RFC 9110.
//
//  2. THERE MUST BE NO REQUEST BODY. A body is a one-shot stream; once read it
//     is gone, and replaying it would mean buffering every request in memory --
//     an unbounded memory cost and a denial-of-service vector.
//
// PUT and DELETE are idempotent too, but they usually carry bodies, so they are
// excluded here rather than complicating the code for a rare case.
func isRetryable(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
	default:
		return false
	}
	return r.ContentLength <= 0
}

// statusRecorder wraps http.ResponseWriter so the status code can be read back,
// and so the retry loop can tell whether anything has been written yet.
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
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

var _ http.Handler = (*Balancer)(nil)
