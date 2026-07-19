package strategy

import (
	"math/rand"
	"net/http"

	"github.com/arsanantha1890-prog/load_balancing/internal/backend"
)

// P2C is power-of-two-choices: sample two backends at random, send the request
// to whichever has fewer requests in flight.
//
// This is the headline algorithm of the project. It is what Envoy, Finagle,
// HAProxy and most modern service meshes actually use, and the reason is a
// beautiful result from balls-into-bins analysis:
//
//	random choice          -> max load grows as  log n / log log n
//	best of two random     -> max load grows as  log log n
//
// That is an EXPONENTIAL improvement in the worst case, from sampling one extra
// backend. Sampling three or more buys you almost nothing on top -- the jump is
// entirely between d=1 and d=2. Being able to state that is the single most
// quotable fact in this project.
//
// Why not just always pick the global minimum (least_connections)? Two reasons,
// both of which matter at scale:
//
//  1. COST. Least-connections is O(n) per request. P2C is O(1) regardless of
//     how many backends exist. At 10,000 backends that is the difference
//     between a scan and two array lookups.
//
//  2. HERD BEHAVIOUR. This is the important one. When several load balancer
//     replicas each independently pick the globally least-loaded backend, they
//     all pick the SAME one simultaneously and stampede it -- then all move
//     together to the next victim. Randomised sampling means replicas make
//     uncorrelated choices, so the herd never forms. P2C degrades gracefully
//     under replication; least-connections does not.
//
// Point 2 is a real distributed-systems answer to "what breaks if you run more
// than one of these?", and it is the kind of thing interviewers remember.
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
