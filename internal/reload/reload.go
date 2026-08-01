package reload

import (
	"context"
	"log/slog"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/arsanantha1890-prog/load_balancing/internal/backend"
	"github.com/arsanantha1890-prog/load_balancing/internal/breaker"
	"github.com/arsanantha1890-prog/load_balancing/internal/config"
	"github.com/arsanantha1890-prog/load_balancing/internal/pool"
	"github.com/arsanantha1890-prog/load_balancing/internal/strategy"
)

type ReloadFunc func(newStrategy strategy.Strategy)

// Config holds what the reloader needs.
type Config struct {
	Path           string
	Pool           *pool.Pool
	BreakerCfg     config.Breaker
	DialTimeout    time.Duration
	ResponseTimeout time.Duration
	OnReload       ReloadFunc
	Log            *slog.Logger
}

// Watch watches the config file and applies changes until ctx is cancelled.
func Watch(ctx context.Context, cfg Config) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()

	if err := watcher.Add(cfg.Path); err != nil {
		return err
	}

	cfg.Log.Info("config watcher started", "path", cfg.Path)

	var debounce *time.Timer

	for {
		select {
		case <-ctx.Done():
			cfg.Log.Info("config watcher stopped")
			return nil

		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			if event.Op&(fsnotify.Write|fsnotify.Create) == 0 {
				continue
			}

			if debounce != nil {
				debounce.Stop()
			}
			debounce = time.AfterFunc(500*time.Millisecond, func() {
				apply(cfg)
			})

		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			cfg.Log.Error("config watcher error", "error", err)
		}
	}
}

func apply(cfg Config) {
	newCfg, err := config.Load(cfg.Path)
	if err != nil {
		cfg.Log.Error("config reload failed — keeping previous config",
			"error", err)
		return
	}

	// Build new backends.
	newBackends := make([]*backend.Backend, 0, len(newCfg.Backends))
	for _, bc := range newCfg.Backends {
		b, err := backend.New(bc.Name, bc.URL, bc.Weight,
			cfg.DialTimeout, cfg.ResponseTimeout)
		if err != nil {
			cfg.Log.Error("config reload: invalid backend — keeping previous config",
				"backend", bc.Name, "error", err)
			return
		}

		if cfg.BreakerCfg.Enabled {
			b.Breaker = breaker.New(breaker.Config{
				FailureThreshold: cfg.BreakerCfg.FailureThreshold,
				SuccessThreshold: cfg.BreakerCfg.SuccessThreshold,
				OpenTimeout:      cfg.BreakerCfg.OpenTimeout.Std(),
				MaxProbes:        cfg.BreakerCfg.MaxProbes,
			})
		}

		newBackends = append(newBackends, b)
	}

	oldBackends := cfg.Pool.All()
	cfg.Pool.Set(newBackends)

	added, removed, kept := diffBackends(oldBackends, newBackends)
	cfg.Log.Info("config reloaded — backend pool updated",
		"added", added, "removed", removed, "kept", kept,
		"total", len(newBackends))

	// Strategy reload.
	if cfg.OnReload != nil {
		newStrat, err := strategy.Get(newCfg.Strategy)
		if err != nil {
			cfg.Log.Error("config reload: invalid strategy — keeping previous",
				"strategy", newCfg.Strategy, "error", err)
			return
		}
		cfg.OnReload(newStrat)
		cfg.Log.Info("strategy reloaded", "strategy", newStrat.Name())
	}
}

func diffBackends(old, new []*backend.Backend) (added, removed, kept int) {
	oldMap := make(map[string]bool, len(old))
	for _, b := range old {
		oldMap[b.Name] = true
	}
	newMap := make(map[string]bool, len(new))
	for _, b := range new {
		newMap[b.Name] = true
	}

	for name := range newMap {
		if oldMap[name] {
			kept++
		} else {
			added++
		}
	}
	for name := range oldMap {
		if !newMap[name] {
			removed++
		}
	}
	return
}
