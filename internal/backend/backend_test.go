package backend_test

import (
	"sync"
	"testing"
	"time"

	"github.com/arsanantha1890-prog/gobalance/internal/backend"
)

func TestInflightRace(t *testing.T) {
	b, err := backend.New("test", "http://127.0.0.1:9999", 1, time.Second, time.Second)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b.IncInflight()
			_ = b.Inflight()
			b.DecInflight()
		}()
	}
	wg.Wait()
}