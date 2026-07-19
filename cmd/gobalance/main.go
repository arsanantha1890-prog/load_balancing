// Command gobalance is an HTTP load balancer.
//
// It runs two servers:
//   - the proxy server (default :8080), which forwards client traffic
//   - the admin server (default :8081), which exposes /healthz and /backends
//
// Keeping them on separate ports matters: an admin path on the proxy port would
// shadow a real customer URL, and you never want the operations surface exposed
// to the same audience as the traffic surface.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/arsanantha1890-prog/load_balancing/internal/backend"
	"github.com/arsanantha1890-prog/load_balancing/internal/config"
	"github.com/arsanantha1890-prog/load_balancing/internal/pool"
	"github.com/arsanantha1890-prog/load_balancing/internal/proxy"
	"github.com/arsanantha1890-prog/load_balancing/internal/strategy"
)

func main() {
	// GO NOTE: main should do as little as possible. Putting the real work in
	// run() means every path can return an error instead of calling os.Exit
	// from deep inside the program, which would skip all deferred cleanup.
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "gobalance: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "gobalance.yaml", "path to the config file")
	logLevel := flag.String("log-level", "info", "debug | info | warn | error")
	strategyName := flag.String("strategy", "", "override the strategy named in the config file")
	flag.Parse()

	log := newLogger(*logLevel)

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	// Build one Backend per config entry.
	backends := make([]*backend.Backend, 0, len(cfg.Backends))
	for _, bc := range cfg.Backends {
		b, err := backend.New(bc.Name, bc.URL, bc.Weight,
			cfg.Timeouts.Dial.Std(), cfg.Timeouts.ResponseHeader.Std())
		if err != nil {
			return err
		}
		backends = append(backends, b)
		log.Info("registered backend", "name", b.Name, "url", b.URL.String(), "weight", b.Weight)
	}

	if *strategyName != "" {
		cfg.Strategy = *strategyName
	}

	strat, err := strategy.Get(cfg.Strategy)
	if err != nil {
		return err
	}

	p := pool.New(backends)
	balancer := proxy.New(p, strat, log)

	proxySrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           balancer,
		ReadHeaderTimeout: cfg.Timeouts.Read.Std(),
		ReadTimeout:       cfg.Timeouts.Read.Std(),
		WriteTimeout:      cfg.Timeouts.Write.Std(),
		IdleTimeout:       cfg.Timeouts.Idle.Std(),
	}

	adminSrv := &http.Server{
		Addr:              cfg.AdminListen,
		Handler:           adminHandler(balancer),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// GO NOTE: a buffered channel of size 2 so neither goroutine blocks forever
	// if both servers fail at once.
	errCh := make(chan error, 2)

	go func() {
		log.Info("proxy listening", "addr", cfg.Listen, "strategy", strat.Name(),
			"backends", len(backends))
		// ErrServerClosed is the normal result of a graceful Shutdown, not a failure.
		if err := proxySrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("proxy server: %w", err)
		}
	}()

	go func() {
		log.Info("admin listening", "addr", cfg.AdminListen)
		if err := adminSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("admin server: %w", err)
		}
	}()

	// Wait for either a fatal server error or Ctrl-C / SIGTERM.
	//
	// GO NOTE: signal.NotifyContext gives you a context that is cancelled when
	// the signal arrives. select waits on whichever channel fires first.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutdown signal received, draining in-flight requests",
			"grace", cfg.Timeouts.GracefulShutdown.Std())
	}

	// Graceful shutdown: stop accepting new connections, let in-flight requests
	// finish, then exit. Without this, a deploy drops live requests — and this
	// is exactly the behaviour a Kubernetes rolling update depends on.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Timeouts.GracefulShutdown.Std())
	defer cancel()

	_ = adminSrv.Shutdown(shutdownCtx)
	if err := proxySrv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown timed out: %w", err)
	}

	log.Info("shutdown complete")
	return nil
}

// adminHandler serves the operational endpoints.
func adminHandler(b *proxy.Balancer) http.Handler {
	mux := http.NewServeMux()

	// Liveness: is the process up at all?
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})

	// Readiness: is there anything to route to? Kubernetes uses this in week 5
	// to keep traffic away from a balancer with an empty pool.
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if len(b.Pool().Healthy()) == 0 {
			http.Error(w, "no healthy backends", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ready\n"))
	})

	// A human- and script-readable view of the pool. This is what you leave on
	// screen during a demo.
	mux.HandleFunc("GET /backends", func(w http.ResponseWriter, r *http.Request) {
		type view struct {
			Name     string `json:"name"`
			URL      string `json:"url"`
			Weight   int    `json:"weight"`
			Healthy  bool   `json:"healthy"`
			Inflight int64  `json:"inflight"`
			Total    uint64 `json:"total"`
			Failures uint64 `json:"failures"`
		}

		out := struct {
			Strategy string `json:"strategy"`
			Backends []view `json:"backends"`
		}{Strategy: b.Strategy().Name()}

		for _, be := range b.Pool().All() {
			out.Backends = append(out.Backends, view{
				Name:     be.Name,
				URL:      be.URL.String(),
				Weight:   be.Weight,
				Healthy:  be.Healthy(),
				Inflight: be.Inflight(),
				Total:    be.Total(),
				Failures: be.Failures(),
			})
		}

		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
	})

	return mux
}

func newLogger(level string) *slog.Logger {
	var lv slog.Level
	if err := lv.UnmarshalText([]byte(level)); err != nil {
		lv = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lv}))
}
