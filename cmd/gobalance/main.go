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
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/arsanantha1890-prog/load_balancing/internal/backend"
	"github.com/arsanantha1890-prog/load_balancing/internal/breaker"
	"github.com/arsanantha1890-prog/load_balancing/internal/config"
	"github.com/arsanantha1890-prog/load_balancing/internal/discovery"
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

	// --- pool (empty if kubernetes mode; populated by provider) ---
	p := pool.New(nil)

	// --- discovery provider ---
	var provider discovery.Provider

	switch cfg.Discovery.Mode {
	case "kubernetes":
		client, err := buildKubeClient(cfg.Discovery.Kubeconfig)
		if err != nil {
			return fmt.Errorf("building kubernetes client: %w", err)
		}

		var brkCfg *breaker.Config
		if cfg.Breaker.Enabled {
			brkCfg = &breaker.Config{
				FailureThreshold: cfg.Breaker.FailureThreshold,
				SuccessThreshold: cfg.Breaker.SuccessThreshold,
				OpenTimeout:      cfg.Breaker.OpenTimeout.Std(),
				MaxProbes:        cfg.Breaker.MaxProbes,
			}
		}

		provider = discovery.NewKubernetesProvider(client, p, discovery.KubernetesConfig{
			ServiceName:     cfg.Discovery.ServiceName,
			Namespace:       cfg.Discovery.Namespace,
			PortName:        cfg.Discovery.PortName,
			BreakerCfg:      brkCfg,
			DialTimeout:     cfg.Timeouts.Dial.Std(),
			ResponseTimeout: cfg.Timeouts.ResponseHeader.Std(),
		}, log)

		log.Info("discovery mode: kubernetes",
			"service", cfg.Discovery.ServiceName,
			"namespace", cfg.Discovery.Namespace)

	default: // "static"
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
		p.Set(backends)
		provider = discovery.StaticProvider{}
		log.Info("discovery mode: static", "backends", len(backends))
	}

	strat, err := strategy.Get(cfg.Strategy)
	if err != nil {
		return err
	}

	budget := retry.NewBudget(cfg.Retry.BudgetMax, cfg.Retry.BudgetRatio)
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

	// Discovery provider.
	go func() {
		if err := provider.Run(ctx); err != nil {
			log.Error("discovery provider failed", "error", err)
		}
	}()

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

	// Metrics collector.
	go m.RunCollector(ctx, p, budget, time.Second)

	// Config watcher (only in static mode — in k8s mode the informer is the source of truth).
	if cfg.Discovery.Mode == "static" {
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
	}

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
			"retry", cfg.Retry.Enabled, "breaker", cfg.Breaker.Enabled)
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

	cancel()

	shutdownCtx, shutCancel := context.WithTimeout(context.Background(), cfg.Timeouts.GracefulShutdown.Std())
	defer shutCancel()

	_ = adminSrv.Shutdown(shutdownCtx)
	if err := proxySrv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown timed out: %w", err)
	}

	log.Info("shutdown complete")
	return nil
}

// buildKubeClient creates a Kubernetes clientset from either a kubeconfig file
// or in-cluster service account credentials.
//
// The order mirrors kubectl: explicit path wins, then KUBECONFIG env var, then
// in-cluster. This is the standard pattern used by every Kubernetes controller.
func buildKubeClient(kubeconfigPath string) (kubernetes.Interface, error) {
	var cfg *rest.Config
	var err error

	if kubeconfigPath != "" {
		cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	} else if kc := os.Getenv("KUBECONFIG"); kc != "" {
		cfg, err = clientcmd.BuildConfigFromFlags("", kc)
	} else {
		cfg, err = rest.InClusterConfig()
	}

	if err != nil {
		return nil, err
	}

	return kubernetes.NewForConfig(cfg)
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
