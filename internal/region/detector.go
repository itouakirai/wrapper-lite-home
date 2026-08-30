// Package region resolves which storefront regions an adamId is available in
// by querying the iTunes lookup API, with an in-memory TTL cache and
// per-adamId singleflight to avoid duplicate lookups.
package region

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

type Options struct {
	CacheTTL      time.Duration
	NotFoundTTL   time.Duration
	Concurrency   int
	LookupTimeout time.Duration
	LookupBase    string
}

type Detector struct {
	opts   Options
	client *http.Client

	mu        sync.Mutex
	cache     map[string]map[string]regionResult // adamID -> region -> result
	flights   map[string]*flight
	lastClean time.Time
}

type regionResult struct {
	available bool
	at        time.Time
}

type flight struct {
	done      chan struct{}
	available []string
	err       error
}

func NewDetector(opts Options) *Detector {
	if opts.Concurrency < 1 {
		opts.Concurrency = 4
	}
	if opts.LookupTimeout <= 0 {
		opts.LookupTimeout = 5 * time.Second
	}
	if opts.LookupBase == "" {
		opts.LookupBase = "https://itunes.apple.com/lookup"
	}
	return &Detector{
		opts:    opts,
		client:  &http.Client{Timeout: opts.LookupTimeout},
		cache:   make(map[string]map[string]regionResult),
		flights: make(map[string]*flight),
	}
}

// Detect returns the subset of regions where the adamId exists
// (iTunes resultCount == 1). Results are cached to avoid repeated lookups.
func (d *Detector) Detect(ctx context.Context, adamID string, regions []string) ([]string, error) {
	regions = normalizeUnique(regions)
	if len(regions) == 0 {
		return nil, nil
	}

	available, missing := d.fromCache(adamID, regions)
	if len(missing) == 0 {
		return available, nil
	}

	// Become the leader for this adamId, or wait for the in-flight lookup.
	d.mu.Lock()
	f, ok := d.flights[adamID]
	if !ok {
		f = &flight{done: make(chan struct{})}
		d.flights[adamID] = f
	}
	d.mu.Unlock()

	if !ok {
		// Leader: perform the lookups and broadcast the result.
		defer func() {
			d.mu.Lock()
			delete(d.flights, adamID)
			d.mu.Unlock()
			close(f.done)
		}()

		available, missing = d.fromCache(adamID, regions)
		if len(missing) == 0 {
			f.available = available
			return available, nil
		}

		// Use a detached context for lookups so one cancelled request does
		// not fail the detection for other in-flight requests of the same adamId.
		available, lookupFailures := d.resolve(context.WithoutCancel(ctx), adamID, regions, missing)
		f.available = available
		if len(available) == 0 && lookupFailures > 0 {
			f.err = fmt.Errorf("itunes lookup failed for %d region(s)", lookupFailures)
		}
		return f.available, f.err
	}

	// Follower: reuse the leader's result.
	select {
	case <-f.done:
		return f.available, f.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// resolve looks up the missing regions in parallel and returns the sorted
// list of regions where the adamId is available, plus how many lookups failed.
func (d *Detector) resolve(ctx context.Context, adamID string, regions, missing []string) ([]string, int) {
	sem := make(chan struct{}, d.opts.Concurrency)
	var wg sync.WaitGroup
	var results sync.Map // region -> bool
	var failures int
	var failMu sync.Mutex

	for _, region := range missing {
		wg.Add(1)
		go func(reg string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()

			ok, err := d.lookup(ctx, adamID, reg)
			if err != nil {
				failMu.Lock()
				failures++
				failMu.Unlock()
				return
			}
			d.setCache(adamID, reg, ok)
			results.Store(reg, ok)
		}(region)
	}
	wg.Wait()

	var available []string
	for _, reg := range regions {
		if v, ok := results.Load(reg); ok && v.(bool) {
			available = append(available, reg)
		}
	}
	sort.Strings(available)
	return available, failures
}

func (d *Detector) lookup(ctx context.Context, adamID, region string) (bool, error) {
	u := fmt.Sprintf("%s?id=%s&country=%s",
		strings.TrimRight(d.opts.LookupBase, "/"),
		url.QueryEscape(adamID), url.QueryEscape(region))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("User-Agent", "wrapper-lite/1.0")
	req.Header.Set("Accept", "application/json")
	resp, err := d.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("itunes lookup %s: status %d", region, resp.StatusCode)
	}
	var out struct {
		ResultCount int `json:"resultCount"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return false, err
	}
	return out.ResultCount > 0, nil
}

func (d *Detector) fromCache(adamID string, regions []string) (available, missing []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	d.cleanupLocked(now)

	byRegion := d.cache[adamID]
	for _, reg := range regions {
		res, ok := byRegion[reg]
		if !ok {
			missing = append(missing, reg)
			continue
		}
		ttl := d.opts.CacheTTL
		if !res.available {
			ttl = d.opts.NotFoundTTL
		}
		if now.Sub(res.at) >= ttl {
			missing = append(missing, reg)
			delete(byRegion, reg)
			continue
		}
		if res.available {
			available = append(available, reg)
		}
	}
	sort.Strings(available)
	return available, missing
}

func (d *Detector) setCache(adamID, region string, available bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	byRegion := d.cache[adamID]
	if byRegion == nil {
		byRegion = make(map[string]regionResult)
		d.cache[adamID] = byRegion
	}
	byRegion[region] = regionResult{available: available, at: time.Now()}
}

func (d *Detector) cleanupLocked(now time.Time) {
	if now.Sub(d.lastClean) < time.Minute {
		return
	}
	d.lastClean = now
	for id, byRegion := range d.cache {
		for reg, res := range byRegion {
			ttl := d.opts.CacheTTL
			if !res.available {
				ttl = d.opts.NotFoundTTL
			}
			if now.Sub(res.at) >= ttl {
				delete(byRegion, reg)
			}
		}
		if len(byRegion) == 0 {
			delete(d.cache, id)
		}
	}
}

func normalizeUnique(regions []string) []string {
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
