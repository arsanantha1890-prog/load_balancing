package proxy

import (
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/arsanantha1890-prog/load_balancing/internal/backend"
	"github.com/arsanantha1890-prog/load_balancing/internal/metrics"
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

// PassiveConfig controls passive health checking.
type PassiveConfig struct {
	Enabled             bool
	ConsecutiveFailures uint32
}

// Balancer is an http.Handler that forwards requests to a pool of backends.
type Balancer struct {
	pool *pool.Pool
	log  *slog.Logger

	mu       sync.RWMutex
	strat    strategy.Strategy
	observer strategy.Observer

	retryCfg RetryConfig
	budget   *retry.Budget
	passive  PassiveConfig
	metrics  *metrics.Metrics
}

// Option configures a Balancer.
type Option func(*Balancer)

// WithRetry enables retries.
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

// WithMetrics attaches Prometheus instrumentation.
func WithMetrics(m *metrics.Metrics) Option {
	return func(b *Balancer) { b.metrics = m }
}

// New builds a Balancer.
func New(p *pool.Pool, s strategy.Strategy, log *slog.Logger, opts ...Option) *Balancer {
	b := &Balancer{pool: p, strat: s, log: log}

	if obs, ok := s.(strategy.Observer); ok {
		b.observer = obs
	}

	for _, opt := range opts {
		opt(b)
	}
	return b
}

func (b *Balancer) SwapStrategy(s strategy.Strategy) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.strat = s
	if obs, ok := s.(strategy.Observer); ok {
		b.observer = obs
	} else {
		b.observer = nil
	}
}

// Strategy returns the current strategy.
func (b *Balancer) Strategy() strategy.Strategy {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.strat
}

// Pool exposes the backend pool.
func (b *Balancer) Pool() *pool.Pool { return b.pool }

// Budget exposes the retry budget, or nil.
func (b *Balancer) Budget() *retry.Budget { return b.budget }

func (b *Balancer) getObserver() strategy.Observer {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.observer
}

type observerHolder struct {
	obs strategy.Observer
}

func (b *Balancer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	maxAttempts := 1
	if b.retryCfg.Enabled && b.retryCfg.MaxRetries > 0 && isRetryable(r) {
		maxAttempts = 1 + b.retryCfg.MaxRetries
	}

	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	tried := make(map[string]bool, maxAttempts)
	dispatched := false

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
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

		failed, transportErr := b.attempt(rec, r, target, attempt)
		if !failed {
			return
		}

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

func (b *Balancer) attempt(rec *statusRecorder, r *http.Request, target *backend.Backend, attempt int) (failed bool, transportErr error) {
	strat := b.Strategy()
	obs := b.getObserver()

	target.IncInflight()
	defer target.DecInflight()

	if obs != nil {
		obs.OnStart(target)
	}

	ctx, state := retryctx.With(r.Context())
	req := r.WithContext(ctx)

	rec.Header().Set("X-Gobalance-Backend", target.Name)
	rec.Header().Set("X-Gobalance-Strategy", strat.Name())

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

		if b.passive.Enabled && n >= b.passive.ConsecutiveFailures && target.Healthy() {
			target.SetHealthy(false)
			b.log.Warn("backend ejected by passive health check",
				"backend", target.Name, "consecutive_failures", n)
		}

		// Metrics: distinguish transport failures from upstream 5xx.
		if b.metrics != nil {
			if state.Err != nil {
				b.metrics.FailuresTotal.WithLabelValues(target.Name, "transport").Inc()
			} else {
				b.metrics.FailuresTotal.WithLabelValues(target.Name, "5xx").Inc()
			}
			if attempt > 0 {
				b.metrics.RetriesTotal.WithLabelValues(target.Name, "failed").Inc()
			}
		}
	} else {
		target.RecordSuccess()
		if b.budget != nil {
			b.budget.OnSuccess()
		}
		if b.metrics != nil && attempt > 0 {
			b.metrics.RetriesTotal.WithLabelValues(target.Name, "succeeded").Inc()
		}
	}

	if obs != nil {
		obs.OnFinish(target, latency, state.Err)
	}

	// Prometheus request metrics.
	if b.metrics != nil {
		code := fmt.Sprintf("%d", rec.status)
		b.metrics.RequestsTotal.WithLabelValues(target.Name, strat.Name(), r.Method, code).Inc()
		b.metrics.RequestDuration.WithLabelValues(target.Name, strat.Name()).Observe(latency.Seconds())
	}

	b.log.Debug("proxied request",
		"backend", target.Name, "method", r.Method, "path", r.URL.Path,
		"status", rec.status, "latency_ms", latency.Milliseconds(), "failed", failed)

	return failed, state.Err
}

func (b *Balancer) choose(r *http.Request, tried map[string]bool) *backend.Backend {
	const maxPickAttempts = 8

	for i := 0; i < maxPickAttempts; i++ {
		healthy := b.pool.Healthy()

		candidates := make([]*backend.Backend, 0, len(healthy))
		for _, be := range healthy {
			if tried[be.Name] {
				continue
			}
			if be.Breaker != nil && !be.Breaker.Ready() {
				continue
			}
			candidates = append(candidates, be)
		}

		if len(candidates) == 0 {
			return nil
		}

		strat := b.Strategy()
		target, err := strat.Pick(r, candidates)
		if err != nil || target == nil {
			return nil
		}

		if target.Breaker == nil || target.Breaker.Acquire() {
			return target
		}

		tried[target.Name] = true
	}

	return nil
}

func isRetryable(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
	default:
		return false
	}
	return r.ContentLength <= 0
}

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
		s.wroteHeader = true
	}
	return s.ResponseWriter.Write(p)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

var _ http.Handler = (*Balancer)(nil)
