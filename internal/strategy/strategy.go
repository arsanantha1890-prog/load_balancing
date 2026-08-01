package strategy

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/arsanantha1890-prog/load_balancing/internal/backend"
)

var ErrNoBackends = errors.New("no healthy backends available")

type Strategy interface {
	// Name is the identifier used in the config file and in metrics labels.
	Name() string

	Pick(r *http.Request, healthy []*backend.Backend) (*backend.Backend, error)
}

type Observer interface {
	OnStart(b *backend.Backend)
	OnFinish(b *backend.Backend, latency time.Duration, err error)
}

type Factory func() Strategy

var (
	registryMu sync.RWMutex
	registry   = map[string]Factory{}
)

func Register(name string, f Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[name] = f
}

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
