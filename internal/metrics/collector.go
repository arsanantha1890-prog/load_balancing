// collector.go periodically syncs backend state into Prometheus gauges.
//
// WHY A PERIODIC COLLECTOR
//
// Request-path metrics (counters, histograms) are updated inline because every
// request passes through the proxy anyway. But health and breaker STATE are
// changed by background goroutines (the health checker) or by transitions
// inside the breaker that happen independently of the request path.
//
// Polling every second is cheap (one pass over the backend list), keeps the
// request path free of gauge updates, and gives Grafana a smooth timeline
// rather than a staircase that only moves when a request happens to trigger it.
package metrics

import (
	"context"
	"time"

	"github.com/arsanantha1890-prog/load_balancing/internal/breaker"
	"github.com/arsanantha1890-prog/load_balancing/internal/pool"
	"github.com/arsanantha1890-prog/load_balancing/internal/retry"
)

// RunCollector polls backend state into Prometheus gauges until ctx is done.
func (m *Metrics) RunCollector(ctx context.Context, p *pool.Pool, budget *retry.Budget, interval time.Duration) {
	if interval <= 0 {
		interval = time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.collect(p, budget)
		}
	}
}

func (m *Metrics) collect(p *pool.Pool, budget *retry.Budget) {
	for _, b := range p.All() {
		name := b.Name

		if b.Healthy() {
			m.BackendHealthy.WithLabelValues(name).Set(1)
		} else {
			m.BackendHealthy.WithLabelValues(name).Set(0)
		}

		m.Inflight.WithLabelValues(name).Set(float64(b.Inflight()))

		if b.Breaker != nil {
			var v float64
			switch b.Breaker.State() {
			case breaker.StateClosed:
				v = 0
			case breaker.StateHalfOpen:
				v = 0.5
			case breaker.StateOpen:
				v = 1
			}
			m.BreakerState.WithLabelValues(name).Set(v)
		}
	}

	if budget != nil {
		m.RetryBudgetTokens.Set(budget.Tokens())
	}
}
