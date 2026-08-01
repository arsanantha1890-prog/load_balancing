package health

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/arsanantha1890-prog/load_balancing/internal/backend"
	"github.com/arsanantha1890-prog/load_balancing/internal/pool"
)

// Config tunes the prober.
type Config struct {
	Enabled            bool
	Path               string
	Interval           time.Duration
	Timeout            time.Duration
	HealthyThreshold   int
	UnhealthyThreshold int
}

func (c Config) withDefaults() Config {
	if c.Path == "" {
		c.Path = "/healthz"
	}
	if c.Interval <= 0 {
		c.Interval = 2 * time.Second
	}
	if c.Timeout <= 0 {
		c.Timeout = time.Second
	}
	if c.HealthyThreshold <= 0 {
		c.HealthyThreshold = 2
	}
	if c.UnhealthyThreshold <= 0 {
		c.UnhealthyThreshold = 3
	}
	return c
}

type probeState struct {
	successes int
	failures  int
}

// Checker probes every backend in a pool on an interval.
type Checker struct {
	pool   *pool.Pool
	cfg    Config
	log    *slog.Logger
	client *http.Client

	mu    sync.Mutex
	state map[string]*probeState
}

// New builds a Checker.
func New(p *pool.Pool, cfg Config, log *slog.Logger) *Checker {
	cfg = cfg.withDefaults()

	return &Checker{
		pool:  p,
		cfg:   cfg,
		log:   log,
		state: make(map[string]*probeState),
		client: &http.Client{
			Timeout: cfg.Timeout,
			// Health checks get their own transport. Sharing the proxy's
			// connection pool would mean a backend saturated with real traffic
			// could starve its own health check of a connection and be declared
			// dead for being busy -- a spectacular way to cause an outage.
			Transport: &http.Transport{
				MaxIdleConns:        16,
				MaxIdleConnsPerHost: 2,
				IdleConnTimeout:     30 * time.Second,
				DisableKeepAlives:   false,
			},
		},
	}
}

// Run probes until ctx is cancelled. Call it in a goroutine.
func (c *Checker) Run(ctx context.Context) {
	if !c.cfg.Enabled {
		c.log.Info("active health checking is disabled")
		return
	}

	c.log.Info("health checker started",
		"path", c.cfg.Path,
		"interval", c.cfg.Interval,
		"unhealthy_after", c.cfg.UnhealthyThreshold,
		"healthy_after", c.cfg.HealthyThreshold,
	)

	ticker := time.NewTicker(c.cfg.Interval)
	defer ticker.Stop()

	// Probe once immediately rather than waiting a full interval, so a backend
	// that is already down at startup is never sent a request.
	c.checkAll(ctx)

	for {
		select {
		case <-ctx.Done():
			c.log.Info("health checker stopped")
			return
		case <-ticker.C:
			c.checkAll(ctx)
		}
	}
}

// checkAll probes every backend concurrently.
//
// Reading the pool on every tick rather than capturing it once means week 4's
// config reload and week 5's endpoint watcher work with no changes here.
func (c *Checker) checkAll(ctx context.Context) {
	backends := c.pool.All()

	var wg sync.WaitGroup
	for _, b := range backends {
		wg.Add(1)
		go func(b *backend.Backend) {
			defer wg.Done()
			c.probe(ctx, b)
		}(b)
	}
	wg.Wait()

	c.prune(backends)
}

func (c *Checker) probe(ctx context.Context, b *backend.Backend) {
	url := strings.TrimSuffix(b.URL.String(), "/") + c.cfg.Path

	reqCtx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	ok := false
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err == nil {
		resp, err := c.client.Do(req)
		if err == nil {
			// Drain a bounded amount so the connection can be reused, without
			// letting a misbehaving backend stream megabytes into a health check.
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
			ok = resp.StatusCode >= 200 && resp.StatusCode < 400
		}
	}

	c.record(b, ok)
}

func (c *Checker) record(b *backend.Backend, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	st, exists := c.state[b.Name]
	if !exists {
		st = &probeState{}
		c.state[b.Name] = st
	}

	if ok {
		st.failures = 0
		st.successes++

		if !b.Healthy() && st.successes >= c.cfg.HealthyThreshold {
			b.SetHealthy(true)
			c.log.Info("backend restored to the pool",
				"backend", b.Name, "consecutive_successes", st.successes)
		}
		return
	}

	st.successes = 0
	st.failures++

	if b.Healthy() && st.failures >= c.cfg.UnhealthyThreshold {
		b.SetHealthy(false)
		c.log.Warn("backend ejected from the pool",
			"backend", b.Name, "consecutive_failures", st.failures)
	}
}

// prune drops probe state for backends that no longer exist, so the map cannot
// grow without bound across week 4 reloads and week 5 endpoint churn.
func (c *Checker) prune(current []*backend.Backend) {
	live := make(map[string]bool, len(current))
	for _, b := range current {
		live[b.Name] = true
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	for name := range c.state {
		if !live[name] {
			delete(c.state, name)
		}
	}
}
