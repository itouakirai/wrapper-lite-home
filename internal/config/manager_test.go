package config

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestManagerWatchReloadsFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	cfg := Default()
	cfg.ReloadInterval = Duration(20 * time.Millisecond)
	cfg.Upstreams = []Upstream{{Name: "one", BaseURL: "http://127.0.0.1:9001"}}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	m, err := NewManager(path)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	changed := make(chan struct{}, 1)
	m.SetOnChange(func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Watch(ctx, 20*time.Millisecond)

	cfg.Upstreams = []Upstream{
		{Name: "one", BaseURL: "http://127.0.0.1:9001"},
		{Name: "two", BaseURL: "http://127.0.0.1:9002"},
	}
	b, err = json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("marshal update: %v", err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatalf("config change not observed")
	}
	if got := len(m.Get().Upstreams); got != 2 {
		t.Fatalf("upstreams = %d, want 2", got)
	}
}

func TestManagerUpdateRejectsInvalidConfig(t *testing.T) {
	m := NewManagerFromConfig(Default())
	before := len(m.Get().Upstreams)
	err := m.Update(func(c *Config) {
		c.Upstreams = nil
	})
	if err == nil {
		t.Fatalf("expected validation error")
	}
	if got := len(m.Get().Upstreams); got != before {
		t.Fatalf("upstreams changed after invalid update: %d", got)
	}
}
