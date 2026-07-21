// Package config loads and validates gobalance.yaml.
//
// Two rules this package follows:
//
//  1. Validate everything at startup. A load balancer that starts with a broken
//     config and fails on the first request is worse than one that refuses to
//     start.
//  2. Defaults live in one place (applyDefaults), not scattered around the
//     codebase as `if x == 0` checks.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration wraps time.Duration so the YAML file can say "5s".
type Duration time.Duration

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return fmt.Errorf("duration must be a string like \"5s\": %w", err)
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

// Std converts back to a plain time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// Config is the whole file.
type Config struct {
	Listen      string          `yaml:"listen"`
	AdminListen string          `yaml:"admin_listen"`
	Strategy    string          `yaml:"strategy"`
	Timeouts    Timeouts        `yaml:"timeouts"`
	Health      Health          `yaml:"health"`
	Passive     Passive         `yaml:"passive_health"`
	Breaker     Breaker         `yaml:"breaker"`
	Retry       Retry           `yaml:"retry"`
	Backends    []BackendConfig `yaml:"backends"`
}

// Timeouts groups every deadline in the system.
type Timeouts struct {
	Read             Duration `yaml:"read"`
	Write            Duration `yaml:"write"`
	Idle             Duration `yaml:"idle"`
	Dial             Duration `yaml:"dial"`
	ResponseHeader   Duration `yaml:"response_header"`
	GracefulShutdown Duration `yaml:"graceful_shutdown"`
}

// Health configures ACTIVE health checking: a background prober.
type Health struct {
	Enabled            bool     `yaml:"enabled"`
	Path               string   `yaml:"path"`
	Interval           Duration `yaml:"interval"`
	Timeout            Duration `yaml:"timeout"`
	HealthyThreshold   int      `yaml:"healthy_threshold"`
	UnhealthyThreshold int      `yaml:"unhealthy_threshold"`
}

// Passive configures PASSIVE health checking: ejection based on real traffic.
type Passive struct {
	Enabled             bool `yaml:"enabled"`
	ConsecutiveFailures int  `yaml:"consecutive_failures"`
}

// Breaker configures the per-backend circuit breaker.
type Breaker struct {
	Enabled          bool     `yaml:"enabled"`
	FailureThreshold int      `yaml:"failure_threshold"`
	SuccessThreshold int      `yaml:"success_threshold"`
	OpenTimeout      Duration `yaml:"open_timeout"`
	MaxProbes        int      `yaml:"max_probes"`
}

// Retry configures request retries and their budget.
type Retry struct {
	Enabled     bool    `yaml:"enabled"`
	MaxRetries  int     `yaml:"max_retries"`
	BudgetMax   int     `yaml:"budget_max_tokens"`
	BudgetRatio float64 `yaml:"budget_ratio"`
}

// BackendConfig is one entry in the backends list.
type BackendConfig struct {
	Name   string `yaml:"name"`
	URL    string `yaml:"url"`
	Weight int    `yaml:"weight"`
}

// Load reads, parses, defaults and validates a config file.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	return Parse(raw)
}

// Parse does everything Load does without touching the filesystem.
func Parse(raw []byte) (*Config, error) {
	var cfg Config

	dec := yaml.NewDecoder(newReader(raw))
	dec.KnownFields(true) // a typo in a key becomes an error, not silence

	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Listen == "" {
		c.Listen = ":8080"
	}
	if c.AdminListen == "" {
		c.AdminListen = ":8081"
	}
	if c.Strategy == "" {
		c.Strategy = "round_robin"
	}

	if c.Timeouts.Read == 0 {
		c.Timeouts.Read = Duration(15 * time.Second)
	}
	if c.Timeouts.Write == 0 {
		c.Timeouts.Write = Duration(15 * time.Second)
	}
	if c.Timeouts.Idle == 0 {
		c.Timeouts.Idle = Duration(60 * time.Second)
	}
	if c.Timeouts.Dial == 0 {
		c.Timeouts.Dial = Duration(2 * time.Second)
	}
	if c.Timeouts.ResponseHeader == 0 {
		c.Timeouts.ResponseHeader = Duration(10 * time.Second)
	}
	if c.Timeouts.GracefulShutdown == 0 {
		c.Timeouts.GracefulShutdown = Duration(20 * time.Second)
	}

	if c.Health.Path == "" {
		c.Health.Path = "/healthz"
	}
	if c.Health.Interval == 0 {
		c.Health.Interval = Duration(2 * time.Second)
	}
	if c.Health.Timeout == 0 {
		c.Health.Timeout = Duration(1 * time.Second)
	}
	if c.Health.HealthyThreshold <= 0 {
		c.Health.HealthyThreshold = 2
	}
	if c.Health.UnhealthyThreshold <= 0 {
		c.Health.UnhealthyThreshold = 3
	}

	if c.Passive.ConsecutiveFailures <= 0 {
		c.Passive.ConsecutiveFailures = 5
	}

	if c.Breaker.FailureThreshold <= 0 {
		c.Breaker.FailureThreshold = 5
	}
	if c.Breaker.SuccessThreshold <= 0 {
		c.Breaker.SuccessThreshold = 2
	}
	if c.Breaker.OpenTimeout == 0 {
		c.Breaker.OpenTimeout = Duration(5 * time.Second)
	}
	if c.Breaker.MaxProbes <= 0 {
		c.Breaker.MaxProbes = 1
	}

	if c.Retry.MaxRetries <= 0 {
		c.Retry.MaxRetries = 2
	}
	if c.Retry.BudgetMax <= 0 {
		c.Retry.BudgetMax = 100
	}
	if c.Retry.BudgetRatio <= 0 {
		c.Retry.BudgetRatio = 0.2
	}

	for i := range c.Backends {
		if c.Backends[i].Weight <= 0 {
			c.Backends[i].Weight = 1
		}
		if c.Backends[i].Name == "" {
			c.Backends[i].Name = fmt.Sprintf("backend-%d", i+1)
		}
	}
}

// Validate returns the first thing wrong with the config.
func (c *Config) Validate() error {
	if len(c.Backends) == 0 {
		return fmt.Errorf("config must list at least one backend")
	}

	seen := map[string]bool{}
	for _, b := range c.Backends {
		if b.URL == "" {
			return fmt.Errorf("backend %q has no url", b.Name)
		}
		if seen[b.Name] {
			return fmt.Errorf("duplicate backend name %q: names are used as metric labels and must be unique", b.Name)
		}
		seen[b.Name] = true
	}

	if c.Listen == c.AdminListen {
		return fmt.Errorf("listen and admin_listen must be different addresses (both are %q)", c.Listen)
	}

	// A health check that takes longer than the gap between checks means every
	// probe overlaps the next one -- the checker falls behind and its verdicts
	// describe the past rather than the present.
	if c.Health.Enabled && c.Health.Timeout >= c.Health.Interval {
		return fmt.Errorf("health.timeout (%v) must be shorter than health.interval (%v), or probes will overlap",
			c.Health.Timeout.Std(), c.Health.Interval.Std())
	}

	if c.Retry.BudgetRatio > 1 {
		return fmt.Errorf("retry.budget_ratio is %v; a value above 1 means retries may exceed successful traffic", c.Retry.BudgetRatio)
	}

	return nil
}
