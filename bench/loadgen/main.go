// Command loadgen is a small closed-loop HTTP load generator for GoBalance.
//
// It fires -n requests through -c concurrent workers and reports latency
// percentiles plus how the traffic was distributed across backends (read from
// the X-Gobalance-Backend header the balancer sets).
//
// Usage:
//
//	go run ./bench/loadgen -n 300 -c 30
//	go run ./bench/loadgen -n 300 -c 30 -url http://127.0.0.1:8080/
//
// IMPORTANT CAVEAT, and a good thing to understand now rather than in week 6:
// this is a CLOSED-LOOP generator. It keeps exactly -c requests in flight and
// only sends a new one when an old one finishes. That means a slow backend
// slows down the request rate itself, which hides part of the damage it causes.
// Real users do not politely wait — they keep arriving. That effect is called
// coordinated omission, and week 6 replaces this tool with k6's
// constant-arrival-rate executor, which is open-loop and does not suffer from
// it.
//
// For now, closed-loop is exactly right: it reproduces "30 users click at the
// same time" and it makes the round-robin problem visible in one command.
package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"
)

type result struct {
	latency time.Duration
	backend string
	status  int
	failed  bool
}

func main() {
	var (
		target  = flag.String("url", "http://127.0.0.1:8080/", "URL to hammer")
		n       = flag.Int("n", 300, "total number of requests")
		c       = flag.Int("c", 30, "number of concurrent workers")
		timeout = flag.Duration("timeout", 10*time.Second, "per-request timeout")
	)
	flag.Parse()

	if *n <= 0 || *c <= 0 {
		fmt.Fprintln(os.Stderr, "-n and -c must both be positive")
		os.Exit(1)
	}
	if *c > *n {
		*c = *n
	}

	// One shared client so connections are pooled and reused, the same way a
	// real client would. A fresh client per request would measure TCP handshake
	// cost instead of balancer behaviour.
	client := &http.Client{
		Timeout: *timeout,
		Transport: &http.Transport{
			MaxIdleConns:        *c * 2,
			MaxIdleConnsPerHost: *c * 2,
			IdleConnTimeout:     30 * time.Second,
		},
	}

	// A buffered channel as a work queue: fill it, close it, and let the
	// workers drain it. Closing is what tells the workers to stop.
	jobs := make(chan struct{}, *n)
	for i := 0; i < *n; i++ {
		jobs <- struct{}{}
	}
	close(jobs)

	results := make([]result, 0, *n)
	var mu sync.Mutex
	var wg sync.WaitGroup

	fmt.Printf("firing %d requests at %s with %d concurrent workers\n\n", *n, *target, *c)
	start := time.Now()

	for w := 0; w < *c; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			// Each worker collects into its own slice and merges once at the
			// end, so the mutex is taken -c times instead of -n times.
			local := make([]result, 0, *n / *c + 1)

			for range jobs {
				t0 := time.Now()
				resp, err := client.Get(*target)
				if err != nil {
					local = append(local, result{latency: time.Since(t0), failed: true})
					continue
				}

				backendName := resp.Header.Get("X-Gobalance-Backend")
				status := resp.StatusCode

				// Draining and closing the body is what lets the connection be
				// reused. Skip it and every request pays for a new TCP handshake.
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()

				local = append(local, result{
					latency: time.Since(t0),
					backend: backendName,
					status:  status,
					failed:  status >= 500,
				})
			}

			mu.Lock()
			results = append(results, local...)
			mu.Unlock()
		}()
	}

	wg.Wait()
	elapsed := time.Since(start)

	report(results, elapsed, *c)
}

func report(results []result, elapsed time.Duration, concurrency int) {
	latencies := make([]time.Duration, 0, len(results))
	failures := 0
	perBackendCount := map[string]int{}
	perBackendTotal := map[string]time.Duration{}

	for _, r := range results {
		if r.failed {
			failures++
		}
		latencies = append(latencies, r.latency)
		if r.backend != "" {
			perBackendCount[r.backend]++
			perBackendTotal[r.backend] += r.latency
		}
	}

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })

	pct := func(p float64) time.Duration {
		if len(latencies) == 0 {
			return 0
		}
		idx := int(float64(len(latencies)-1)*p + 0.5)
		return latencies[idx]
	}

	var sum time.Duration
	for _, l := range latencies {
		sum += l
	}
	mean := time.Duration(0)
	if len(latencies) > 0 {
		mean = sum / time.Duration(len(latencies))
	}

	fmt.Println("=== summary ===")
	fmt.Printf("  requests      %d\n", len(results))
	fmt.Printf("  concurrency   %d\n", concurrency)
	fmt.Printf("  failures      %d\n", failures)
	fmt.Printf("  wall time     %v\n", elapsed.Round(time.Millisecond))
	fmt.Printf("  throughput    %.1f req/s\n", float64(len(results))/elapsed.Seconds())
	fmt.Println()

	fmt.Println("=== latency ===")
	fmt.Printf("  mean          %v\n", mean.Round(time.Millisecond))
	fmt.Printf("  p50           %v\n", pct(0.50).Round(time.Millisecond))
	fmt.Printf("  p90           %v\n", pct(0.90).Round(time.Millisecond))
	fmt.Printf("  p95           %v\n", pct(0.95).Round(time.Millisecond))
	fmt.Printf("  p99           %v\n", pct(0.99).Round(time.Millisecond))
	if len(latencies) > 0 {
		fmt.Printf("  max           %v\n", latencies[len(latencies)-1].Round(time.Millisecond))
	}
	fmt.Println()

	fmt.Println("=== distribution ===")
	names := make([]string, 0, len(perBackendCount))
	for name := range perBackendCount {
		names = append(names, name)
	}
	sort.Strings(names)

	fmt.Printf("  %-10s %8s %8s %12s\n", "backend", "requests", "share", "mean latency")
	for _, name := range names {
		count := perBackendCount[name]
		share := 100 * float64(count) / float64(len(results))
		avg := perBackendTotal[name] / time.Duration(count)
		fmt.Printf("  %-10s %8d %7.1f%% %12v\n", name, count, share, avg.Round(time.Millisecond))
	}
}
