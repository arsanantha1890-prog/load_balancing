// Package breaker implements a per-backend circuit breaker.
//
// THE PROBLEM IT SOLVES
//
// When a backend dies, every request sent to it costs a full connect timeout --
// two seconds in our config. At 30 concurrent requests that is 30 goroutines
// and 30 sockets tied up for two seconds each, doing nothing but waiting to
// fail. The load balancer becomes slower than having no load balancer at all.
//
// Worse, those doomed requests keep the dead backend's queue full of retries at
// exactly the moment it might be trying to restart.
//
// A circuit breaker is a small state machine that says "we already know this is
// broken -- stop asking". It fails instantly instead of failing slowly, and it
// probes cautiously instead of stampeding.
//
// THE STATE MACHINE
//
//	CLOSED ──── FailureThreshold consecutive failures ────► OPEN
//	   ▲                                                      │
//	   │                                            OpenTimeout elapses
//	   │                                                      │
//	   └──── SuccessThreshold consecutive probes ──── HALF-OPEN
//	                                                          │
//	                              any probe fails ────────────┘
//	                                    (back to OPEN)
//
// CLOSED    normal operation, traffic flows, failures are counted
// OPEN      reject immediately without touching the network
// HALF-OPEN allow a strictly limited number of probe requests through to see
//           whether the backend has recovered
//
// HALF-OPEN is the part people get wrong. Going straight from OPEN back to
// CLOSED sends full production traffic at a backend that just came up cold --
// empty caches, no warm connection pool -- and knocks it straight back down.
// Letting exactly MaxProbes requests through at a time is the difference
// between a recovery and an oscillation.
package breaker

import (
	"sync"
	"time"
)

// State is the breaker's current position in the state machine.
type State int32

const (
	StateClosed State = iota
	StateOpen
	StateHalfOpen
)

func (s State) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half_open"
	default:
		return "unknown"
	}
}

// Config tunes the state machine.
type Config struct {
	// FailureThreshold is how many consecutive failures trip the breaker open.
	FailureThreshold int

	// SuccessThreshold is how many consecutive successful probes are needed in
	// HALF-OPEN before closing. Greater than 1 protects against one lucky
	// request being mistaken for a recovery.
	SuccessThreshold int

	// OpenTimeout is how long to stay OPEN before allowing probes.
	OpenTimeout time.Duration

	// MaxProbes is how many requests may be in flight at once in HALF-OPEN.
	MaxProbes int
}

func (c Config) withDefaults() Config {
	if c.FailureThreshold <= 0 {
		c.FailureThreshold = 5
	}
	if c.SuccessThreshold <= 0 {
		c.SuccessThreshold = 2
	}
	if c.OpenTimeout <= 0 {
		c.OpenTimeout = 5 * time.Second
	}
	if c.MaxProbes <= 0 {
		c.MaxProbes = 1
	}
	return c
}

// Breaker is safe for concurrent use.
type Breaker struct {
	cfg Config

	mu              sync.Mutex
	state           State
	consecFailures  int
	consecSuccesses int
	openedAt        time.Time
	probesInFlight  int

	// now is injectable so tests can advance time without sleeping.
	now func() time.Time
}

// New builds a breaker in the CLOSED state.
func New(cfg Config) *Breaker {
	return &Breaker{
		cfg:   cfg.withDefaults(),
		state: StateClosed,
		now:   time.Now,
	}
}

// SetClock replaces the time source. Tests only.
func (b *Breaker) SetClock(f func() time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.now = f
}

// State reports the current state, applying any pending OPEN -> HALF-OPEN
// transition first.
func (b *Breaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.maybeHalfOpenLocked()
	return b.state
}

// Ready reports whether this backend is worth considering, WITHOUT consuming a
// probe slot. The balancer calls this while filtering candidates, so it must
// stay side-effect free -- otherwise merely looking at a half-open backend
// would burn its probe budget.
func (b *Breaker) Ready() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.maybeHalfOpenLocked()

	switch b.state {
	case StateClosed:
		return true
	case StateHalfOpen:
		return b.probesInFlight < b.cfg.MaxProbes
	default:
		return false
	}
}

// Acquire reserves permission to send one request. It DOES consume a probe slot
// in HALF-OPEN. Every successful Acquire must be paired with exactly one
// Release, which is why the proxy calls it inside a function with a defer.
func (b *Breaker) Acquire() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.maybeHalfOpenLocked()

	switch b.state {
	case StateClosed:
		return true
	case StateHalfOpen:
		if b.probesInFlight < b.cfg.MaxProbes {
			b.probesInFlight++
			return true
		}
		return false
	default:
		return false
	}
}

// Release reports how a request that Acquire permitted turned out.
func (b *Breaker) Release(success bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case StateHalfOpen:
		if b.probesInFlight > 0 {
			b.probesInFlight--
		}
		if !success {
			// A probe failed. Straight back to OPEN and restart the timer --
			// do not give a flapping backend a second chance in the same cycle.
			b.tripLocked()
			return
		}
		b.consecSuccesses++
		if b.consecSuccesses >= b.cfg.SuccessThreshold {
			b.state = StateClosed
			b.consecFailures = 0
			b.consecSuccesses = 0
		}

	case StateClosed:
		if success {
			// CONSECUTIVE, not cumulative. One success resets the count. A
			// backend that fails 4 times then succeeds is flaky, not broken;
			// only an unbroken run of failures indicates a real outage.
			b.consecFailures = 0
			return
		}
		b.consecFailures++
		if b.consecFailures >= b.cfg.FailureThreshold {
			b.tripLocked()
		}

	case StateOpen:
		// A result arriving while OPEN is a straggler from before the trip.
		// Ignore it rather than letting stale news reopen or close anything.
	}
}

// maybeHalfOpenLocked promotes OPEN to HALF-OPEN once the timeout has elapsed.
// Callers must hold b.mu.
func (b *Breaker) maybeHalfOpenLocked() {
	if b.state == StateOpen && b.now().Sub(b.openedAt) >= b.cfg.OpenTimeout {
		b.state = StateHalfOpen
		b.consecSuccesses = 0
		b.probesInFlight = 0
	}
}

func (b *Breaker) tripLocked() {
	b.state = StateOpen
	b.openedAt = b.now()
	b.consecFailures = 0
	b.consecSuccesses = 0
	b.probesInFlight = 0
}
