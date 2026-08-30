package config

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Manager holds the live configuration, watches the config file for changes
// and applies them without restarting. It is also the source of truth for
// upstreams managed through the admin UI.
type Manager struct {
	mu       sync.RWMutex
	path     string
	cfg      *Config
	onChange func()
	lastHash [sha256.Size]byte
	hasHash  bool
}

func NewManager(path string) (*Manager, error) {
	cfg, err := Load(path)
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	m := &Manager{path: path, cfg: cfg}
	m.lastHash = m.fileHashLocked()
	m.hasHash = true
	return m, nil
}

// NewManagerFromConfig builds a manager from an existing config (used in tests
// and for in-memory setups where the file path is empty).
func NewManagerFromConfig(cfg *Config) *Manager {
	return &Manager{path: "", cfg: cfg.clone()}
}

func (m *Manager) SetOnChange(fn func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onChange = fn
}

func (m *Manager) Path() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.path
}

// Get returns a snapshot of the current config.
func (m *Manager) Get() *Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg.clone()
}

// Update applies fn to a copy of the current config, validates and persists
// the result, then swaps it in and fires the change callback.
func (m *Manager) Update(fn func(*Config)) error {
	return m.UpdateE(func(c *Config) error {
		if fn != nil {
			fn(c)
		}
		return nil
	})
}

// UpdateE is like Update but allows fn to abort the update with an error.
func (m *Manager) UpdateE(fn func(*Config) error) error {
	m.mu.Lock()
	next := m.cfg.clone()
	if fn != nil {
		if err := fn(next); err != nil {
			m.mu.Unlock()
			return err
		}
	}
	if err := next.Validate(); err != nil {
		m.mu.Unlock()
		return err
	}
	if err := m.saveLocked(next); err != nil {
		m.mu.Unlock()
		return err
	}
	m.cfg = next
	fnChange := m.onChange
	m.mu.Unlock()
	if fnChange != nil {
		go fnChange()
	}
	return nil
}

// Save persists the current config to disk.
func (m *Manager) Save() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saveLocked(m.cfg)
}

func (m *Manager) saveLocked(cfg *Config) error {
	if m.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(m.path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, m.path); err != nil {
		return err
	}
	m.lastHash = sha256.Sum256(b)
	m.hasHash = true
	return nil
}

// Reload re-reads the config file, validates it and swaps it in. Returns an
// error if the new config is invalid; the old config stays active.
func (m *Manager) Reload() error {
	m.mu.RLock()
	path := m.path
	m.mu.RUnlock()
	cfg, err := Load(path)
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("reloaded config invalid: %w", err)
	}
	m.mu.Lock()
	m.cfg = cfg
	m.lastHash = m.fileHashLocked()
	m.hasHash = true
	fn := m.onChange
	m.mu.Unlock()
	if fn != nil {
		fn()
	}
	return nil
}

// Watch polls the config file and applies changes. The poll interval is read
// from the live config on every cycle, so reload_interval itself can be
// changed without restarting. This is intended to run in its own goroutine.
func (m *Manager) Watch(ctx context.Context, fallbackInterval time.Duration) {
	m.mu.RLock()
	path := m.path
	m.mu.RUnlock()
	if path == "" {
		return
	}
	if fallbackInterval <= 0 {
		fallbackInterval = 2 * time.Second
	}
	for {
		interval := m.Get().ReloadInterval.Duration()
		if interval <= 0 {
			interval = fallbackInterval
		}
		t := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
			if _, err := os.Stat(path); err != nil {
				continue
			}
			m.mu.RLock()
			last := m.lastHash
			has := m.hasHash
			m.mu.RUnlock()
			h := m.fileHash()
			if has && h == last {
				continue
			}
			if err := m.Reload(); err != nil {
				log.Printf("config reload failed: %v", err)
				m.mu.Lock()
				m.lastHash = h
				m.hasHash = true
				m.mu.Unlock()
				continue
			}
			log.Printf("config file changed, reloaded")
		}
	}
}

func (m *Manager) fileHash() [sha256.Size]byte {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.fileHashLocked()
}

func (m *Manager) fileHashLocked() [sha256.Size]byte {
	if m.path == "" {
		return sha256.Sum256([]byte{})
	}
	b, err := os.ReadFile(m.path)
	if err != nil {
		return sha256.Sum256([]byte{})
	}
	return sha256.Sum256(b)
}

func (c *Config) clone() *Config {
	if c == nil {
		return nil
	}
	out := *c
	if c.Upstreams != nil {
		out.Upstreams = make([]Upstream, len(c.Upstreams))
		for i, u := range c.Upstreams {
			out.Upstreams[i] = u
			if u.Enabled != nil {
				enabled := *u.Enabled
				out.Upstreams[i].Enabled = &enabled
			}
		}
	}
	return &out
}
