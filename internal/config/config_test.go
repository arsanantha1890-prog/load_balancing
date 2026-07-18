package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/arsanantha1890-prog/load_balancing/internal/config"
)

const good = `
listen: ":8080"
strategy: round_robin
backends:
  - name: alpha
    url: http://127.0.0.1:9001
    weight: 2
  - name: beta
    url: http://127.0.0.1:9002
`

func TestParseGoodConfig(t *testing.T) {
	cfg, err := config.Parse([]byte(good))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if len(cfg.Backends) != 2 {
		t.Fatalf("expected 2 backends, got %d", len(cfg.Backends))
	}
	if cfg.Backends[1].Weight != 1 {
		t.Errorf("expected default weight 1 for beta, got %d", cfg.Backends[1].Weight)
	}
	if cfg.AdminListen != ":8081" {
		t.Errorf("expected default admin_listen :8081, got %q", cfg.AdminListen)
	}
	if cfg.Timeouts.Dial.Std() != 2*time.Second {
		t.Errorf("expected default dial timeout 2s, got %v", cfg.Timeouts.Dial.Std())
	}
}

func TestDurationStrings(t *testing.T) {
	cfg, err := config.Parse([]byte(good + "\ntimeouts:\n  dial: 750ms\n  read: 30s\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Timeouts.Dial.Std() != 750*time.Millisecond {
		t.Errorf("dial = %v, want 750ms", cfg.Timeouts.Dial.Std())
	}
	if cfg.Timeouts.Read.Std() != 30*time.Second {
		t.Errorf("read = %v, want 30s", cfg.Timeouts.Read.Std())
	}
}

func TestRejectsEmptyBackendList(t *testing.T) {
	_, err := config.Parse([]byte(`listen: ":8080"`))
	if err == nil {
		t.Fatal("expected an error for a config with no backends")
	}
}

func TestRejectsDuplicateNames(t *testing.T) {
	dup := `
backends:
  - name: same
    url: http://127.0.0.1:9001
  - name: same
    url: http://127.0.0.1:9002
`
	_, err := config.Parse([]byte(dup))
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected a duplicate-name error, got %v", err)
	}
}

func TestRejectsUnknownFields(t *testing.T) {
	_, err := config.Parse([]byte("stratergy: round_robin\nbackends:\n  - url: http://x:1\n"))
	if err == nil {
		t.Fatal("expected an error for the misspelled key 'stratergy'")
	}
}
