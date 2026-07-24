// Package metrics exposes Prometheus counters, histograms and gauges for the
// load balancer.
//
// DESIGN DECISIONS
//
// 1. One global registry, built once at startup. Per-request allocation of
//    labels would be a latency and GC disaster at 10k RPS.
//
// 2. Every metric uses LABELS, not separate metric names per backend. That is
//    what makes Grafana queries simple: rate(gobalance_requests_total{backend="gamma"}[1m])
//    works across any number of backends without editing the dashboard.
//
// 3. The histogram buckets are tuned for millisecond-scale HTTP latencies.
//    Default Prometheus buckets (.005, .01, .025, ...) are designed for seconds
//    and produce mostly empty buckets at our latencies. Custom buckets mean the
//    heatmap actually has data in it.
//
// 4. Metrics are registered on a CUSTOM registry, not the global default. The
//    global default includes Go runtime metrics (gc, goroutines, memory) which
//    are useful but noisy; a custom registry keeps the /metrics endpoint clean
//    and lets tests create isolated registries.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics holds every Prometheus metric the balancer exposes.
type Metrics struct {
	// RequestsTotal counts every proxied request, labelled by backend, strategy,
	// HTTP method, and status code. This is the single most important metric:
	// rate(gobalance_requests_total[1m]) is throughput, and splitting by backend
	// shows the distribution that the whole project is about.
	RequestsTotal *prometheus.CounterVec

	// RequestDuration is a histogram of end-to-end latency from the balancer's
	// perspective: time between dispatching to a backend and receiving the last
	// byte of the response (or the error). Buckets are in seconds.
	RequestDuration *prometheus.HistogramVec

	// Inflight is the number of requests currently being served by each backend.
	// This is the signal least-connections and P2C read on every pick.
	Inflight *prometheus.GaugeVec

	// BackendHealthy is 1 when the backend is in the healthy pool, 0 otherwise.
	// A transition from 1 to 0 in the dashboard is the visual "something died".
	BackendHealthy *prometheus.GaugeVec

	// BreakerState encodes closed=0, half_open=0.5, open=1. Not ideal as a
	// metric type, but it makes state transitions visible on a timeline without
	// needing Grafana's state-timeline panel.
	BreakerState *prometheus.GaugeVec

	// RetriesTotal counts retry attempts (not original requests).
	RetriesTotal *prometheus.CounterVec

	// RetryBudgetTokens is the current balance in the retry budget.
	RetryBudgetTokens prometheus.Gauge

	// FailuresTotal counts transport and 5xx failures per backend.
	FailuresTotal *prometheus.CounterVec

	Registry *prometheus.Registry
}

// New builds and registers every metric.
func New() *Metrics {
	reg := prometheus.NewRegistry()

	m := &Metrics{
		RequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gobalance_requests_total",
			Help: "Total proxied requests by backend, strategy, method and status code.",
		}, []string{"backend", "strategy", "method", "code"}),

		RequestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "gobalance_request_duration_seconds",
			Help: "Request latency in seconds.",
			// Buckets tuned for millisecond-scale services. 1ms to 10s covers
			// everything from a fast cache hit to a slow database query.
			Buckets: []float64{
				0.001, 0.002, 0.005, 0.01, 0.015, 0.02, 0.03, 0.05,
				0.075, 0.1, 0.15, 0.2, 0.3, 0.5, 1, 2, 5, 10,
			},
		}, []string{"backend", "strategy"}),

		Inflight: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "gobalance_backend_inflight",
			Help: "Requests currently in flight per backend.",
		}, []string{"backend"}),

		BackendHealthy: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "gobalance_backend_healthy",
			Help: "1 if the backend is healthy, 0 otherwise.",
		}, []string{"backend"}),

		BreakerState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "gobalance_breaker_state",
			Help: "Circuit breaker state: 0=closed, 0.5=half_open, 1=open.",
		}, []string{"backend"}),

		RetriesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gobalance_retries_total",
			Help: "Total retry attempts by backend and outcome.",
		}, []string{"backend", "outcome"}),

		RetryBudgetTokens: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "gobalance_retry_budget_tokens",
			Help: "Current retry budget token balance.",
		}),

		FailuresTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gobalance_failures_total",
			Help: "Total failures (transport errors and 5xx) per backend.",
		}, []string{"backend", "type"}),

		Registry: reg,
	}

	reg.MustRegister(
		m.RequestsTotal,
		m.RequestDuration,
		m.Inflight,
		m.BackendHealthy,
		m.BreakerState,
		m.RetriesTotal,
		m.RetryBudgetTokens,
		m.FailuresTotal,
	)

	// Include Go runtime metrics (goroutines, GC, memory). These are free and
	// occasionally useful for diagnosing the balancer itself rather than the
	// backends.
	reg.MustRegister(prometheus.NewGoCollector())
	reg.MustRegister(prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))

	return m
}
