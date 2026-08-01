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

func hashString(s string) uint32 {
	h := sha256.Sum256([]byte(s))
	return binary.BigEndian.Uint32(h[:4])
}

func defaultHashKey(r *http.Request) string {

	return r.URL.Path
}

var _ Strategy = (*ConsistentHash)(nil)
