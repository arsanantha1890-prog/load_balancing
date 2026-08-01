package strategy

import (
	"net/http"
	"sync/atomic"

	"github.com/arsanantha1890-prog/load_balancing/internal/backend"
)

type RoundRobin struct {
	counter atomic.Uint64
}

func init() {
	Register("round_robin", func() Strategy { return &RoundRobin{} })
}

func (rr *RoundRobin) Name() string { return "round_robin" }

func (rr *RoundRobin) Pick(_ *http.Request, healthy []*backend.Backend) (*backend.Backend, error) {
	n := uint64(len(healthy))
	if n == 0 {
		return nil, ErrNoBackends
	}

	i := rr.counter.Add(1) - 1
	return healthy[i%n], nil
}

var _ Strategy = (*RoundRobin)(nil)
