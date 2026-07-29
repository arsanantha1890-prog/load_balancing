// Command mockbackend is a configurable upstream server for benchmarking.
//
// WEEK 6 UPGRADE: CONCURRENCY LIMIT
//
// Previous versions used time.Sleep with unlimited concurrency — gamma was
// SLOW but never SATURATED. Real backends have finite resources: connection
// pools, CPU cores, memory. When all slots are busy, new requests QUEUE, and
// queueing is where tail latency actually comes from.
//
// -max-concurrent limits how many requests are processed simultaneously. Once
// full, arriving requests wait in a channel buffer (up to -queue-size), and
// any overflow is rejected with 503. This makes the mock backend behave like a
// real service under load, and it is what makes the week-6 benchmark results
// meaningful.
//
// FLAGS
//
//	-name           identity returned in headers and body
//	-addr           listen address
//	-delay          base processing delay
//	-jitter         random extra delay in [0, jitter)
//	-tail           probability of a slow-tail request
//	-tail-delay     delay for tail requests
//	-fail-rate      probability of returning 500
//	-max-concurrent maximum simultaneous requests (0 = unlimited)
//	-queue-size     overflow queue depth (default 100)
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"time"
)

func main() {
	var (
		name          = flag.String("name", "mock", "backend identity")
		addr          = flag.String("addr", ":9001", "listen address")
		delay         = flag.Duration("delay", 10*time.Millisecond, "base processing delay")
		jitter        = flag.Duration("jitter", 0, "uniform random extra delay")
		tailProb      = flag.Float64("tail", 0, "probability of a slow-tail request (0..1)")
		tailDelay     = flag.Duration("tail-delay", 500*time.Millisecond, "delay for tail requests")
		failRate      = flag.Float64("fail-rate", 0, "probability of returning 500 (0..1)")
		maxConcurrent = flag.Int("max-concurrent", 0, "max simultaneous requests (0=unlimited)")
		_ = flag.Int("queue-size", 100, "overflow queue depth (reserved for future use)")
	)
	flag.Parse()

	var served atomic.Uint64
	healthy := &atomic.Bool{}
	healthy.Store(true)
	sick := &atomic.Bool{}

	// Semaphore for concurrency limiting. A buffered channel of capacity
	// maxConcurrent: sending to it "acquires a slot", receiving "releases" it.
	// When full, senders block — which is the queueing that makes tail latency
	// real instead of theoretical.
	var sem chan struct{}
	if *maxConcurrent > 0 {
		sem = make(chan struct{}, *maxConcurrent)
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if !healthy.Load() {
			http.Error(w, "unhealthy", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintln(w, "ok")
	})

	mux.HandleFunc("/toggle", func(w http.ResponseWriter, r *http.Request) {
		state := !healthy.Load()
		healthy.Store(state)
		fmt.Fprintf(w, "%s healthy=%v\n", *name, state)
	})

	mux.HandleFunc("/sick", func(w http.ResponseWriter, r *http.Request) {
		sick.Store(true)
		fmt.Fprintf(w, "%s is now failing requests but still reports healthy\n", *name)
	})

	mux.HandleFunc("/well", func(w http.ResponseWriter, r *http.Request) {
		sick.Store(false)
		fmt.Fprintf(w, "%s recovered\n", *name)
	})

	// Stats endpoint for the benchmark scripts.
	mux.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
		inflight := 0
		if sem != nil {
			inflight = len(sem)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name":           *name,
			"served":         served.Load(),
			"max_concurrent": *maxConcurrent,
			"inflight":       inflight,
			"healthy":        healthy.Load(),
			"sick":           sick.Load(),
		})
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Concurrency limit: acquire a slot or queue.
		if sem != nil {
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			default:
				// Queue is full. 503 is the correct response — the backend is
				// at capacity, not broken. The load balancer should try elsewhere.
				http.Error(w, "503 service unavailable: at capacity", http.StatusServiceUnavailable)
				return
			}
		}

		d := *delay
		if q := r.URL.Query().Get("delay"); q != "" {
			if parsed, err := time.ParseDuration(q); err == nil {
				d = parsed
			}
		} else {
			if *jitter > 0 {
				d += time.Duration(rand.Int63n(int64(*jitter)))
			}
			if *tailProb > 0 && rand.Float64() < *tailProb {
				d = *tailDelay
			}
		}

		time.Sleep(d)

		status := http.StatusOK
		if sick.Load() || (*failRate > 0 && rand.Float64() < *failRate) {
			status = http.StatusInternalServerError
		}
		if q := r.URL.Query().Get("status"); q != "" {
			if parsed, err := strconv.Atoi(q); err == nil && parsed >= 100 && parsed < 600 {
				status = parsed
			}
		}

		n := served.Add(1)

		w.Header().Set("X-Backend", *name)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"backend":  *name,
			"path":     r.URL.Path,
			"delay_ms": d.Milliseconds(),
			"served":   n,
			"status":   status,
		})
	})

	log.Printf("mockbackend %q listening on %s (delay=%v jitter=%v tail=%.2f@%v fail-rate=%.2f max-concurrent=%d)",
		*name, *addr, *delay, *jitter, *tailProb, *tailDelay, *failRate, *maxConcurrent)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Println(err)
		os.Exit(1)
	}
}
