
package main

import (
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type result struct {
	latency   time.Duration
	waitTime  time.Duration // time spent waiting before dispatch (open-loop only)
	backend   string
	status    int
	failed    bool
	isRetry   bool
}

func main() {
	var (
		target   = flag.String("url", "http://127.0.0.1:8080/", "target URL")
		mode     = flag.String("mode", "closed", "closed (fixed concurrency) or open (fixed arrival rate)")
		// closed-loop flags
		n        = flag.Int("n", 300, "total requests (closed-loop)")
		c        = flag.Int("c", 30, "concurrent workers (closed-loop)")
		// open-loop flags
		rps      = flag.Float64("rps", 500, "requests per second (open-loop)")
		duration = flag.Duration("duration", 10*time.Second, "test duration (open-loop)")
		// shared
		timeout  = flag.Duration("timeout", 10*time.Second, "per-request timeout")
		scenario = flag.String("scenario", "", "scenario label for the output header")
	)
	flag.Parse()

	client := &http.Client{
		Timeout: *timeout,
		Transport: &http.Transport{
			MaxIdleConns:        500,
			MaxIdleConnsPerHost: 500,
			IdleConnTimeout:     30 * time.Second,
		},
	}

	if *scenario != "" {
		fmt.Printf("scenario: %s\n", *scenario)
	}

	var results []result
	var elapsed time.Duration

	switch *mode {
	case "closed":
		results, elapsed = runClosed(client, *target, *n, *c)
	case "open":
		results, elapsed = runOpen(client, *target, *rps, *duration)
	default:
		fmt.Fprintf(os.Stderr, "unknown mode %q: use 'closed' or 'open'\n", *mode)
		os.Exit(1)
	}

	report(results, elapsed, *mode)
}


func runClosed(client *http.Client, target string, n, c int) ([]result, time.Duration) {
	if c > n {
		c = n
	}

	jobs := make(chan struct{}, n)
	for i := 0; i < n; i++ {
		jobs <- struct{}{}
	}
	close(jobs)

	results := make([]result, 0, n)
	var mu sync.Mutex
	var wg sync.WaitGroup

	fmt.Printf("closed-loop: %d requests, %d workers at %s\n\n", n, c, target)
	start := time.Now()

	for w := 0; w < c; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := make([]result, 0, n/c+1)
			for range jobs {
				local = append(local, doRequest(client, target))
			}
			mu.Lock()
			results = append(results, local...)
			mu.Unlock()
		}()
	}

	wg.Wait()
	return results, time.Since(start)
}


func runOpen(client *http.Client, target string, rps float64, dur time.Duration) ([]result, time.Duration) {
	interval := time.Duration(float64(time.Second) / rps)
	totalRequests := int(rps * dur.Seconds())

	fmt.Printf("open-loop: %.0f rps for %v (%d requests) at %s\n\n", rps, dur, totalRequests, target)

	results := make([]result, totalRequests)
	var wg sync.WaitGroup
	var idx atomic.Int64

	start := time.Now()

	for i := 0; i < totalRequests; i++ {
		scheduledAt := start.Add(time.Duration(i) * interval)

		// Wait until the scheduled time.
		now := time.Now()
		if scheduledAt.After(now) {
			time.Sleep(scheduledAt.Sub(now))
		}

		wg.Add(1)
		go func(scheduled time.Time) {
			defer wg.Done()
			dispatched := time.Now()
			waitTime := dispatched.Sub(scheduled)

			r := doRequest(client, target)
			r.waitTime = waitTime

			j := idx.Add(1) - 1
			results[j] = r
		}(scheduledAt)
	}

	wg.Wait()
	elapsed := time.Since(start)

	// Trim to actual count (in case of early termination).
	actual := int(idx.Load())
	return results[:actual], elapsed
}

func doRequest(client *http.Client, target string) result {
	t0 := time.Now()
	resp, err := client.Get(target)
	if err != nil {
		return result{latency: time.Since(t0), failed: true}
	}

	backendName := resp.Header.Get("X-Gobalance-Backend")
	status := resp.StatusCode
	isRetry := resp.Header.Get("X-Gobalance-Retried") != ""

	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	return result{
		latency: time.Since(t0),
		backend: backendName,
		status:  status,
		failed:  status >= 500,
		isRetry: isRetry,
	}
}

// reporting

func report(results []result, elapsed time.Duration, mode string) {
	latencies := make([]time.Duration, 0, len(results))
	failures := 0
	perBackendCount := map[string]int{}
	perBackendTotal := map[string]time.Duration{}

	var totalWait time.Duration
	var maxWait time.Duration

	for _, r := range results {
		if r.failed {
			failures++
		}
		latencies = append(latencies, r.latency)
		if r.backend != "" {
			perBackendCount[r.backend]++
			perBackendTotal[r.backend] += r.latency
		}
		totalWait += r.waitTime
		if r.waitTime > maxWait {
			maxWait = r.waitTime
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
	fmt.Printf("  mode          %s\n", mode)
	fmt.Printf("  requests      %d\n", len(results))
	fmt.Printf("  failures      %d (%.1f%%)\n", failures, 100*float64(failures)/float64(len(results)))
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

	if mode == "open" {
		fmt.Println()
		fmt.Println("=== coordinated omission check ===")
		meanWait := time.Duration(0)
		if len(results) > 0 {
			meanWait = totalWait / time.Duration(len(results))
		}
		fmt.Printf("  mean wait     %v\n", meanWait.Round(time.Millisecond))
		fmt.Printf("  max wait      %v\n", maxWait.Round(time.Millisecond))
		fmt.Println("  (high wait = requests queued before dispatch = real overload)")
	}
	fmt.Println()

	fmt.Println("=== distribution ===")
	names := make([]string, 0, len(perBackendCount))
	for name := range perBackendCount {
		names = append(names, name)
	}
	sort.Strings(names)

	fmt.Printf("  %-12s %8s %8s %12s\n", "backend", "requests", "share", "mean latency")
	fmt.Println("  " + strings.Repeat("-", 46))
	for _, name := range names {
		count := perBackendCount[name]
		share := 100 * float64(count) / float64(len(results))
		avg := perBackendTotal[name] / time.Duration(count)
		fmt.Printf("  %-12s %8d %7.1f%% %12v\n", name, count, share, avg.Round(time.Millisecond))
	}
}

func randomDelay(median, sigma float64) time.Duration {
	return time.Duration(median * (1 + sigma*rand.NormFloat64()))
}
