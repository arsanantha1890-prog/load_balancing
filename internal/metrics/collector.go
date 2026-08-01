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
