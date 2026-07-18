package strategy

import (
	"math/rand"
	"net/http"

	"github.com/arsanantha1890-prog/load_balancing/internal/backend"
)

// Random picks a uniformly random backend.
//
// It exists in week 1 for two reasons. It proves the plugin registry works
// (switch strategies by editing one line of YAML), and it is the honest
// statistical baseline: round-robin only beats random because it removes
// variance, not because it is smarter about load.
//
// It is also half of power-of-two-choices, which you write in week 2.
type Random struct{}

func init() {
	Register("random", func() Strategy { return &Random{} })
}

func (Random) Name() string { return "random" }

func (Random) Pick(_ *http.Request, healthy []*backend.Backend) (*backend.Backend, error) {
	if len(healthy) == 0 {
		return nil, ErrNoBackends
	}
	// GO NOTE: the global math/rand source is safe for concurrent use and is
	// automatically seeded since Go 1.20, so no rand.Seed call is needed.
	return healthy[rand.Intn(len(healthy))], nil
}

var _ Strategy = (*Random)(nil)
