package strategy

import (
	"net/http"
	"sync/atomic"

	"github.com/arsanantha1890-prog/load_balancing/internal/backend"
)

// RoundRobin hands out backends in a fixed rotation.
//
// It is the baseline every other strategy in this project is measured against.
// It is also the default in nginx, kube-proxy and most cloud load balancers,
// which is precisely why beating it on tail latency is an interesting result.
//
// Implementation note worth knowing for interviews: there is no mutex here.
// A single atomic counter is enough, so RoundRobin has no lock contention at
// any request rate. Under a mutex, every request would serialise on one lock.
type RoundRobin struct {
	counter atomic.Uint64
}

// GO NOTE: init() runs automatically when the package is loaded. This is the
// idiomatic way to build a plugin registry in Go.
func init() {
	Register("round_robin", func() Strategy { return &RoundRobin{} })
}

func (rr *RoundRobin) Name() string { return "round_robin" }

func (rr *RoundRobin) Pick(_ *http.Request, healthy []*backend.Backend) (*backend.Backend, error) {
	n := uint64(len(healthy))
	if n == 0 {
		return nil, ErrNoBackends
	}

	// Add returns the NEW value, so subtract 1 to make the first request use
	// index 0. Wrapping past max uint64 is defined behaviour in Go and simply
	// wraps to 0, so this is correct forever.
	i := rr.counter.Add(1) - 1
	return healthy[i%n], nil
}

// Compile-time assertion that RoundRobin really is a Strategy.
//
// GO NOTE: this line allocates nothing and runs never. It exists so that if you
// break the interface, the compiler tells you here instead of somewhere far
// away. Every strategy file in this project ends with one.
var _ Strategy = (*RoundRobin)(nil)
