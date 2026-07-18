// Package backend models a single upstream server that GoBalance can send
// traffic to.
//
// A Backend holds three kinds of things:
//  1. static configuration (name, URL, weight) that never changes after New(),
//  2. live counters (in-flight requests, totals, failures) that are updated
//     concurrently by many goroutines, and
//  3. a *httputil.ReverseProxy, which is the standard library's HTTP proxy.
//
// GO NOTE: every incoming HTTP request is served by its own goroutine, so
// several goroutines touch the same Backend at the same time. That is why the
// counters are sync/atomic types instead of plain ints. Plain ints would be a
// data race, and `go test -race` would (correctly) fail the build.
package backend

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync/atomic"
	"time"
)

// Backend is one upstream server.
//
// GO NOTE: exported fields start with a capital letter; unexported ones do not.
// The counters are unexported so that the only way to change them is through
// the methods below, which keeps the concurrency rules in one place.
type Backend struct {
	Name   string
	URL    *url.URL
	Weight int

	// Proxy forwards a request to this backend. It is safe for concurrent use.
	Proxy *httputil.ReverseProxy

	inflight atomic.Int64  // requests currently being served by this backend
	total    atomic.Uint64 // requests ever dispatched to this backend
	failures atomic.Uint64 // requests that ended in a transport error or 5xx
	healthy  atomic.Bool   // week 3 will flip this from the health checker
}

// New builds a Backend from configuration values.
//
// GO NOTE: Go has no exceptions. A function that can fail returns an error as
// its last return value, and the caller checks it. This is the single most
// common pattern you will write in this project.
func New(name, rawURL string, weight int, dialTimeout, responseTimeout time.Duration) (*Backend, error) {
	if name == "" {
		return nil, fmt.Errorf("backend name must not be empty")
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("backend %q: invalid url %q: %w", name, rawURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("backend %q: url must start with http:// or https://, got %q", name, rawURL)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("backend %q: url %q has no host", name, rawURL)
	}
	if weight <= 0 {
		weight = 1
	}

	b := &Backend{
		Name:   name,
		URL:    u,
		Weight: weight,
	}
	b.healthy.Store(true) // optimistic until the week-3 health checker says otherwise

	// Each backend gets its own transport so that connection pools, and later
	// per-backend tuning, stay isolated.
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   dialTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   dialTimeout,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: responseTimeout,
		ForceAttemptHTTP2:     true,
	}

	b.Proxy = &httputil.ReverseProxy{
		// GO NOTE: Rewrite is the modern replacement for Director (Go 1.20+).
		// SetURL points the outbound request at this backend; SetXForwarded
		// sets X-Forwarded-For / -Host / -Proto correctly, including stripping
		// any values a client tried to spoof.
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(u)
			pr.SetXForwarded()
			pr.Out.Host = pr.In.Host // preserve the Host header the client sent
		},
		Transport: transport,
		// ErrorHandler runs when the upstream is unreachable, times out, or
		// speaks broken HTTP. Without it the standard library logs and writes a
		// bare 502; we want our own logging and status code.
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, "502 bad gateway: upstream unavailable", http.StatusBadGateway)
		},
	}

	return b, nil
}

// --- live counters -------------------------------------------------------

// IncInflight records that a request has been dispatched to this backend.
// Returns the new in-flight count.
func (b *Backend) IncInflight() int64 {
	b.total.Add(1)
	return b.inflight.Add(1)
}

// DecInflight records that a request to this backend has finished.
func (b *Backend) DecInflight() int64 { return b.inflight.Add(-1) }

// Inflight is the number of requests currently in progress.
// Least-connections and power-of-two-choices (week 2) read this.
func (b *Backend) Inflight() int64 { return b.inflight.Load() }

// Total is the number of requests ever dispatched here.
func (b *Backend) Total() uint64 { return b.total.Load() }

// Failures is the number of requests that ended badly.
func (b *Backend) Failures() uint64 { return b.failures.Load() }

// RecordFailure bumps the failure counter. Week 3 uses this for passive health
// checking and the circuit breaker.
func (b *Backend) RecordFailure() { b.failures.Add(1) }

// Healthy reports whether this backend is eligible to receive traffic.
func (b *Backend) Healthy() bool { return b.healthy.Load() }

// SetHealthy marks the backend up or down.
func (b *Backend) SetHealthy(v bool) { b.healthy.Store(v) }

// String makes Backend print nicely in logs.
//
// GO NOTE: implementing String() string satisfies the fmt.Stringer interface.
// Go interfaces are implicit: there is no "implements" keyword.
func (b *Backend) String() string { return fmt.Sprintf("%s(%s)", b.Name, b.URL) }
