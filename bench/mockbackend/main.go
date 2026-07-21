// Command mockbackend is a tiny upstream server used to test and benchmark
// GoBalance.
//
//	-name        identity returned in the body and the X-Backend header
//	-addr        listen address, e.g. :9001
//	-delay       base processing delay, e.g. 10ms
//	-jitter      random extra delay in [0, jitter)
//	-tail        probability (0..1) that a request is a "slow tail" request
//	-tail-delay  how slow those tail requests are
//	-fail-rate   probability (0..1) that a request returns 500  [week 3]
//
// Per-request overrides: /?delay=250ms and /?status=500
// Control endpoints:     /healthz  /toggle  /sick  /well
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
		failRate  = flag.Float64("fail-rate", 0, "probability a request returns 500 (0..1)")
	)
	flag.Parse()

	var served atomic.Uint64

	healthy := &atomic.Bool{}
	healthy.Store(true)

	// sick means "serve 500s but keep answering health checks normally".
	// That models a GREY FAILURE: a backend that looks fine to a shallow health
	// probe while failing real work. It is the case passive health checking
	// exists for, and it is far more common in production than a clean crash.
	sick := &atomic.Bool{}

	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if !healthy.Load() {
			http.Error(w, "unhealthy", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintln(w, "ok")
	})

	// Flip health so the ACTIVE checker notices.
	mux.HandleFunc("/toggle", func(w http.ResponseWriter, r *http.Request) {
		state := !healthy.Load()
		healthy.Store(state)
		fmt.Fprintf(w, "%s healthy=%v\n", *name, state)
	})

	// Start failing real traffic while still reporting healthy: grey failure.
	mux.HandleFunc("/sick", func(w http.ResponseWriter, r *http.Request) {
		sick.Store(true)
		fmt.Fprintf(w, "%s is now failing requests but still reports healthy\n", *name)
	})

	mux.HandleFunc("/well", func(w http.ResponseWriter, r *http.Request) {
		sick.Store(false)
		fmt.Fprintf(w, "%s recovered\n", *name)
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
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

	log.Printf("mockbackend %q listening on %s (delay=%v jitter=%v tail=%.2f@%v fail-rate=%.2f)",
		*name, *addr, *delay, *jitter, *tailProb, *tailDelay, *failRate)

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
