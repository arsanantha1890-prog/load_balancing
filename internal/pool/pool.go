package pool

import (
	"sync"

	"github.com/arsanantha1890-prog/load_balancing/internal/backend"
)

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

func (p *Pool) Set(backends []*backend.Backend) {
	cp := make([]*backend.Backend, len(backends))
	copy(cp, backends)

	p.mu.Lock()
	defer p.mu.Unlock() 
	p.backends = cp
}

func (p *Pool) All() []*backend.Backend {
	p.mu.RLock()
	defer p.mu.RUnlock()

	out := make([]*backend.Backend, len(p.backends))
	copy(out, p.backends)
	return out
}

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
