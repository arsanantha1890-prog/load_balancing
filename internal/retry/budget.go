package retry

import "sync"

// Budget is a token bucket that throttles retries. Safe for concurrent use.
type Budget struct {
	mu     sync.Mutex
	tokens float64
	max    float64
	ratio  float64
}

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
