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

	// SuccessThreshold is how many consecutive successful probes are needed in HALF-OPEN before closing. Greater than 1 protects against one lucky request being mistaken for a recovery.
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

func (b *Breaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.maybeHalfOpenLocked()
	return b.state
}

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
