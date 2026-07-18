// Package strategy defines HOW a backend is chosen for a request.
//
// This file contains the single most important design decision in the project.
// Everything else — health checks, circuit breakers, Kubernetes discovery — is
// plumbing around this one interface. Get it right and week 2 is just five
// small files that each implement it.
package strategy

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/arsanantha1890-prog/gobalance/internal/backend"
)

// ErrNoBackends is returned when there is nothing healthy to route to.
// The proxy turns this into a 503.
var ErrNoBackends = errors.New("no healthy backends available")

// Strategy picks one backend out of the healthy set.
//
// GO NOTE: an interface is a list of method signatures. Any type with those
// methods satisfies it automatically — no "implements" keyword. That is why
// adding a new strategy in week 2 requires zero changes to the proxy.
//
// Pick must be safe to call from many goroutines at once.
type Strategy interface {
	// Name is the identifier used in the config file and in metrics labels.
	Name() string

	// Pick chooses a backend. healthy is never empty when Pick is called;
	// the proxy checks that first. Returning ErrNoBackends is still allowed
	// for strategies that may legitimately reject (e.g. all backends over
	// capacity).
	Pick(r *http.Request, healthy []*backend.Backend) (*backend.Backend, error)
}

// Observer is an OPTIONAL second interface. Strategies that need feedback about
// how requests actually went implement it; strategies that do not, ignore it.
//
// Round-robin needs nothing. Least-connections needs to know when a request
// starts and ends. EWMA-latency needs the measured latency. Splitting this out
// instead of stuffing it into Strategy keeps the simple strategies simple.
//
// The proxy discovers whether a strategy wants callbacks with a type assertion:
//
//	if obs, ok := strat.(Observer); ok { obs.OnFinish(b, latency, err) }
type Observer interface {
	OnStart(b *backend.Backend)
	OnFinish(b *backend.Backend, latency time.Duration, err error)
}

// --- registry ------------------------------------------------------------

// Factory builds a strategy instance.
type Factory func() Strategy

var (
	registryMu sync.RWMutex
	registry   = map[string]Factory{}
)

// Register makes a strategy available by name in the config file.
// Each strategy file calls this from its init() function.
func Register(name string, f Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[name] = f
}

// Get builds the strategy with the given name.
func Get(name string) (Strategy, error) {
	registryMu.RLock()
	f, ok := registry[name]
	registryMu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("unknown strategy %q (available: %v)", name, Available())
	}
	return f(), nil
}

// Available lists every registered strategy name, sorted.
func Available() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()

	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
