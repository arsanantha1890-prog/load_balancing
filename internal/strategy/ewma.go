package strategy

import (
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/arsanantha1890-prog/load_balancing/internal/backend"
)

const ewmaTau = 10 * time.Second


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

		w := math.Exp(-float64(now.Sub(entry.updated)) / float64(ewmaTau))
		entry.value = entry.value*w + observed*(1-w)
	}

	entry.updated = now
}

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
