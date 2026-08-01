package strategy

import (
	"math/rand"
	"net/http"

	"github.com/arsanantha1890-prog/load_balancing/internal/backend"
)

type LeastConnections struct{}

func init() {
	Register("least_connections", func() Strategy { return &LeastConnections{} })
}

func (LeastConnections) Name() string { return "least_connections" }

func (LeastConnections) Pick(_ *http.Request, healthy []*backend.Backend) (*backend.Backend, error) {
	if len(healthy) == 0 {
		return nil, ErrNoBackends
	}

	best := healthy[0]
	bestLoad := load(best)
	ties := 1

	for _, b := range healthy[1:] {
		l := load(b)

		switch {
		case l < bestLoad:
			best, bestLoad, ties = b, l, 1

		case l == bestLoad:

			ties++
			if rand.Intn(ties) == 0 {
				best = b
			}
		}
	}

	return best, nil
}

func load(b *backend.Backend) float64 {
	weight := b.Weight
	if weight <= 0 {
		weight = 1
	}
	return float64(b.Inflight()) / float64(weight)
}

var _ Strategy = (*LeastConnections)(nil)
