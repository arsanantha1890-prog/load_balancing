// Command gobalance is an HTTP load balancer.
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

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/arsanantha1890-prog/load_balancing/internal/backend"
	"github.com/arsanantha1890-prog/load_balancing/internal/breaker"
	"github.com/arsanantha1890-prog/load_balancing/internal/config"
	"github.com/arsanantha1890-prog/load_balancing/internal/health"
	"github.com/arsanantha1890-prog/load_balancing/internal/metrics"
	"github.com/arsanantha1890-prog/load_balancing/internal/pool"
	"github.com/arsanantha1890-prog/load_balancing/internal/proxy"
	"github.com/arsanantha1890-prog/load_balancing/internal/reload"
	"github.com/arsanantha1890-prog/load_balancing/internal/retry"
	"github.com/arsanantha1890-prog/load_balancing/internal/strategy"
)

func main() {
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

	if *strategyName != "" {
		cfg.Strategy = *strategyName
	}

	// --- backends ---
	backends := make([]*backend.Backend, 0, len(cfg.Backends))
	for _, bc := range cfg.Backends {
		b, err := backend.New(bc.Name, bc.URL, bc.Weight,
			cfg.Timeouts.Dial.Std(), cfg.Timeouts.ResponseHeader.Std())
		if err != nil {
			return err
		}
		if cfg.Breaker.Enabled {
			b.Breaker = breaker.New(breaker.Config{
				FailureThreshold: cfg.Breaker.FailureThreshold,
				SuccessThreshold: cfg.Breaker.SuccessThreshold,
				OpenTimeout:      cfg.Breaker.OpenTimeout.Std(),
				MaxProbes:        cfg.Breaker.MaxProbes,
			})
		}
		backends = append(backends, b)
		log.Info("registered backend", "name", b.Name, "url", b.URL.String(), "weight", b.Weight)
	}

	strat, err := strategy.Get(cfg.Strategy)
	if err != nil {
		return err
	}

	p := pool.New(backends)
	budget := retry.NewBudget(cfg.Retry.BudgetMax, cfg.Retry.BudgetRatio)

	// --- metrics ---
	m := metrics.New()

	balancer := proxy.New(p, strat, log,
		proxy.WithRetry(proxy.RetryConfig{
			Enabled:    cfg.Retry.Enabled,
			MaxRetries: cfg.Retry.MaxRetries,
		}, budget),
		proxy.WithPassiveHealth(proxy.PassiveConfig{
			Enabled:             cfg.Passive.Enabled,
			ConsecutiveFailures: uint32(cfg.Passive.ConsecutiveFailures),
		}),
		proxy.WithMetrics(m),
	)

	// --- background workers ---
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Health checker.
	checker := health.New(p, health.Config{
		Enabled:            cfg.Health.Enabled,
		Path:               cfg.Health.Path,
		Interval:           cfg.Health.Interval.Std(),
		Timeout:            cfg.Health.Timeout.Std(),
		HealthyThreshold:   cfg.Health.HealthyThreshold,
		UnhealthyThreshold: cfg.Health.UnhealthyThreshold,
	}, log)
	go checker.Run(ctx)

	// Metrics collector: syncs health/breaker gauges once per second.
	go m.RunCollector(ctx, p, budget, time.Second)

	// Config watcher: hot reload.
	go func() {
		if err := reload.Watch(ctx, reload.Config{
			Path:            *configPath,
			Pool:            p,
			BreakerCfg:      cfg.Breaker,
			DialTimeout:     cfg.Timeouts.Dial.Std(),
			ResponseTimeout: cfg.Timeouts.ResponseHeader.Std(),
			OnReload:        balancer.SwapStrategy,
			Log:             log,
		}); err != nil {
			log.Error("config watcher failed", "error", err)
		}
	}()

	// --- servers ---
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
		Handler:           adminHandler(balancer, m),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 2)

	go func() {
		log.Info("proxy listening", "addr", cfg.Listen, "strategy", strat.Name(),
			"backends", len(backends), "retry", cfg.Retry.Enabled, "breaker", cfg.Breaker.Enabled)
		if err := proxySrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("proxy server: %w", err)
		}
	}()

	go func() {
		log.Info("admin listening", "addr", cfg.AdminListen, "metrics", "/metrics")
		if err := adminSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("admin server: %w", err)
		}
	}()

	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-errCh:
		return err
	case <-sigCtx.Done():
		log.Info("shutdown signal received", "grace", cfg.Timeouts.GracefulShutdown.Std())
	}

	cancel() // stop health checker, metrics collector, config watcher

	shutdownCtx, shutCancel := context.WithTimeout(context.Background(), cfg.Timeouts.GracefulShutdown.Std())
	defer shutCancel()

	_ = adminSrv.Shutdown(shutdownCtx)
	if err := proxySrv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown timed out: %w", err)
	}

	log.Info("shutdown complete")
	return nil
}

func adminHandler(b *proxy.Balancer, m *metrics.Metrics) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})

	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if len(b.Pool().Healthy()) == 0 {
			http.Error(w, "no healthy backends", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ready\n"))
	})

	mux.HandleFunc("GET /backends", func(w http.ResponseWriter, r *http.Request) {
		type view struct {
			Name           string `json:"name"`
			URL            string `json:"url"`
			Weight         int    `json:"weight"`
			Healthy        bool   `json:"healthy"`
			Breaker        string `json:"breaker"`
			Inflight       int64  `json:"inflight"`
			Total          uint64 `json:"total"`
			Failures       uint64 `json:"failures"`
			ConsecFailures uint32 `json:"consecutive_failures"`
		}

		out := struct {
			Strategy     string  `json:"strategy"`
			RetryTokens  float64 `json:"retry_budget_tokens"`
			HealthyCount int     `json:"healthy_backends"`
			Backends     []view  `json:"backends"`
		}{
			Strategy:     b.Strategy().Name(),
			HealthyCount: len(b.Pool().Healthy()),
		}

		if budget := b.Budget(); budget != nil {
			out.RetryTokens = budget.Tokens()
		}

		for _, be := range b.Pool().All() {
			out.Backends = append(out.Backends, view{
				Name:           be.Name,
				URL:            be.URL.String(),
				Weight:         be.Weight,
				Healthy:        be.Healthy(),
				Breaker:        be.BreakerState(),
				Inflight:       be.Inflight(),
				Total:          be.Total(),
				Failures:       be.Failures(),
				ConsecFailures: be.ConsecutiveFailures(),
			})
		}

		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
	})

	// Prometheus metrics endpoint.
	mux.Handle("GET /metrics", promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{}))

	return mux
}

func newLogger(level string) *slog.Logger {
	var lv slog.Level
	if err := lv.UnmarshalText([]byte(level)); err != nil {
		lv = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lv}))
}
