// Package retry implements a retry budget.
//
// THE PROBLEM: RETRY STORMS
//
// Retrying a failed request is obviously good for that one request. Retrying
// EVERY failed request is how a partial outage becomes a total one.
//
// Three backends, one dies. Every request routed to it fails and retries
// elsewhere. The two survivors now carry 100% of normal traffic plus 50% extra
// retry traffic -- 150% of what they were sized for. They slow down, start
// failing, and their failures generate more retries. The system collapses from
// a fault it had enough capacity to absorb.
//
// This is a genuine, famous outage pattern. The defence is to cap retries as a
// FRACTION OF SUCCESSFUL TRAFFIC rather than as a per-request count.
//
// THE ALGORITHM (gRPC's retry throttling)
//
//	every success  ->  tokens += ratio   (capped at max)
//	every failure  ->  tokens -= 1
//	retry allowed  ->  only while tokens > max/2
//
// The elegance is in what happens at the extremes:
//
//   - Healthy system: successes vastly outnumber failures, the bucket sits
//     full, and every retry that is wanted is permitted.
//   - One backend dies: failures drain tokens faster than successes refill
//     them, retries switch off within seconds, and the survivors are protected.
//   - Recovery: successes refill the bucket and retries come back automatically.
//
// No configuration of "how many retries", no timers, no manual intervention.
// The budget is a feedback loop.
package retry

import "sync"

// Budget is a token bucket that throttles retries. Safe for concurrent use.
type Budget struct {
	mu     sync.Mutex
	tokens float64
	max    float64
	ratio  float64
}

// NewBudget creates a budget that starts full.
//
// maxTokens sizes the reservoir: bigger means slower to react in both
// directions. ratio is how much credit one success earns; 0.2 means retries may
// be sustained at roughly 20% of successful request volume.
func NewBudget(maxTokens int, ratio float64) *Budget {
	if maxTokens <= 0 {
		maxTokens = 100
	}
	if ratio <= 0 {
		ratio = 0.2
	}
	return &Budget{
		tokens: float64(maxTokens),
		max:    float64(maxTokens),
		ratio:  ratio,
	}
}

// Allow reports whether a retry may proceed.
//
// The threshold is half the reservoir rather than zero. Waiting until the
// bucket is empty means retries keep flowing throughout the drain, which is
// exactly the window where they do the most damage. Cutting off at the halfway
// mark makes the response early rather than terminal.
func (b *Budget) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.tokens > b.max/2
}

// OnSuccess credits the bucket.
func (b *Budget) OnSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.tokens += b.ratio
	if b.tokens > b.max {
		b.tokens = b.max
	}
}

// OnFailure debits the bucket.
func (b *Budget) OnFailure() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.tokens--
	if b.tokens < 0 {
		b.tokens = 0
	}
}

// Tokens exposes the current balance for the admin endpoint and for tests.
func (b *Budget) Tokens() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.tokens
}
