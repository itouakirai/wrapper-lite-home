package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// Duration is a JSON-friendly duration that accepts either a Go duration
// string (e.g. "30s", "5m", "1h") or a plain number of seconds.
type Duration time.Duration

func (d *Duration) UnmarshalJSON(b []byte) error {
	var raw json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		dur, err := time.ParseDuration(s)
		if err != nil {
			return err
		}
		*d = Duration(dur)
		return nil
	}
	var n float64
	if err := json.Unmarshal(raw, &n); err == nil {
		*d = Duration(time.Duration(n * float64(time.Second)))
		return nil
	}
	return fmt.Errorf("invalid duration value %s", string(b))
}

func (d Duration) Duration() time.Duration { return time.Duration(d) }

type Config struct {
	Listen          string       `json:"listen"`
	Auth            AuthConfig   `json:"auth"`
	SessionTTL      Duration     `json:"session_ttl"`
	Region          RegionConfig `json:"region"`
	Probe           ProbeConfig  `json:"probe"`
	UpstreamTimeout Duration     `json:"upstream_timeout"`
	Upstreams       []Upstream   `json:"upstreams"`
	StatsFile       string       `json:"stats_file"`
	StatsInterval   Duration     `json:"stats_save_interval"`
}

type AuthConfig struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type RegionConfig struct {
	CacheTTL      Duration `json:"cache_ttl"`
	NotFoundTTL   Duration `json:"not_found_ttl"`
	Concurrency   int      `json:"concurrency"`
	LookupTimeout Duration `json:"lookup_timeout"`
	LookupBase    string   `json:"itunes_lookup_base"`
}

type ProbeConfig struct {
	Interval        Duration `json:"interval"`
	Retries         int      `json:"retries"`
	RetryDelay      Duration `json:"retry_delay"`
	BackoffInterval Duration `json:"backoff_interval"`
	Timeout         Duration `json:"timeout"`
}

type Upstream struct {
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	Enabled *bool  `json:"enabled,omitempty"`
}

func (u Upstream) IsEnabled() bool {
	return u.Enabled == nil || *u.Enabled
}

func Default() *Config {
	return &Config{
		Listen: ":8080",
		Auth: AuthConfig{
			Username: "admin",
			Password: "admin",
		},
		SessionTTL: Duration(24 * time.Hour),
		Region: RegionConfig{
			CacheTTL:      Duration(30 * time.Minute),
			NotFoundTTL:   Duration(10 * time.Minute),
			Concurrency:   4,
			LookupTimeout: Duration(5 * time.Second),
			LookupBase:    "https://itunes.apple.com/lookup",
		},
		Probe: ProbeConfig{
			Interval:        Duration(time.Minute),
			Retries:         3,
			RetryDelay:      Duration(time.Second),
			BackoffInterval: Duration(10 * time.Minute),
			Timeout:         Duration(5 * time.Second),
		},
		UpstreamTimeout: Duration(30 * time.Second),
		StatsFile:       "data/stats.json",
		StatsInterval:   Duration(30 * time.Second),
	}
}

func Load(path string) (*Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

func (c *Config) Validate() error {
	if c.Listen == "" {
		return fmt.Errorf("listen is required")
	}
	if c.Auth.Username == "" {
		return fmt.Errorf("auth.username is required")
	}
	if len(c.Upstreams) == 0 {
		return fmt.Errorf("at least one upstream is required")
	}
	for i, u := range c.Upstreams {
		if u.Name == "" {
			return fmt.Errorf("upstream %d: name is required", i)
		}
		if u.BaseURL == "" {
			return fmt.Errorf("upstream %d: base_url is required", i)
		}
		if !strings.HasPrefix(u.BaseURL, "http://") && !strings.HasPrefix(u.BaseURL, "https://") {
			return fmt.Errorf("upstream %d: base_url must start with http:// or https://", i)
		}
	}
	if c.Region.Concurrency < 1 {
		return fmt.Errorf("region.concurrency must be >= 1")
	}
	return nil
}

func WriteExample(path string) error {
	b, err := json.MarshalIndent(Default(), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
