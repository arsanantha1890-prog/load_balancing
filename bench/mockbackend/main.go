// Command mockbackend is a tiny upstream server used to test and benchmark
// GoBalance.
//
// It exists so you can control the one variable that decides which load
// balancing strategy wins: how long a backend takes to respond.
//
//	-name    identity returned in the body and the X-Backend header
//	-addr    listen address, e.g. :9001
//	-delay   base processing delay, e.g. 10ms
//	-jitter  random extra delay in [0, jitter)
//	-tail    probability (0..1) that a request is a "slow tail" request
//	-tail-delay  how slow those tail requests are
//
// Per-request overrides: /?delay=250ms and /?status=500
//
// Why this matters: with three identical instant backends, every strategy looks
// the same. The interesting result in week 6 comes from heterogeneous backends
// with heavy-tailed service times, and this flag set is how you create them.
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
		name      = flag.String("name", "mock", "backend identity")
		addr      = flag.String("addr", ":9001", "listen address")
		delay     = flag.Duration("delay", 10*time.Millisecond, "base processing delay")
		jitter    = flag.Duration("jitter", 0, "uniform random extra delay in [0, jitter)")
		tailProb  = flag.Float64("tail", 0, "probability a request is a slow-tail request (0..1)")
		tailDelay = flag.Duration("tail-delay", 500*time.Millisecond, "delay for slow-tail requests")
	)
	flag.Parse()

	var served atomic.Uint64
	healthy := &atomic.Bool{}
	healthy.Store(true)

	mux := http.NewServeMux()

	// /healthz is what the week-3 active health checker will poll.
	// /toggle flips it so you can simulate a failure without killing the process.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if !healthy.Load() {
			http.Error(w, "unhealthy", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintln(w, "ok")
	})

	mux.HandleFunc("/toggle", func(w http.ResponseWriter, r *http.Request) {
		newState := !healthy.Load()
		healthy.Store(newState)
		fmt.Fprintf(w, "%s healthy=%v\n", *name, newState)
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Per-request delay override wins over the flags.
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
		})
	})

	log.Printf("mockbackend %q listening on %s (delay=%v jitter=%v tail=%.2f@%v)",
		*name, *addr, *delay, *jitter, *tailProb, *tailDelay)

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
