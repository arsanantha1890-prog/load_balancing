package breaker_test

import (
	"testing"
	"time"

	"github.com/arsanantha1890-prog/load_balancing/internal/breaker"
)

func newTestBreaker() (*breaker.Breaker, func(time.Duration)) {
	now := time.Now()
	b := breaker.New(breaker.Config{
		FailureThreshold: 3,
		SuccessThreshold: 2,
		OpenTimeout:      5 * time.Second,
		MaxProbes:        1,
	})
	b.SetClock(func() time.Time { return now })
	advance := func(d time.Duration) { now = now.Add(d) }
	return b, advance
}

func TestStartsClosed(t *testing.T) {
	b, _ := newTestBreaker()
	if b.State() != breaker.StateClosed {
		t.Fatalf("state = %v, want closed", b.State())
	}
	if !b.Acquire() {
		t.Fatal("a closed breaker must allow requests")
	}
}

func TestTripsAfterConsecutiveFailures(t *testing.T) {
	b, _ := newTestBreaker()

	for i := 0; i < 3; i++ {
		if !b.Acquire() {
			t.Fatalf("Acquire denied at failure %d while still closed", i)
		}
		b.Release(false)
	}

	if b.State() != breaker.StateOpen {
		t.Fatalf("state = %v after 3 consecutive failures, want open", b.State())
	}
	if b.Acquire() {
		t.Fatal("an open breaker must reject requests immediately")
	}
}

// One success in the middle means the backend is flaky, not down. The count is
// consecutive, so the breaker must NOT trip.
func TestSuccessResetsFailureCount(t *testing.T) {
	b, _ := newTestBreaker()

	b.Acquire()
	b.Release(false)
	b.Acquire()
	b.Release(false)
	b.Acquire()
	b.Release(true) // reset
	b.Acquire()
	b.Release(false)
	b.Acquire()
	b.Release(false)

	if b.State() != breaker.StateClosed {
		t.Fatalf("state = %v, want closed -- a success should have reset the run", b.State())
	}
}

func TestOpenBecomesHalfOpenAfterTimeout(t *testing.T) {
	b, advance := newTestBreaker()

	for i := 0; i < 3; i++ {
		b.Acquire()
		b.Release(false)
	}
	if b.State() != breaker.StateOpen {
		t.Fatal("expected open")
	}

	advance(4 * time.Second)
	if b.State() != breaker.StateOpen {
		t.Fatal("breaker reopened too early -- timeout has not elapsed")
	}

	advance(2 * time.Second)
	if b.State() != breaker.StateHalfOpen {
		t.Fatalf("state = %v after the open timeout, want half_open", b.State())
	}
}

// The whole point of HALF-OPEN: a limited number of probes, not full traffic.
func TestHalfOpenLimitsConcurrentProbes(t *testing.T) {
	b, advance := newTestBreaker()

	for i := 0; i < 3; i++ {
		b.Acquire()
		b.Release(false)
	}
	advance(6 * time.Second)

	if !b.Acquire() {
		t.Fatal("half-open must allow the first probe")
	}
	if b.Acquire() {
		t.Fatal("half-open allowed a second concurrent probe; MaxProbes is 1")
	}

	b.Release(true) // probe finished, slot freed
	if !b.Acquire() {
		t.Fatal("half-open should allow another probe once the first completed")
	}
}

func TestHalfOpenClosesAfterEnoughSuccesses(t *testing.T) {
	b, advance := newTestBreaker()

	for i := 0; i < 3; i++ {
		b.Acquire()
		b.Release(false)
	}
	advance(6 * time.Second)

	b.Acquire()
	b.Release(true)
	if b.State() != breaker.StateHalfOpen {
		t.Fatalf("state = %v after 1 of 2 required successes, want half_open", b.State())
	}

	b.Acquire()
	b.Release(true)
	if b.State() != breaker.StateClosed {
		t.Fatalf("state = %v after 2 successes, want closed", b.State())
	}
}

// A failed probe must go straight back to OPEN and restart the timer. Giving a
// flapping backend repeated chances in the same cycle is how you get an
// oscillating system.
func TestFailedProbeReopens(t *testing.T) {
	b, advance := newTestBreaker()

	for i := 0; i < 3; i++ {
		b.Acquire()
		b.Release(false)
	}
	advance(6 * time.Second)

	b.Acquire()
	b.Release(false)

	if b.State() != breaker.StateOpen {
		t.Fatalf("state = %v after a failed probe, want open", b.State())
	}
	if b.Acquire() {
		t.Fatal("breaker must be closed to traffic again after a failed probe")
	}
}

// Ready must never consume a probe slot -- the balancer calls it on every
// candidate while filtering, and burning the budget there would starve probes.
func TestReadyDoesNotConsumeProbes(t *testing.T) {
	b, advance := newTestBreaker()

	for i := 0; i < 3; i++ {
		b.Acquire()
		b.Release(false)
	}
	advance(6 * time.Second)

	for i := 0; i < 10; i++ {
		if !b.Ready() {
			t.Fatalf("Ready returned false on call %d; it must be side-effect free", i)
		}
	}
	if !b.Acquire() {
		t.Fatal("the probe slot was consumed by Ready")
	}
}
