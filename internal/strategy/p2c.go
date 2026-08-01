package strategy

import (
	"math/rand"
	"net/http"

	"github.com/arsanantha1890-prog/load_balancing/internal/backend"
)

type P2C struct{}

func init() {
	Register("p2c", func() Strategy { return &P2C{} })
}

func (P2C) Name() string { return "p2c" }

func (P2C) Pick(_ *http.Request, healthy []*backend.Backend) (*backend.Backend, error) {
	n := len(healthy)
	switch n {
	case 0:
		return nil, ErrNoBackends
	case 1:
		return healthy[0], nil
	}

	a, b := twoDistinct(n)
	first, second := healthy[a], healthy[b]

	if load(first) <= load(second) {
		return first, nil
	}
	return second, nil
}

// twoDistinct returns two different indices in [0, n) with uniform probability.
//
// The obvious version -- draw twice and retry on collision -- has an unbounded
// worst case. This one is branch-free and always terminates: draw j from a
// range one smaller, then shift it up past i if it would have collided.
func twoDistinct(n int) (int, int) {
	i := rand.Intn(n)
	j := rand.Intn(n - 1)
	if j >= i {
		j++
	}
	return i, j
}

var _ Strategy = (*P2C)(nil)
