// Package config loads and validates gobalance.yaml.
//
// Two rules this package follows, both worth defending in an interview:
//
//  1. Validate everything at startup. A load balancer that starts with a broken
//     config and fails on the first request is worse than one that refuses to
//     start.
//  2. Defaults live in one place (applyDefaults), not scattered across the
//     codebase as `if x == 0` checks.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration wraps time.Duration so the YAML file can say "5s" instead of
// 5000000000.
//
// GO NOTE: yaml.v3 cannot decode "5s" into a time.Duration on its own. Defining
// a named type with an UnmarshalYAML method teaches it how. This is the same
// pattern as implementing json.Unmarshaler.
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

// Std converts back to a plain time.Duration for use with the standard library.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// Config is the whole file.
type Config struct {
	Listen      string          `yaml:"listen"`
	AdminListen string          `yaml:"admin_listen"`
	Strategy    string          `yaml:"strategy"`
	Timeouts    Timeouts        `yaml:"timeouts"`
	Backends    []BackendConfig `yaml:"backends"`
}

// Timeouts groups every deadline in the system.
//
// A load balancer with no timeouts is a denial-of-service amplifier: one slow
// upstream ties up connections until the process runs out of file descriptors.
type Timeouts struct {
	Read             Duration `yaml:"read"`              // client -> LB: time to read the request
	Write            Duration `yaml:"write"`             // LB -> client: time to write the response
	Idle             Duration `yaml:"idle"`              // keep-alive idle timeout
	Dial             Duration `yaml:"dial"`              // LB -> backend: TCP connect
	ResponseHeader   Duration `yaml:"response_header"`   // LB -> backend: time to first byte
	GracefulShutdown Duration `yaml:"graceful_shutdown"` // drain window on SIGTERM
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

// Parse does everything Load does except touching the filesystem, which makes
// it trivial to unit test.
func Parse(raw []byte) (*Config, error) {
	var cfg Config

	dec := yaml.NewDecoder(newReader(raw))
	dec.KnownFields(true) // typo in a key name becomes an error, not silence

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
	return nil
}
