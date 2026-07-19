package strategy

import (
	"net/http"
	"sync"

	"github.com/arsanantha1890-prog/load_balancing/internal/backend"
)

// WeightedRoundRobin distributes requests in proportion to each backend's
// configured weight, using the SMOOTH algorithm that nginx uses.
//
// The naive approach is to expand the weights into a list -- weights 5,1,1
// becomes [a a a a a b c] -- and round-robin over that. It produces the correct
// ratio but a terrible ORDER: five requests slam backend a in a row before b or
// c see anything. At any real request rate that is a burst, and bursts are what
// cause queueing.
//
// The smooth algorithm produces a,a,b,a,c,a,a for the same 5:1:1 weights: the
// same ratio, spread evenly. Being able to explain why the ratio is not the
// only thing that matters is a genuinely good interview answer.
//
// How it works, per request:
//  1. every backend adds its weight to a running "current" score
//  2. the highest score wins
//  3. the winner subtracts the TOTAL weight from its score
//
// Step 3 is the trick. A heavy backend goes deeply negative after winning, so
// lighter backends get their turn before it climbs back.
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

	// A mutex is unavoidable here: the scores of ALL backends must be updated
	// as one atomic step, which is more than a single atomic counter can do.
	// That is the real cost of weighted round-robin versus plain round-robin,
	// and it is worth knowing where your locks are.
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
