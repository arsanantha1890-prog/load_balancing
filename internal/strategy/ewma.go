package strategy

import (
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/arsanantha1890-prog/load_balancing/internal/backend"
)

// ewmaTau controls how fast a backend is forgiven for being slow.
//
// Larger tau = longer memory = more stable but slower to notice a recovery.
// 10 seconds is what Finagle uses and it is a reasonable default. Week 6 is the
// place to tune it with data rather than intuition, and "the decay constant is
// a tunable I benchmarked" is a better sentence than "I picked 10s".
const ewmaTau = 10 * time.Second

// EWMA routes on measured response time rather than on request count.
//
// This is the most sophisticated strategy in the project and it combines three
// separate ideas. Understanding why each is there is the point of the exercise.
//
// IDEA 1 -- PEAK EWMA, not plain EWMA.
// A normal exponentially weighted moving average reacts symmetrically: it takes
// just as long to notice a backend got slow as to notice it got fast. That is
// the wrong trade-off. Damage from routing to a degraded backend is immediate,
// so this implementation jumps STRAIGHT UP on a slow response and only decays
// gradually on fast ones. React instantly to bad news, cautiously to good news.
// Finagle calls this Peak EWMA.
//
// IDEA 2 -- cost = latency * (inflight + 1).
// Pure historical latency is backward-looking: it cannot see the ten requests
// you dispatched two milliseconds ago that have not returned yet. Multiplying
// by in-flight count turns the score into an ESTIMATED QUEUE TIME -- roughly
// "how long until a request sent now finishes". The +1 accounts for the request
// being placed. This single multiplication is what makes EWMA robust under
// bursts instead of oscillating.
//
// IDEA 3 -- pick with P2C, not by global minimum.
// Same herd-avoidance argument as p2c.go. Scanning for the global best score
// makes every replica converge on the same backend at the same instant.
//
// EWMA is the one strategy that implements Observer, because latency is the one
// signal nothing else in the system already records.
type EWMA struct {
	mu      sync.Mutex
	entries map[string]*ewmaEntry
}

type ewmaEntry struct {
	value   float64 // smoothed latency, in nanoseconds
	updated time.Time
}

func init() {
	Register("ewma", func() Strategy {
		return &EWMA{entries: make(map[string]*ewmaEntry)}
	})
}

func (e *EWMA) Name() string { return "ewma" }

func (e *EWMA) Pick(_ *http.Request, healthy []*backend.Backend) (*backend.Backend, error) {
	n := len(healthy)
	switch n {
	case 0:
		return nil, ErrNoBackends
	case 1:
		return healthy[0], nil
	}

	i, j := twoDistinct(n)
	first, second := healthy[i], healthy[j]

	e.mu.Lock()
	defer e.mu.Unlock()

	if e.cost(first) <= e.cost(second) {
		return first, nil
	}
	return second, nil
}

// cost estimates how long a request dispatched now would take.
// Callers must hold e.mu.
func (e *EWMA) cost(b *backend.Backend) float64 {
	entry, ok := e.entries[b.Name]
	if !ok {
		// Never sampled. A cost of zero makes it the most attractive option,
		// so brand new backends get probed immediately instead of being starved
		// by incumbents that already have a good score. Because selection is
		// P2C rather than global-minimum, the resulting probe traffic is spread
		// out instead of arriving as a stampede.
		return 0
	}

	weight := b.Weight
	if weight <= 0 {
		weight = 1
	}

	return entry.value * float64(b.Inflight()+1) / float64(weight)
}

// OnStart satisfies Observer. EWMA needs nothing at dispatch time -- the
// in-flight counter is already maintained by the proxy.
func (e *EWMA) OnStart(_ *backend.Backend) {}

// OnFinish folds a completed request's latency into the backend's score.
func (e *EWMA) OnFinish(b *backend.Backend, latency time.Duration, _ error) {
	now := time.Now()
	observed := float64(latency)

	e.mu.Lock()
	defer e.mu.Unlock()

	if e.entries == nil {
		e.entries = make(map[string]*ewmaEntry)
	}

	entry, ok := e.entries[b.Name]
	if !ok {
		e.entries[b.Name] = &ewmaEntry{value: observed, updated: now}
		return
	}

	if observed > entry.value {
		// Bad news travels instantly.
		entry.value = observed
	} else {
		// Good news decays in. The weight given to history depends on ELAPSED
		// TIME rather than on request count, so a backend that goes quiet is
		// forgiven at the same rate as one under constant traffic. A fixed
		// alpha would make recovery speed depend on how busy you happen to be.
		w := math.Exp(-float64(now.Sub(entry.updated)) / float64(ewmaTau))
		entry.value = entry.value*w + observed*(1-w)
	}

	entry.updated = now
}

// Score exposes the current smoothed latency for a backend, for tests and for
// the admin endpoint. Returns false if the backend has never been sampled.
func (e *EWMA) Score(name string) (time.Duration, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	entry, ok := e.entries[name]
	if !ok {
		return 0, false
	}
	return time.Duration(entry.value), true
}

var (
	_ Strategy = (*EWMA)(nil)
	_ Observer = (*EWMA)(nil)
)
