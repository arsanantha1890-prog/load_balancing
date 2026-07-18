package strategy

import (
	"net/http"
	"sync"
	"time"

	"github.com/arsanantha1890-prog/load_balancing/internal/backend"
)

// LeastRecentlyUsed picks the backend that was used longest ago.
type LeastRecentlyUsed struct {
	mu       sync.Mutex
	lastUsed map[*backend.Backend]time.Time
}

func init() {
	Register("least_recently_used", func() Strategy {
		return &LeastRecentlyUsed{lastUsed: make(map[*backend.Backend]time.Time)}
	})
}

func (l *LeastRecentlyUsed) Name() string { return "least_recently_used" }

func (l *LeastRecentlyUsed) Pick(_ *http.Request, healthy []*backend.Backend) (*backend.Backend, error) {
	if len(healthy) == 0 {
		return nil, ErrNoBackends
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	oldest := healthy[0]
	oldestTime := l.lastUsed[oldest]

	for _, b := range healthy[1:] {
		t, ok := l.lastUsed[b]
		if !ok {
			oldest = b
			break
		}
		if t.Before(oldestTime) {
			oldest = b
			oldestTime = t
		}
	}

	l.lastUsed[oldest] = time.Now()
	return oldest, nil
}

var _ Strategy = (*LeastRecentlyUsed)(nil)