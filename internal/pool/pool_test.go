package pool_test

import (
	"sync"
	"testing"
	"time"

	"github.com/arsanantha1890-prog/load_balancing/internal/backend"
	"github.com/arsanantha1890-prog/load_balancing/internal/pool"
)

func mustBackend(t *testing.T, name, rawURL string) *backend.Backend {
	t.Helper()
	b, err := backend.New(name, rawURL, 1, time.Second, time.Second)
	if err != nil {
		t.Fatalf("backend.New(%q, %q) returned error: %v", name, rawURL, err)
	}
	return b
}

func TestHealthyFiltersUnhealthyBackends(t *testing.T) {
	a := mustBackend(t, "a", "http://127.0.0.1:9001")
	b := mustBackend(t, "b", "http://127.0.0.1:9002")
	p := pool.New([]*backend.Backend{a, b})

	if got := len(p.Healthy()); got != 2 {
		t.Fatalf("expected 2 healthy backends, got %d", got)
	}

	b.SetHealthy(false)

	healthy := p.Healthy()
	if len(healthy) != 1 {
		t.Fatalf("expected 1 healthy backend after marking b down, got %d", len(healthy))
	}
	if healthy[0].Name != "a" {
		t.Fatalf("expected the healthy backend to be %q, got %q", "a", healthy[0].Name)
	}
}

func TestRemove(t *testing.T) {
	p := pool.New([]*backend.Backend{
		mustBackend(t, "a", "http://127.0.0.1:9001"),
		mustBackend(t, "b", "http://127.0.0.1:9002"),
	})

	if !p.Remove("a") {
		t.Fatal("Remove(\"a\") returned false, want true")
	}
	if p.Remove("nope") {
		t.Fatal("Remove(\"nope\") returned true, want false")
	}
	if p.Len() != 1 {
		t.Fatalf("expected 1 backend left, got %d", p.Len())
	}
}

// TestConcurrentReadWrite is the test that justifies the RWMutex.
// Run it with: go test -race ./...
func TestConcurrentReadWrite(t *testing.T) {
	p := pool.New([]*backend.Backend{mustBackend(t, "a", "http://127.0.0.1:9001")})

	var wg sync.WaitGroup
	stop := make(chan struct{})

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = p.Healthy()
				}
			}
		}()
	}

	for i := 0; i < 100; i++ {
		p.Set([]*backend.Backend{
			mustBackend(t, "a", "http://127.0.0.1:9001"),
			mustBackend(t, "b", "http://127.0.0.1:9002"),
		})
	}

	close(stop)
	wg.Wait()
}
