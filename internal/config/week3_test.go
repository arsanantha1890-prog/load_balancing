package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/arsanantha1890-prog/load_balancing/internal/config"
)

const week3Config = `
listen: ":8080"
strategy: p2c

health:
  enabled: true
  path: /healthz
  interval: 2s
  timeout: 1s
  healthy_threshold: 2
  unhealthy_threshold: 3

passive_health:
  enabled: true
  consecutive_failures: 5

breaker:
  enabled: true
  failure_threshold: 5
  success_threshold: 2
  open_timeout: 5s
  max_probes: 1

retry:
  enabled: true
  max_retries: 2
  budget_max_tokens: 100
  budget_ratio: 0.2

backends:
  - name: alpha
    url: http://127.0.0.1:9001
`

func TestParsesWeek3Sections(t *testing.T) {
	cfg, err := config.Parse([]byte(week3Config))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if !cfg.Health.Enabled {
		t.Error("health.enabled did not parse")
	}
	if cfg.Health.Interval.Std() != 2*time.Second {
		t.Errorf("health.interval = %v, want 2s", cfg.Health.Interval.Std())
	}
	if cfg.Breaker.FailureThreshold != 5 {
		t.Errorf("breaker.failure_threshold = %d, want 5", cfg.Breaker.FailureThreshold)
	}
	if cfg.Retry.BudgetRatio != 0.2 {
		t.Errorf("retry.budget_ratio = %v, want 0.2", cfg.Retry.BudgetRatio)
	}
	if cfg.Passive.ConsecutiveFailures != 5 {
		t.Errorf("passive_health.consecutive_failures = %d, want 5", cfg.Passive.ConsecutiveFailures)
	}
}

func TestWeek3Defaults(t *testing.T) {
	cfg, err := config.Parse([]byte("backends:\n  - url: http://127.0.0.1:9001\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if cfg.Health.Path != "/healthz" {
		t.Errorf("health.path = %q, want /healthz", cfg.Health.Path)
	}
	if cfg.Breaker.MaxProbes != 1 {
		t.Errorf("breaker.max_probes = %d, want 1", cfg.Breaker.MaxProbes)
	}
	if cfg.Retry.BudgetMax != 100 {
		t.Errorf("retry.budget_max_tokens = %d, want 100", cfg.Retry.BudgetMax)
	}
}

// A probe timeout longer than the interval means every check overlaps the next.
func TestRejectsOverlappingHealthProbes(t *testing.T) {
	bad := `
health:
  enabled: true
  interval: 1s
  timeout: 2s
backends:
  - url: http://127.0.0.1:9001
`
	_, err := config.Parse([]byte(bad))
	if err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("expected an overlap error, got %v", err)
	}
}

func TestRejectsAbsurdRetryBudget(t *testing.T) {
	bad := `
retry:
  enabled: true
  budget_ratio: 5
backends:
  - url: http://127.0.0.1:9001
`
	_, err := config.Parse([]byte(bad))
	if err == nil {
		t.Fatal("expected an error for a budget ratio above 1")
	}
}
