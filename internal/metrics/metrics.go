package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics holds every Prometheus metric the balancer exposes.
type Metrics struct {
	RequestsTotal *prometheus.CounterVec

	RequestDuration *prometheus.HistogramVec

	Inflight *prometheus.GaugeVec

	BackendHealthy *prometheus.GaugeVec

	BreakerState *prometheus.GaugeVec

	RetriesTotal *prometheus.CounterVec

	RetryBudgetTokens prometheus.Gauge

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

	reg.MustRegister(prometheus.NewGoCollector())
	reg.MustRegister(prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))

	return m
}
