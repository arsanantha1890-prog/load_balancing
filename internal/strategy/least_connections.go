package strategy

import (
	"math/rand"
	"net/http"

	"github.com/arsanantha1890-prog/load_balancing/internal/backend"
)

// LeastConnections sends each request to whichever backend currently has the
// fewest requests in flight.
//
// This is the strategy your week-1 baseline was crying out for, and the theory
// behind it is worth memorising:
//
//	Little's Law:  L = lambda * W
//	  L      = requests in flight
//	  lambda = arrival rate
//	  W      = service time
//
// Least-connections equalises L across backends. If L is equal everywhere but W
// differs, then lambda must be proportional to 1/W. In other words the traffic
// share converges on each backend's actual capacity AUTOMATICALLY -- and this
// strategy never measures latency at all. It only counts.
//
// For your setup (alpha 13ms, beta 13ms, gamma 92ms) the equilibrium share for
// gamma is (1/92) / (1/13 + 1/13 + 1/92) = 6.6%, down from round-robin's 33.3%.
//
// NOTE ON DESIGN: this strategy does NOT implement Observer. It does not need
// to. The in-flight counter lives on the Backend itself, and proxy.ServeHTTP
// already increments it before forwarding and decrements it via defer. So the
// state least-connections needs is a side effect of the request path rather
// than something the strategy has to track. EWMA is different -- it needs
// measured latency, which nothing else records, so that one does implement
// Observer.
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
			// TIE-BREAKING MATTERS MORE THAN IT LOOKS. At low load every
			// backend sits at zero in flight, so without this every request
			// would go to healthy[0] and the balancer would not balance at all.
			//
			// This is reservoir sampling: after seeing k tied candidates, each
			// has probability exactly 1/k of being held. Uniform, one pass, no
			// allocation.
			ties++
			if rand.Intn(ties) == 0 {
				best = b
			}
		}
	}

	return best, nil
}

// load is in-flight requests normalised by weight, so a backend configured as
// twice as powerful is allowed twice as many concurrent requests before it
// looks equally busy. With all weights at 1 this is just the in-flight count.
func load(b *backend.Backend) float64 {
	weight := b.Weight
	if weight <= 0 {
		weight = 1
	}
	return float64(b.Inflight()) / float64(weight)
}

var _ Strategy = (*LeastConnections)(nil)
