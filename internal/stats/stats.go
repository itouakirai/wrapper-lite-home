// Package stats records request counts (total, per upstream, per endpoint,
// hourly per day, client IPs) and health-probe results, persisting them to a
// JSON file.
package stats

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type DayStats struct {
	Date      string           `json:"date"`
	Hourly    [24]int64        `json:"hourly"`
	Total     int64            `json:"total"`
	Upstreams map[string]int64 `json:"upstreams"`
	Endpoints map[string]int64 `json:"endpoints"`
	ClientIPs map[string]int64 `json:"client_ips,omitempty"`
}

type ProbeDay struct {
	Up   int64 `json:"up"`
	Down int64 `json:"down"`
}

type Data struct {
	Days           map[string]*DayStats            `json:"days"`
	Total          int64                           `json:"total"`
	UpstreamsTotal map[string]int64                `json:"upstreams_total"`
	ClientIPsTotal map[string]int64                `json:"client_ips_total,omitempty"`
	Probes         map[string]map[string]*ProbeDay `json:"probes"` // day -> upstream -> up/down
}

type Stats struct {
	mu           sync.Mutex
	data         Data
	file         string
	interval     time.Duration
	maxClientIPs int
	dirty        bool
	started      bool
	done         chan struct{}
	startTime    time.Time
}

func New(file string, interval time.Duration) *Stats {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &Stats{
		data: Data{
			Days:           map[string]*DayStats{},
			UpstreamsTotal: map[string]int64{},
			ClientIPsTotal: map[string]int64{},
			Probes:         map[string]map[string]*ProbeDay{},
		},
		file:         file,
		interval:     interval,
		maxClientIPs: 10000,
		done:         make(chan struct{}),
		startTime:    time.Now(),
	}
}

func (s *Stats) SetSaveInterval(d time.Duration) {
	if d <= 0 {
		d = 30 * time.Second
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.interval = d
}

func (s *Stats) SetMaxClientIPs(n int) {
	if n <= 0 {
		n = 10000
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maxClientIPs = n
}

func (s *Stats) Start() {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.mu.Unlock()
	go s.saveLoop()
}

func (s *Stats) Stop() {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return
	}
	s.started = false
	s.mu.Unlock()
	s.done <- struct{}{}
	s.Save()
}

func (s *Stats) saveLoop() {
	for {
		s.mu.Lock()
		interval := s.interval
		started := s.started
		s.mu.Unlock()
		if !started {
			return
		}
		if interval <= 0 {
			interval = 30 * time.Second
		}
		t := time.NewTimer(interval)
		select {
		case <-s.done:
			t.Stop()
			return
		case <-t.C:
			s.saveIfDirty()
		}
	}
}

func (s *Stats) Incr(upstreamName, endpoint string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	day := now.Format("2006-01-02")
	ds := s.dayLocked(day)
	ds.Total++
	ds.Hourly[now.Hour()]++
	if upstreamName != "" {
		ds.Upstreams[upstreamName]++
		s.data.UpstreamsTotal[upstreamName]++
	}
	if endpoint != "" {
		ds.Endpoints[endpoint]++
	}
	s.data.Total++
	s.dirty = true
}

// RecordClientIP counts a client request to the public proxy endpoints.
func (s *Stats) RecordClientIP(rawIP string) {
	ip := normalizeIP(rawIP)
	if ip == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.ClientIPsTotal[ip] == 0 && len(s.data.ClientIPsTotal) >= s.maxClientIPs {
		return
	}
	now := time.Now()
	day := now.Format("2006-01-02")
	ds := s.dayLocked(day)
	if ds.ClientIPs == nil {
		ds.ClientIPs = map[string]int64{}
	}
	ds.ClientIPs[ip]++
	s.data.ClientIPsTotal[ip]++
	s.dirty = true
}

func (s *Stats) dayLocked(day string) *DayStats {
	ds := s.data.Days[day]
	if ds == nil {
		ds = &DayStats{
			Date:      day,
			Upstreams: map[string]int64{},
			Endpoints: map[string]int64{},
			ClientIPs: map[string]int64{},
		}
		s.data.Days[day] = ds
	}
	if ds.Upstreams == nil {
		ds.Upstreams = map[string]int64{}
	}
	if ds.Endpoints == nil {
		ds.Endpoints = map[string]int64{}
	}
	if ds.ClientIPs == nil {
		ds.ClientIPs = map[string]int64{}
	}
	return ds
}

func (s *Stats) RecordProbe(upstreamName string, up bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	day := time.Now().Format("2006-01-02")
	byDay := s.data.Probes[day]
	if byDay == nil {
		byDay = map[string]*ProbeDay{}
		s.data.Probes[day] = byDay
	}
	p := byDay[upstreamName]
	if p == nil {
		p = &ProbeDay{}
		byDay[upstreamName] = p
	}
	if up {
		p.Up++
	} else {
		p.Down++
	}
	s.dirty = true
}

// Uptime returns the percentage of successful probes for an upstream over the
// last `days` days (0 means all time).
func (s *Stats) Uptime(name string, days int) float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var up, down int64
	cutoff := time.Now().AddDate(0, 0, -(days - 1))
	for dayStr, byDay := range s.data.Probes {
		if days > 0 {
			t, err := time.Parse("2006-01-02", dayStr)
			if err != nil || t.Before(cutoff) {
				continue
			}
		}
		if p := byDay[name]; p != nil {
			up += p.Up
			down += p.Down
		}
	}
	total := up + down
	if total == 0 {
		return 0
	}
	return float64(up) / float64(total) * 100
}

type TodayStats struct {
	Total     int64            `json:"total"`
	Hourly    [24]int64        `json:"hourly"`
	Upstreams map[string]int64 `json:"upstreams"`
	Endpoints map[string]int64 `json:"endpoints"`
	Hour      int              `json:"hour"`
}

type DaySummary struct {
	Date  string `json:"date"`
	Total int64  `json:"total"`
}

type IPRank struct {
	IP    string `json:"ip"`
	Count int64  `json:"count"`
}

type Snapshot struct {
	Total            int64            `json:"total"`
	Today            TodayStats       `json:"today"`
	UpstreamsTotal   map[string]int64 `json:"upstreams_total"`
	Days             []DaySummary     `json:"days"`
	ClientIPsToday   []IPRank         `json:"client_ips_today"`
	ClientIPsTotal   []IPRank         `json:"client_ips_total"`
	TrackedClientIPs int              `json:"tracked_client_ips"`
	StartTime        time.Time        `json:"start_time"`
	Now              time.Time        `json:"now"`
}

func (s *Stats) Snapshot(days int) Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	today := now.Format("2006-01-02")
	ts := TodayStats{
		Upstreams: map[string]int64{},
		Endpoints: map[string]int64{},
		Hour:      now.Hour(),
	}
	var todayIPs map[string]int64
	if ds := s.data.Days[today]; ds != nil {
		ts.Total = ds.Total
		ts.Hourly = ds.Hourly
		for k, v := range ds.Upstreams {
			ts.Upstreams[k] = v
		}
		for k, v := range ds.Endpoints {
			ts.Endpoints[k] = v
		}
		todayIPs = ds.ClientIPs
	}
	snap := Snapshot{
		Total:            s.data.Total,
		Today:            ts,
		UpstreamsTotal:   map[string]int64{},
		ClientIPsToday:   topIPs(todayIPs, 20),
		ClientIPsTotal:   topIPs(s.data.ClientIPsTotal, 20),
		TrackedClientIPs: len(s.data.ClientIPsTotal),
		StartTime:        s.startTime,
		Now:              now,
	}
	for k, v := range s.data.UpstreamsTotal {
		snap.UpstreamsTotal[k] = v
	}
	if days < 1 {
		days = 7
	}
	for i := days - 1; i >= 0; i-- {
		d := now.AddDate(0, 0, -i).Format("2006-01-02")
		sum := DaySummary{Date: d}
		if ds := s.data.Days[d]; ds != nil {
			sum.Total = ds.Total
		}
		snap.Days = append(snap.Days, sum)
	}
	return snap
}

func topIPs(counts map[string]int64, limit int) []IPRank {
	out := make([]IPRank, 0, len(counts))
	for ip, count := range counts {
		if count > 0 {
			out = append(out, IPRank{IP: ip, Count: count})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count == out[j].Count {
			return out[i].IP < out[j].IP
		}
		return out[i].Count > out[j].Count
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func normalizeIP(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(raw); err == nil {
		raw = host
	}
	raw = strings.Trim(raw, "[]")
	if idx := strings.IndexByte(raw, '%'); idx >= 0 {
		raw = raw[:idx]
	}
	return raw
}

func (s *Stats) saveIfDirty() {
	s.mu.Lock()
	dirty := s.dirty
	s.mu.Unlock()
	if !dirty {
		return
	}
	s.Save()
}

func (s *Stats) Save() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == "" {
		return
	}
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return
	}
	dir := filepath.Dir(s.file)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return
		}
	}
	tmp := s.file + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return
	}
	if err := os.Rename(tmp, s.file); err != nil {
		return
	}
	s.dirty = false
}

func (s *Stats) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == "" {
		return nil
	}
	b, err := os.ReadFile(s.file)
	if err != nil {
		return err
	}
	var data Data
	if err := json.Unmarshal(b, &data); err != nil {
		return err
	}
	if data.Days == nil {
		data.Days = map[string]*DayStats{}
	}
	if data.UpstreamsTotal == nil {
		data.UpstreamsTotal = map[string]int64{}
	}
	if data.ClientIPsTotal == nil {
		data.ClientIPsTotal = map[string]int64{}
	}
	if data.Probes == nil {
		data.Probes = map[string]map[string]*ProbeDay{}
	}
	s.data = data
	return nil
}
