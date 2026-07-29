package strategy

import (
	"crypto/sha256"
	"encoding/binary"
	"math"
	"net/http"
	"sort"
	"sync"

	"github.com/arsanantha1890-prog/load_balancing/internal/backend"
)

// ConsistentHash routes requests to backends using consistent hashing with
// bounded loads.
//
// WHY CONSISTENT HASHING EXISTS
//
// Sometimes the same request should go to the same backend every time. The
// classic example is a cache: if /user/123 always lands on backend-B, then
// backend-B's local cache is warm for that user, and the response comes from
// memory instead of the database. Spread /user/123 across three backends and
// each one cache-misses independently — three cold caches instead of one warm
// one.
//
// Session affinity (sticky sessions), sharded databases, and deduplicated
// processing all have the same requirement: requests with the same key should
// go to the same place.
//
// Plain hashing (hash(key) % n) does this, but adding or removing a backend
// remaps nearly every key. Consistent hashing remaps only ~1/n of keys when
// the backend set changes — which is why it was invented (Karger et al., 1997)
// and why every distributed cache and CDN uses it.
//
// WHY *BOUNDED LOADS* (Google, 2016)
//
// Pure consistent hashing has the same problem as round-robin: it ignores
// load. If one key is extremely hot, its backend gets hammered while others
// idle. If a backend is slow, requests keep piling on because the hash says so.
//
// Bounded loads (Mirrokni, Thorup, Zadimoghaddam, 2016) adds one rule:
//
//     A backend may only accept a request if its load is below
//     ceil(average_load * (1 + epsilon))
//
// If the hashed backend is over the bound, the request walks the ring to the
// next backend, then the next, until it finds one with headroom. This
// guarantees O(1/epsilon^2) maximum load imbalance while keeping most requests
// on their hashed target.
//
// epsilon = 0.25 means a backend may be at most 25% above average load. Lower
// epsilon = more even load but more key remapping. Higher epsilon = stickier
// but more imbalance.
//
// The beauty is that it DEGRADES GRACEFULLY into least-loaded under high skew
// and into pure consistent hashing under uniform load. It is never worse than
// either one alone.
//
// HOW THE RING WORKS
//
// Each backend is placed at multiple points (virtual nodes) on a hash ring.
// A request's key is hashed to a point on the ring, and the ring is walked
// clockwise until a backend with available capacity is found.
//
// Virtual nodes (replicas) spread each backend across the ring so that the
// load is approximately even even with few physical backends. Without them,
// 3 backends would divide the ring into 3 arcs of random size.
const (
	defaultReplicas = 150
	defaultEpsilon  = 0.25
)

// ConsistentHash implements Strategy.
type ConsistentHash struct {
	mu       sync.Mutex
	ring     []ringEntry
	replicas int
	epsilon  float64

	// hashKey extracts the routing key from the request. Defaults to URL path.
	// Could be set to use a header, cookie, or query parameter.
	hashKey func(r *http.Request) string
}

type ringEntry struct {
	hash    uint32
	backend *backend.Backend
}

func init() {
	Register("consistent_hash", func() Strategy {
		return &ConsistentHash{
			replicas: defaultReplicas,
			epsilon:  defaultEpsilon,
			hashKey:  defaultHashKey,
		}
	})
}

func (ch *ConsistentHash) Name() string { return "consistent_hash" }

func (ch *ConsistentHash) Pick(r *http.Request, healthy []*backend.Backend) (*backend.Backend, error) {
	if len(healthy) == 0 {
		return nil, ErrNoBackends
	}
	if len(healthy) == 1 {
		return healthy[0], nil
	}

	ch.mu.Lock()
	defer ch.mu.Unlock()

	// Rebuild the ring if the backend set changed. In a production system this
	// would be cached and invalidated on pool changes; rebuilding on every
	// request is correct but not optimal. Good enough for the project scope.
	ch.buildRing(healthy)

	key := ch.hashKey(r)
	h := hashString(key)

	// Find the first ring entry at or after the hash point.
	idx := sort.Search(len(ch.ring), func(i int) bool {
		return ch.ring[i].hash >= h
	})
	if idx == len(ch.ring) {
		idx = 0 // wrap around
	}

	// Bounded load check: walk the ring until we find a backend under the bound.
	bound := ch.loadBound(healthy)
	n := len(ch.ring)

	for i := 0; i < n; i++ {
		entry := ch.ring[(idx+i)%n]
		if float64(entry.backend.Inflight()) < bound {
			return entry.backend, nil
		}
	}

	// Every backend is at or above the bound. Fall back to the hashed target
	// rather than failing — bounded loads is a soft preference, not a hard gate.
	return ch.ring[idx].backend, nil
}

// loadBound calculates the maximum allowed in-flight count for any backend.
//
//	bound = ceil(average_load * (1 + epsilon))
//
// With a minimum of 1 so that an idle system (average=0) still allows requests.
func (ch *ConsistentHash) loadBound(healthy []*backend.Backend) float64 {
	var total int64
	for _, b := range healthy {
		total += b.Inflight()
	}

	avg := float64(total) / float64(len(healthy))
	bound := math.Ceil(avg * (1 + ch.epsilon))
	if bound < 1 {
		bound = 1
	}
	return bound
}

// buildRing creates the hash ring from the healthy backend set.
func (ch *ConsistentHash) buildRing(healthy []*backend.Backend) {
	needed := len(healthy) * ch.replicas
	if cap(ch.ring) < needed {
		ch.ring = make([]ringEntry, 0, needed)
	}
	ch.ring = ch.ring[:0]

	for _, b := range healthy {
		for i := 0; i < ch.replicas; i++ {
			// Hash the backend name + replica index to place virtual nodes.
			key := b.Name + "#" + string(rune(i))
			ch.ring = append(ch.ring, ringEntry{
				hash:    hashString(key),
				backend: b,
			})
		}
	}

	sort.Slice(ch.ring, func(i, j int) bool {
		return ch.ring[i].hash < ch.ring[j].hash
	})
}

// hashString produces a 32-bit hash from a string using SHA-256 truncated.
// SHA-256 is used for uniform distribution rather than speed; the cost is
// dwarfed by the network round-trip.
func hashString(s string) uint32 {
	h := sha256.Sum256([]byte(s))
	return binary.BigEndian.Uint32(h[:4])
}

func defaultHashKey(r *http.Request) string {
	// URL path is the default routing key. In production you might hash
	// a user ID header, a session cookie, or a query parameter.
	return r.URL.Path
}

var _ Strategy = (*ConsistentHash)(nil)
