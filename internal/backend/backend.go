package backend

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/arsanantha1890-prog/load_balancing/internal/breaker"
	"github.com/arsanantha1890-prog/load_balancing/internal/retryctx"
)

type Backend struct {
	Name   string
	URL    *url.URL
	Weight int

	// Proxy forwards a request to this backend. 
	Proxy *httputil.ReverseProxy

	// Breaker is this backend's circuit breaker. Set once at construction time in main, before any request is served, and read-only afterwards. 
	Breaker *breaker.Breaker

	inflight       atomic.Int64  // requests currently being served
	total          atomic.Uint64 // requests ever dispatched here
	failures       atomic.Uint64 // requests that ended in an error or 5xx
	consecFailures atomic.Uint32 // failures in an unbroken run, for passive health
	healthy        atomic.Bool
}

// New builds a Backend from configuration values.
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

	b := &Backend{Name: name, URL: u, Weight: weight}
	b.healthy.Store(true)

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
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(u)
			pr.SetXForwarded()
			pr.Out.Host = pr.In.Host
		},
		Transport: transport,

		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if st := retryctx.From(r.Context()); st != nil {
				st.Err = err
				return
			}
			http.Error(w, "502 bad gateway: upstream unavailable", http.StatusBadGateway)
		},
	}

	return b, nil
}

func (b *Backend) IncInflight() int64 {
	b.total.Add(1)
	return b.inflight.Add(1)
}

// DecInflight records a completion.
func (b *Backend) DecInflight() int64 { return b.inflight.Add(-1) }

// Inflight is the number of requests currently in progress.
func (b *Backend) Inflight() int64 { return b.inflight.Load() }

// Total is the number of requests ever dispatched here.
func (b *Backend) Total() uint64 { return b.total.Load() }

// Failures is the cumulative failure count.
func (b *Backend) Failures() uint64 { return b.failures.Load() }

// ConsecutiveFailures is the current unbroken run of failures.
func (b *Backend) ConsecutiveFailures() uint32 { return b.consecFailures.Load() }

// RecordFailure counts a failed request and returns the new consecutive count.
// The passive health check in the proxy compares that against its threshold.
func (b *Backend) RecordFailure() uint32 {
	b.failures.Add(1)
	return b.consecFailures.Add(1)
}

// RecordSuccess clears the consecutive failure run.

func (b *Backend) RecordSuccess() { b.consecFailures.Store(0) }

// Healthy reports whether this backend may receive traffic.
func (b *Backend) Healthy() bool { return b.healthy.Load() }

// SetHealthy marks the backend up or down. Called by the health checker and by the proxy's passive checks.
func (b *Backend) SetHealthy(v bool) { b.healthy.Store(v) }

// BreakerState is a printable breaker state for the admin endpoint.
func (b *Backend) BreakerState() string {
	if b.Breaker == nil {
		return "disabled"
	}
	return b.Breaker.State().String()
}

// String makes Backend print nicely in logs.
func (b *Backend) String() string { return fmt.Sprintf("%s(%s)", b.Name, b.URL) }
