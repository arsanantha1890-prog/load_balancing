package strategy

import (
	"net/http"
	"sync"
	"time"

	"github.com/arsanantha1890-prog/load_balancing/internal/backend"
)

// LeastRecentlyUsed picks whichever backend has gone longest without COMPLETING
// a request.
//
// This is the corrected version of the week-1 exercise, and the story behind it
// is worth keeping.
//
// The first version recorded the timestamp at PICK time. Trace it: "least
// recently dispatched to" is, by definition, "the one whose turn it is" -- so
// the algorithm collapsed into round-robin exactly, not approximately. It was
// not a coding mistake; it was the algorithm being logically equivalent to a
// simpler one.
//
// Moving the timestamp to COMPLETION time changes everything. A backend that
// takes 92ms keeps a stale timestamp for the whole time it is working, so it
// stops looking attractive precisely while it is busy. That requires the
// Observer interface -- which is exactly what Observer exists for.
//
// Two fixes from the week-1 version:
//   - keyed on b.Name instead of the *Backend pointer. Week 4's config reload
//     and week 5's endpoint watcher rebuild Backend values, so pointer keys
//     would leak memory and silently lose all state on every reconcile.
//   - updates on completion rather than dispatch.
//
// Honest assessment: this is still weaker than least_connections, because a
// single timestamp cannot distinguish "idle" from "handling twenty requests".
// It is here as a worked example of the Observer pattern, not as a contender.
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
