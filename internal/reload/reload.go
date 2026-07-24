// Package reload watches gobalance.yaml for changes and applies them without
// dropping connections.
//
// HOT RELOAD IS NOT A RESTART
//
// A restart drops every in-flight request. In a system handling real traffic,
// "edit the config and restart" means every deploy is a brief outage. Hot
// reload means the operator edits the file, saves, and the balancer picks it up
// within a second -- with zero dropped connections.
//
// WHAT CAN BE RELOADED
//
//   - Backend list: add, remove, change URL or weight. Removed backends finish
//     in-flight requests before being dropped (draining).
//   - Strategy: switching from round_robin to p2c takes effect on the next request.
//
// WHAT CANNOT BE RELOADED (and why)
//
//   - Listen addresses: the TCP listener is already bound; changing it requires
//     closing and reopening the socket, which IS a restart.
//   - TLS certificates: same reason -- the listener holds the cert.
//
// These limitations are stated explicitly because they are often asked in
// interviews: "what can't you hot-reload and why?" The answer is "anything
// that requires re-binding a socket".
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

// ReloadFunc is called whenever a valid new config is loaded.
// The proxy calls this to swap its strategy atomically.
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

	// Debounce: editors write multiple events per save (write, chmod, rename).
	// Reloading on each one wastes work and can hit a half-written file. Wait
	// for a quiet period before applying.
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

	// Swap the pool atomically. pool.Set copies the slice, so old backends can
	// drain their in-flight requests while new ones start receiving.
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
