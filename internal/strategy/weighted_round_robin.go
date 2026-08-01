package strategy

import (
	"net/http"
	"sync"

	"github.com/arsanantha1890-prog/load_balancing/internal/backend"
)

type WeightedRoundRobin struct {
	mu      sync.Mutex
	current map[string]int
}

func init() {
	Register("weighted_round_robin", func() Strategy {
		return &WeightedRoundRobin{current: make(map[string]int)}
	})
}

func (w *WeightedRoundRobin) Name() string { return "weighted_round_robin" }

func (w *WeightedRoundRobin) Pick(_ *http.Request, healthy []*backend.Backend) (*backend.Backend, error) {
	if len(healthy) == 0 {
		return nil, ErrNoBackends
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.current == nil {
		w.current = make(map[string]int)
	}

	total := 0
	var best *backend.Backend
	bestScore := 0

	for _, b := range healthy {
		weight := b.Weight
		if weight <= 0 {
			weight = 1
		}

		score := w.current[b.Name] + weight
		w.current[b.Name] = score
		total += weight

		if best == nil || score > bestScore {
			best, bestScore = b, score
		}
	}

	w.current[best.Name] = bestScore - total
	return best, nil
}

var _ Strategy = (*WeightedRoundRobin)(nil)
