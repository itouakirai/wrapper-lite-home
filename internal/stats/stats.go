// Package stats records request counts (total, per upstream, per endpoint,
// hourly per day) and health-probe results, persisting them to a JSON file.
package stats

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type DayStats struct {
	Date      string           `json:"date"`
	Hourly    [24]int64        `json:"hourly"`
	Total     int64            `json:"total"`
	Upstreams map[string]int64 `json:"upstreams"`
	Endpoints map[string]int64 `json:"endpoints"`
}

type ProbeDay struct {
	Up   int64 `json:"up"`
	Down int64 `json:"down"`
}

type Data struct {
	Days           map[string]*DayStats            `json:"days"`
	Total          int64                           `json:"total"`
	UpstreamsTotal map[string]int64                `json:"upstreams_total"`
	Probes         map[string]map[string]*ProbeDay `json:"probes"` // day -> upstream -> up/down
}

type Stats struct {
	mu        sync.Mutex
	data      Data
	file      string
	interval  time.Duration
	dirty     bool
	started   bool
	done      chan struct{}
	startTime time.Time
}

func New(file string, interval time.Duration) *Stats {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &Stats{
		data: Data{
			Days:           map[string]*DayStats{},
			UpstreamsTotal: map[string]int64{},
			Probes:         map[string]map[string]*ProbeDay{},
		},
		file:      file,
		interval:  interval,
		done:      make(chan struct{}),
		startTime: time.Now(),
	}
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
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-s.done:
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
	ds := s.data.Days[day]
	if ds == nil {
		ds = &DayStats{Date: day, Upstreams: map[string]int64{}, Endpoints: map[string]int64{}}
		s.data.Days[day] = ds
	}
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

type Snapshot struct {
	Total          int64            `json:"total"`
	Today          TodayStats       `json:"today"`
	UpstreamsTotal map[string]int64 `json:"upstreams_total"`
	Days           []DaySummary     `json:"days"`
	StartTime      time.Time        `json:"start_time"`
	Now            time.Time        `json:"now"`
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
	if ds := s.data.Days[today]; ds != nil {
		ts.Total = ds.Total
		ts.Hourly = ds.Hourly
		for k, v := range ds.Upstreams {
			ts.Upstreams[k] = v
		}
		for k, v := range ds.Endpoints {
			ts.Endpoints[k] = v
		}
	}
	snap := Snapshot{
		Total:          s.data.Total,
		Today:          ts,
		UpstreamsTotal: map[string]int64{},
		StartTime:      s.startTime,
		Now:            now,
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
	if data.Probes == nil {
		data.Probes = map[string]map[string]*ProbeDay{}
	}
	s.data = data
	return nil
}
