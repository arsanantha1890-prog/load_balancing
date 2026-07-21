package retry_test

import (
	"testing"

	"github.com/arsanantha1890-prog/load_balancing/internal/retry"
)

func TestStartsFullAndAllows(t *testing.T) {
	b := retry.NewBudget(100, 0.2)

	if got := b.Tokens(); got != 100 {
		t.Fatalf("tokens = %v at construction, want 100", got)
	}
	if !b.Allow() {
		t.Fatal("a full budget must allow retries")
	}
}

// The behaviour that matters: sustained failure switches retries off before the
// bucket is empty, so the survivors are protected early rather than terminally.
func TestSustainedFailuresExhaustTheBudget(t *testing.T) {
	b := retry.NewBudget(100, 0.2)

	for i := 0; i < 50; i++ {
		b.OnFailure()
	}

	if b.Allow() {
		t.Fatalf("budget still allowing retries at %v tokens; threshold is max/2 = 50", b.Tokens())
	}
}

func TestSuccessesRefillAndRestoreRetries(t *testing.T) {
	b := retry.NewBudget(100, 0.2)

	for i := 0; i < 60; i++ {
		b.OnFailure()
	}
	if b.Allow() {
		t.Fatal("expected retries to be throttled after 60 failures")
	}

	// 0.2 tokens per success, so ~50 successes to climb from 40 back over 50.
	for i := 0; i < 60; i++ {
		b.OnSuccess()
	}

	if !b.Allow() {
		t.Fatalf("retries did not recover; tokens = %v", b.Tokens())
	}
}

func TestTokensNeverExceedMax(t *testing.T) {
	b := retry.NewBudget(10, 0.5)

	for i := 0; i < 1000; i++ {
		b.OnSuccess()
	}

	if got := b.Tokens(); got != 10 {
		t.Fatalf("tokens = %v, want the cap of 10", got)
	}
}

func TestTokensNeverGoNegative(t *testing.T) {
	b := retry.NewBudget(10, 0.5)

	for i := 0; i < 1000; i++ {
		b.OnFailure()
	}

	if got := b.Tokens(); got != 0 {
		t.Fatalf("tokens = %v, want a floor of 0", got)
	}
}

// A healthy system has far more successes than failures, so the budget should
// sit full and never interfere with legitimate retries.
func TestHealthySystemKeepsRetriesAvailable(t *testing.T) {
	b := retry.NewBudget(100, 0.2)

	for i := 0; i < 1000; i++ {
		b.OnSuccess()
		if i%50 == 0 {
			b.OnFailure()
		}
	}

	if !b.Allow() {
		t.Fatalf("a mostly healthy system throttled retries; tokens = %v", b.Tokens())
	}
}
