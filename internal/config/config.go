package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

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

func (d Duration) Std() time.Duration { return time.Duration(d) }

type Config struct {
	Listen      string          `yaml:"listen"`
	AdminListen string          `yaml:"admin_listen"`
	Strategy    string          `yaml:"strategy"`
	Timeouts    Timeouts        `yaml:"timeouts"`
	Health      Health          `yaml:"health"`
	Passive     Passive         `yaml:"passive_health"`
	Breaker     Breaker         `yaml:"breaker"`
	Retry       Retry           `yaml:"retry"`
	Discovery   Discovery       `yaml:"discovery"`
	Backends    []BackendConfig `yaml:"backends"`
}

type Timeouts struct {
	Read             Duration `yaml:"read"`
	Write            Duration `yaml:"write"`
	Idle             Duration `yaml:"idle"`
	Dial             Duration `yaml:"dial"`
	ResponseHeader   Duration `yaml:"response_header"`
	GracefulShutdown Duration `yaml:"graceful_shutdown"`
}

type Health struct {
	Enabled            bool     `yaml:"enabled"`
	Path               string   `yaml:"path"`
	Interval           Duration `yaml:"interval"`
	Timeout            Duration `yaml:"timeout"`
	HealthyThreshold   int      `yaml:"healthy_threshold"`
	UnhealthyThreshold int      `yaml:"unhealthy_threshold"`
}

type Passive struct {
	Enabled             bool `yaml:"enabled"`
	ConsecutiveFailures int  `yaml:"consecutive_failures"`
}

type Breaker struct {
	Enabled          bool     `yaml:"enabled"`
	FailureThreshold int      `yaml:"failure_threshold"`
	SuccessThreshold int      `yaml:"success_threshold"`
	OpenTimeout      Duration `yaml:"open_timeout"`
	MaxProbes        int      `yaml:"max_probes"`
}

type Retry struct {
	Enabled     bool    `yaml:"enabled"`
	MaxRetries  int     `yaml:"max_retries"`
	BudgetMax   int     `yaml:"budget_max_tokens"`
	BudgetRatio float64 `yaml:"budget_ratio"`
}

// Discovery configures WHERE the backend list comes from.
//
//   - mode: "static" (default) — backends from the backends[] list in this file.
//   - mode: "kubernetes" — backends from EndpointSlice objects for a Service.
//
// In kubernetes mode, the backends[] list is ignored; the pool is entirely
// driven by the informer. In static mode, the discovery section is ignored.
type Discovery struct {
	Mode        string `yaml:"mode"`         // "static" or "kubernetes"
	ServiceName string `yaml:"service_name"` // k8s Service name to watch
	Namespace   string `yaml:"namespace"`    // k8s namespace
	PortName    string `yaml:"port_name"`    // named port to select (optional)
	Kubeconfig  string `yaml:"kubeconfig"`   // path to kubeconfig (optional, uses in-cluster if empty)
}

type BackendConfig struct {
	Name   string `yaml:"name"`
	URL    string `yaml:"url"`
	Weight int    `yaml:"weight"`
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	return Parse(raw)
}

func Parse(raw []byte) (*Config, error) {
	var cfg Config

	dec := yaml.NewDecoder(newReader(raw))
	dec.KnownFields(true)

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

	if c.Discovery.Mode == "" {
		c.Discovery.Mode = "static"
	}
	if c.Discovery.Namespace == "" {
		c.Discovery.Namespace = "default"
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

func (c *Config) Validate() error {
	if c.Discovery.Mode == "static" && len(c.Backends) == 0 {
		return fmt.Errorf("in static discovery mode, at least one backend must be listed")
	}

	if c.Discovery.Mode == "kubernetes" && c.Discovery.ServiceName == "" {
		return fmt.Errorf("in kubernetes discovery mode, discovery.service_name must be set")
	}

	if c.Discovery.Mode != "static" && c.Discovery.Mode != "kubernetes" {
		return fmt.Errorf("discovery.mode must be \"static\" or \"kubernetes\", got %q", c.Discovery.Mode)
	}

	seen := map[string]bool{}
	for _, b := range c.Backends {
		if b.URL == "" {
			return fmt.Errorf("backend %q has no url", b.Name)
		}
		if seen[b.Name] {
			return fmt.Errorf("duplicate backend name %q", b.Name)
		}
		seen[b.Name] = true
	}

	if c.Listen == c.AdminListen {
		return fmt.Errorf("listen and admin_listen must be different addresses (both are %q)", c.Listen)
	}

	if c.Health.Enabled && c.Health.Timeout >= c.Health.Interval {
		return fmt.Errorf("health.timeout (%v) must be shorter than health.interval (%v), or probes will overlap",
			c.Health.Timeout.Std(), c.Health.Interval.Std())
	}

	if c.Retry.BudgetRatio > 1 {
		return fmt.Errorf("retry.budget_ratio is %v; a value above 1 means retries may exceed successful traffic", c.Retry.BudgetRatio)
	}

	return nil
}
