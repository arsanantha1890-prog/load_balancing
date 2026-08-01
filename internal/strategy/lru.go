package strategy

import (
	"net/http"
	"sync"
	"time"

	"github.com/arsanantha1890-prog/load_balancing/internal/backend"
)

type LeastRecentlyUsed struct {
	mu       sync.Mutex
	lastUsed map[string]time.Time
}

func init() {
	Register("lru", func() Strategy {
		return &LeastRecentlyUsed{lastUsed: make(map[string]time.Time)}
	})
}

func (l *LeastRecentlyUsed) Name() string { return "lru" }

func (l *LeastRecentlyUsed) Pick(_ *http.Request, healthy []*backend.Backend) (*backend.Backend, error) {
	if len(healthy) == 0 {
		return nil, ErrNoBackends
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	var best *backend.Backend
	var bestTime time.Time

	for _, b := range healthy {
		// A backend with no entry has never completed anything, so its zero
		// time is older than any real timestamp and it wins automatically.
		t := l.lastUsed[b.Name]
		if best == nil || t.Before(bestTime) {
			best, bestTime = b, t
		}
	}

	return best, nil
}

func (l *LeastRecentlyUsed) OnStart(_ *backend.Backend) {}

func (l *LeastRecentlyUsed) OnFinish(b *backend.Backend, _ time.Duration, _ error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.lastUsed == nil {
		l.lastUsed = make(map[string]time.Time)
	}
	l.lastUsed[b.Name] = time.Now()
}

var (
	_ Strategy = (*LeastRecentlyUsed)(nil)
	_ Observer = (*LeastRecentlyUsed)(nil)
)
