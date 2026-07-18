// Package pool holds the set of backends and answers the question the balancer
// asks on every single request: "which backends can I use right now?"
//
// The pool is read constantly (once per request) and written rarely (config
// reload in week 4, Kubernetes endpoint changes in week 5). That read/write
// ratio is exactly what sync.RWMutex is for: many readers may hold the lock at
// once, a writer excludes everyone.
package pool

import (
	"sync"

	"github.com/arsanantha1890-prog/load_balancing/internal/backend"
)

// Pool is a concurrency-safe set of backends.
type Pool struct {
	mu       sync.RWMutex
	backends []*backend.Backend
}

// New creates a pool from an initial slice of backends.
func New(backends []*backend.Backend) *Pool {
	p := &Pool{}
	p.Set(backends)
	return p
}

// Set atomically replaces the entire backend list.
// Week 4 (config hot reload) and week 5 (Kubernetes discovery) call this.
func (p *Pool) Set(backends []*backend.Backend) {
	// GO NOTE: we copy the incoming slice. If we stored the caller's slice
	// directly, the caller could keep mutating it behind our lock, which would
	// silently reintroduce the data race we are trying to prevent.
	cp := make([]*backend.Backend, len(backends))
	copy(cp, backends)

	p.mu.Lock()
	defer p.mu.Unlock() // GO NOTE: defer runs when the function returns, even on panic.
	p.backends = cp
}

// All returns a snapshot of every backend, healthy or not.
func (p *Pool) All() []*backend.Backend {
	p.mu.RLock()
	defer p.mu.RUnlock()

	out := make([]*backend.Backend, len(p.backends))
	copy(out, p.backends)
	return out
}

// Healthy returns a snapshot of the backends currently eligible for traffic.
//
// This allocates a slice per request. That is fine at week-1 scale and it keeps
// the code obvious; if the week-6 benchmark shows it matters we can cache the
// healthy list and invalidate it on health transitions. Measure first.
func (p *Pool) Healthy() []*backend.Backend {
	p.mu.RLock()
	defer p.mu.RUnlock()

	out := make([]*backend.Backend, 0, len(p.backends))
	for _, b := range p.backends {
		if b.Healthy() {
			out = append(out, b)
		}
	}
	return out
}

// Len is the total number of backends.
func (p *Pool) Len() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.backends)
}

// Add appends a backend to the pool.
func (p *Pool) Add(b *backend.Backend) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.backends = append(p.backends, b)
}

// Remove deletes the backend with the given name and reports whether it existed.
func (p *Pool) Remove(name string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	for i, b := range p.backends {
		if b.Name == name {
			p.backends = append(p.backends[:i], p.backends[i+1:]...)
			return true
		}
	}
	return false
}
