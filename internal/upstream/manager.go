// Package upstream manages the aggregated upstream APIs: it probes their
// /status endpoint to learn supported regions and track online/offline state,
// with retry and backoff so unresponsive APIs are checked less frequently.
package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"wrapper-lite/internal/config"
	"wrapper-lite/internal/stats"
)

type Upstream struct {
	mu                  sync.Mutex
	Name                string
	BaseURL             string
	Enabled             bool
	Online              bool
	Regions             []string
	LatencyMs           int64
	LastCheck           time.Time
	ConsecutiveFailures int
	Backoff             bool
	LastError           string
}

type Snapshot struct {
	Name                string    `json:"name"`
	BaseURL             string    `json:"base_url"`
	Enabled             bool      `json:"enabled"`
	Online              bool      `json:"online"`
	Regions             []string  `json:"regions"`
	LatencyMs           int64     `json:"latency_ms"`
	LastCheck           time.Time `json:"last_check"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
	Backoff             bool      `json:"backoff"`
	LastError           string    `json:"last_error,omitempty"`
	Uptime7d            float64   `json:"uptime_7d"`
	Uptime30d           float64   `json:"uptime_30d"`
	UptimeAll           float64   `json:"uptime_all"`
}

type Manager struct {
	mu        sync.RWMutex
	upstreams []*Upstream
	client    *http.Client
	cfg       config.ProbeConfig
	stats     *stats.Stats
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

func NewManager(cfg config.Config, st *stats.Stats) *Manager {
	timeout := cfg.Probe.Timeout.Duration()
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	m := &Manager{
		client: &http.Client{Timeout: timeout},
		cfg:    cfg.Probe,
		stats:  st,
	}
	for _, u := range cfg.Upstreams {
		m.upstreams = append(m.upstreams, &Upstream{
			Name:    u.Name,
			BaseURL: strings.TrimRight(u.BaseURL, "/"),
			Enabled: u.IsEnabled(),
		})
	}
	return m
}

func (m *Manager) Start(ctx context.Context) {
	ctx, m.cancel = context.WithCancel(ctx)
	m.wg.Add(1)
	go m.loop(ctx)
	// Do an initial probe shortly after startup so the dashboard is not empty.
	go func() {
		select {
		case <-time.After(300 * time.Millisecond):
			m.ProbeAll()
		case <-ctx.Done():
		}
	}()
}

func (m *Manager) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
	m.wg.Wait()
}

func (m *Manager) loop(ctx context.Context) {
	defer m.wg.Done()
	interval := m.cfg.Interval.Duration()
	if interval <= 0 {
		interval = time.Minute
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.ProbeAll()
		}
	}
}

func (m *Manager) ProbeAll() {
	m.mu.RLock()
	ups := make([]*Upstream, len(m.upstreams))
	copy(ups, m.upstreams)
	m.mu.RUnlock()

	for _, u := range ups {
		if !u.Enabled {
			continue
		}
		u.mu.Lock()
		skip := u.Backoff && time.Since(u.LastCheck) < m.cfg.BackoffInterval.Duration()
		u.mu.Unlock()
		if skip {
			continue
		}
		m.probe(u)
	}
}

// probe checks one upstream, retrying up to `retries` additional times after
// the first failure. If all attempts fail (or regions are empty) the upstream
// is marked offline and switched to backoff mode (probed every
// backoff_interval instead of every interval).
func (m *Manager) probe(u *Upstream) {
	retries := m.cfg.Retries
	if retries < 0 {
		retries = 0
	}
	retryDelay := m.cfg.RetryDelay.Duration()
	if retryDelay <= 0 {
		retryDelay = time.Second
	}
	backoffInterval := m.cfg.BackoffInterval.Duration()
	if backoffInterval <= 0 {
		backoffInterval = 10 * time.Minute
	}

	var regions []string
	var latency time.Duration
	var err error
	for attempt := 0; attempt <= retries; attempt++ {
		regions, latency, err = m.fetchStatus(u.BaseURL)
		if err == nil && len(regions) == 0 {
			err = fmt.Errorf("status returned empty regions")
		}
		if err == nil {
			break
		}
		if attempt < retries {
			time.Sleep(retryDelay)
		}
	}

	now := time.Now()
	u.mu.Lock()
	u.LastCheck = now
	if err == nil {
		u.Online = true
		u.Regions = normalizeRegions(regions)
		u.LatencyMs = latency.Milliseconds()
		u.ConsecutiveFailures = 0
		u.Backoff = false
		u.LastError = ""
	} else {
		u.ConsecutiveFailures++
		u.Online = false
		u.Backoff = true
		u.LastError = err.Error()
	}
	u.mu.Unlock()

	if m.stats != nil {
		m.stats.RecordProbe(u.Name, err == nil)
	}
}

func (m *Manager) fetchStatus(baseURL string) ([]string, time.Duration, error) {
	start := time.Now()
	req, err := http.NewRequest(http.MethodGet, baseURL+"/status", nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", "wrapper-lite/1.0")
	req.Header.Set("Accept", "application/json")
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	latency := time.Since(start)
	if resp.StatusCode != http.StatusOK {
		return nil, latency, fmt.Errorf("status code %d", resp.StatusCode)
	}
	var out struct {
		Data struct {
			Regions []string `json:"regions"`
		} `json:"data"`
		Regions []string `json:"regions"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, latency, err
	}
	regions := out.Data.Regions
	if len(regions) == 0 {
		regions = out.Regions
	}
	return regions, latency, nil
}

// Regions returns the merged, deduplicated, sorted list of storefront codes
// served by all currently online upstreams.
func (m *Manager) Regions() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	seen := make(map[string]bool)
	var out []string
	for _, u := range m.upstreams {
		if !u.Enabled {
			continue
		}
		u.mu.Lock()
		if u.Online {
			for _, r := range u.Regions {
				if !seen[r] {
					seen[r] = true
					out = append(out, r)
				}
			}
		}
		u.mu.Unlock()
	}
	sort.Strings(out)
	return out
}

// OnlineSupporting returns the online upstreams that serve the given region.
func (m *Manager) OnlineSupporting(region string) []*Upstream {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []*Upstream
	for _, u := range m.upstreams {
		if !u.Enabled {
			continue
		}
		u.mu.Lock()
		online := u.Online
		supports := false
		if online {
			for _, r := range u.Regions {
				if r == region {
					supports = true
					break
				}
			}
		}
		u.mu.Unlock()
		if supports {
			out = append(out, u)
		}
	}
	return out
}

func (m *Manager) Snapshot() []Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Snapshot, 0, len(m.upstreams))
	for _, u := range m.upstreams {
		u.mu.Lock()
		s := Snapshot{
			Name:                u.Name,
			BaseURL:             u.BaseURL,
			Enabled:             u.Enabled,
			Online:              u.Online,
			Regions:             append([]string(nil), u.Regions...),
			LatencyMs:           u.LatencyMs,
			LastCheck:           u.LastCheck,
			ConsecutiveFailures: u.ConsecutiveFailures,
			Backoff:             u.Backoff,
			LastError:           u.LastError,
		}
		u.mu.Unlock()
		if m.stats != nil {
			s.Uptime7d = m.stats.Uptime(u.Name, 7)
			s.Uptime30d = m.stats.Uptime(u.Name, 30)
			s.UptimeAll = m.stats.Uptime(u.Name, 0)
		}
		out = append(out, s)
	}
	return out
}

func normalizeRegions(regions []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, r := range regions {
		r = strings.ToLower(strings.TrimSpace(r))
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}
