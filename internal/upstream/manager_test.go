package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"wrapper-lite/internal/config"
	"wrapper-lite/internal/stats"
)

func newTestServer(regions []string, failUntil int32) *httptest.Server {
	var reqs int32
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&reqs, 1)
		if n < failUntil {
			http.Error(w, "fail", http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"code": 0, "msg": "SUCCESS",
			"data": map[string]any{"regions": regions},
		})
	}))
}

func TestProbeSuccess(t *testing.T) {
	srv := newTestServer([]string{"us", "cn"}, 0)
	defer srv.Close()

	cfg := config.Default()
	cfg.Upstreams = []config.Upstream{
		{Name: "test", BaseURL: srv.URL, Enabled: boolPtr(true)},
	}
	st := stats.New("", 0)
	m := NewManager(*cfg, st)
	m.probe(m.upstreams[0])
	if !m.upstreams[0].Online {
		t.Fatalf("expected online")
	}
	if len(m.upstreams[0].Regions) != 2 {
		t.Fatalf("regions = %v", m.upstreams[0].Regions)
	}
}

func TestProbeRetriesThenBackoff(t *testing.T) {
	// Server that always fails.
	srv := newTestServer([]string{"us"}, 999)
	defer srv.Close()

	cfg := config.Default()
	cfg.Upstreams = []config.Upstream{
		{Name: "fail", BaseURL: srv.URL, Enabled: boolPtr(true)},
	}
	cfg.Probe.Retries = 1
	cfg.Probe.RetryDelay = config.Duration(5 * time.Millisecond)

	st := stats.New("", 0)
	m := NewManager(*cfg, st)
	m.probe(m.upstreams[0])

	if m.upstreams[0].Online {
		t.Fatalf("expected offline")
	}
	if !m.upstreams[0].Backoff {
		t.Fatalf("expected backoff mode")
	}
}

func TestProbeEmptyRegionsOffline(t *testing.T) {
	srv := newTestServer(nil, 0)
	defer srv.Close()

	cfg := config.Default()
	cfg.Upstreams = []config.Upstream{
		{Name: "empty", BaseURL: srv.URL, Enabled: boolPtr(true)},
	}
	st := stats.New("", 0)
	m := NewManager(*cfg, st)
	m.probe(m.upstreams[0])

	if m.upstreams[0].Online {
		t.Fatalf("expected offline for empty regions")
	}
}

func TestManagerRegions(t *testing.T) {
	up1 := newTestServer([]string{"us"}, 0)
	up2 := newTestServer([]string{"cn"}, 0)
	defer up1.Close()
	defer up2.Close()

	cfg := config.Default()
	cfg.Upstreams = []config.Upstream{
		{Name: "us", BaseURL: up1.URL, Enabled: boolPtr(true)},
		{Name: "cn", BaseURL: up2.URL, Enabled: boolPtr(true)},
	}
	st := stats.New("", 0)
	m := NewManager(*cfg, st)
	m.ProbeAll()

	regions := m.Regions()
	if len(regions) != 2 || regions[0] != "cn" || regions[1] != "us" {
		t.Fatalf("regions = %v", regions)
	}
}

func TestOnlineSupporting(t *testing.T) {
	up1 := newTestServer([]string{"us"}, 0)
	defer up1.Close()

	cfg := config.Default()
	cfg.Upstreams = []config.Upstream{
		{Name: "us", BaseURL: up1.URL, Enabled: boolPtr(true)},
	}
	st := stats.New("", 0)
	m := NewManager(*cfg, st)
	m.ProbeAll()

	cands := m.OnlineSupporting("us")
	if len(cands) != 1 {
		t.Fatalf("expected 1 upstream for us, got %d", len(cands))
	}
	cands = m.OnlineSupporting("cn")
	if len(cands) != 0 {
		t.Fatalf("expected 0 upstream for cn, got %d", len(cands))
	}
}

func boolPtr(b bool) *bool { return &b }

func TestSnapshot(t *testing.T) {
	srv := newTestServer([]string{"de"}, 0)
	defer srv.Close()

	cfg := config.Default()
	cfg.Upstreams = []config.Upstream{
		{Name: "de", BaseURL: srv.URL, Enabled: boolPtr(true)},
	}
	st := stats.New("", 0)
	m := NewManager(*cfg, st)
	m.ProbeAll()

	snap := m.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("expected 1 snapshot")
	}
	if !snap[0].Online {
		t.Fatalf("expected online")
	}
}

var _ = context.Background
var _ = fmt.Sprintf
var _ = sync.Mutex{}
