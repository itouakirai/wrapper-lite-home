package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Listen != ":8080" {
		t.Fatalf("default listen = %q", cfg.Listen)
	}
	if cfg.Probe.Retries != 3 {
		t.Fatalf("default retries = %d", cfg.Probe.Retries)
	}
	if cfg.Region.CacheTTL.Duration() != 30*time.Minute {
		t.Fatalf("default cache ttl = %v", cfg.Region.CacheTTL)
	}
}

func TestLoadFromFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	os.WriteFile(p, []byte(`{
		"listen": ":9999",
		"auth": {"username": "u", "password": "p"},
		"probe": {"interval": "30s", "retries": 5, "backoff_interval": "5m"},
		"upstreams": [{"name": "A", "base_url": "http://localhost:1"}]
	}`), 0o644)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Listen != ":9999" {
		t.Fatalf("listen = %q", cfg.Listen)
	}
	if cfg.Probe.Interval.Duration() != 30*time.Second {
		t.Fatalf("interval = %v", cfg.Probe.Interval)
	}
	if cfg.Probe.Retries != 5 {
		t.Fatalf("retries = %d", cfg.Probe.Retries)
	}
	// omitted fields keep defaults
	if cfg.Region.CacheTTL.Duration() != 30*time.Minute {
		t.Fatalf("cache ttl = %v", cfg.Region.CacheTTL)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestValidateRejectsNoUpstreams(t *testing.T) {
	cfg := Default()
	cfg.Upstreams = nil
	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected error for missing upstreams")
	}
}
