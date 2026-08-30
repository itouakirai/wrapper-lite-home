package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"wrapper-lite/internal/auth"
	"wrapper-lite/internal/config"
	"wrapper-lite/internal/region"
	"wrapper-lite/internal/stats"
	"wrapper-lite/internal/upstream"
)

type mockUpstream struct {
	srv      *httptest.Server
	regions  []string
	hits     int32
	lastBody atomic.Value
}

func newMockUpstream(t *testing.T, name string, regions []string) *mockUpstream {
	t.Helper()
	mu := &mockUpstream{regions: regions}
	mu.lastBody.Store("")
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"code": 0, "msg": "SUCCESS",
			"data": map[string]any{"regions": regions},
		})
	})
	for _, ep := range []string{"/m3u8", "/key", "/lyrics", "/webplayback"} {
		mux.HandleFunc("GET "+ep, func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&mu.hits, 1)
			json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "msg": "SUCCESS",
				"data": map[string]any{
					"upstream": name,
					"adamId":   r.URL.Query().Get("adamId"),
				},
			})
		})
	}
	mux.HandleFunc("POST /license", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&mu.hits, 1)
		body, _ := io.ReadAll(r.Body)
		mu.lastBody.Store(string(body))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{
			"code": 0, "msg": "SUCCESS",
			"data": map[string]any{"upstream": name, "echo": string(body)},
		})
	})
	mu.srv = httptest.NewServer(mux)
	t.Cleanup(mu.srv.Close)
	return mu
}

func newTestApp(t *testing.T, availability func(id, country string) bool, mocks ...*mockUpstream) (*httptest.Server, *http.Client) {
	t.Helper()

	itunesSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		country := r.URL.Query().Get("country")
		id := r.URL.Query().Get("id")
		if availability(id, country) {
			fmt.Fprintf(w, `{"resultCount":1,"results":[{}]}`)
		} else {
			fmt.Fprintf(w, `{"resultCount":0,"results":[]}`)
		}
	}))
	t.Cleanup(itunesSrv.Close)

	cfg := config.Default()
	cfg.Listen = ":0"
	cfg.Auth = config.AuthConfig{Username: "admin", Password: "admin"}
	cfg.Region.CacheTTL = config.Duration(time.Minute)
	cfg.Region.LookupBase = itunesSrv.URL
	cfg.Probe.RetryDelay = config.Duration(5 * time.Millisecond)
	for _, m := range mocks {
		cfg.Upstreams = append(cfg.Upstreams, config.Upstream{Name: m.srv.URL, BaseURL: m.srv.URL})
	}

	st := stats.New("", 0)
	a := auth.New(cfg.Auth.Username, cfg.Auth.Password, time.Hour)
	up := upstream.NewManager(*cfg, st)
	up.Start(context.Background())
	t.Cleanup(up.Stop)

	det := region.NewDetector(region.Options{
		CacheTTL:      cfg.Region.CacheTTL.Duration(),
		NotFoundTTL:   cfg.Region.NotFoundTTL.Duration(),
		Concurrency:   4,
		LookupTimeout: 5 * time.Second,
		LookupBase:    cfg.Region.LookupBase,
	})

	srv := New(cfg, a, st, up, det)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, ts.Client()
}

func login(t *testing.T, ts *httptest.Server) *http.Cookie {
	t.Helper()
	body := bytes.NewReader([]byte(`{"username":"admin","password":"admin"}`))
	resp, err := http.Post(ts.URL+"/api/login", "application/json", body)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d", resp.StatusCode)
	}
	var cookies []*http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "wl_token" {
			cookies = append(cookies, c)
		}
	}
	if len(cookies) != 1 {
		t.Fatalf("expected wl_token cookie, got %d", len(cookies))
	}
	return cookies[0]
}

func doGet(t *testing.T, client *http.Client, url string, cookie *http.Cookie) (*http.Response, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	return resp, out
}

func TestFullFlowRegionRouting(t *testing.T) {
	// adamId available only in "us" storefront -> routed to the US upstream.
	upUS := newMockUpstream(t, "US", []string{"us"})
	upCN := newMockUpstream(t, "CN", []string{"cn"})
	ts, client := newTestApp(t, func(id, country string) bool {
		// adamId starting with 1 -> us only; starting with 2 -> cn only
		if strings.HasPrefix(id, "2") {
			return country == "cn"
		}
		return country == "us"
	}, upUS, upCN)

	cookie := login(t, ts)

	// public /status shows merged regions (both online after probe)
	waitFor(t, func() bool {
		resp, out := doGet(t, client, ts.URL+"/status", nil)
		if resp.StatusCode != 200 {
			return false
		}
		data, _ := out["data"].(map[string]any)
		regions, _ := data["regions"].([]any)
		return len(regions) == 2
	})

	// m3u8 with adamId available in us -> forwarded to US upstream
	resp, out := doGet(t, client, ts.URL+"/m3u8?adamId=111111&foo=bar", cookie)
	if resp.StatusCode != 200 {
		t.Fatalf("m3u8 status = %d, body=%v", resp.StatusCode, out)
	}
	data, _ := out["data"].(map[string]any)
	if data["upstream"] != "US" {
		t.Fatalf("routed to %v, want US", data["upstream"])
	}
	if data["adamId"] != "111111" {
		t.Fatalf("adamId = %v", data["adamId"])
	}

	// license POST to CN-region adamId -> CN upstream, body forwarded
	licenseBody := `{"adamId":"222222","challenge":"abc","uri":"x"}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/license", bytes.NewReader([]byte(licenseBody)))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	lresp, err := client.Do(req)
	if err != nil {
		t.Fatalf("license: %v", err)
	}
	var lout map[string]any
	json.NewDecoder(lresp.Body).Decode(&lout)
	lresp.Body.Close()
	ldata, _ := lout["data"].(map[string]any)
	if lresp.StatusCode != http.StatusCreated {
		t.Fatalf("license status = %d", lresp.StatusCode)
	}
	if ldata["upstream"] != "CN" {
		t.Fatalf("license routed to %v, want CN", ldata["upstream"])
	}

	// stats reflect the routed requests
	resp, out = doGet(t, client, ts.URL+"/api/stats?days=7", cookie)
	if resp.StatusCode != 200 {
		t.Fatalf("stats status = %d", resp.StatusCode)
	}
	sdata, _ := out["data"].(map[string]any)
	if total, ok := sdata["total"].(float64); !ok || total < 2 {
		t.Fatalf("total = %v, want >= 2", sdata["total"])
	}
	today, _ := sdata["today"].(map[string]any)
	endpoints, _ := today["endpoints"].(map[string]any)
	if endpoints["m3u8"] == nil || endpoints["license"] == nil {
		t.Fatalf("endpoints = %v", endpoints)
	}

	// admin status returns upstream snapshots
	resp, out = doGet(t, client, ts.URL+"/api/status", cookie)
	if resp.StatusCode != 200 {
		t.Fatalf("api status = %d", resp.StatusCode)
	}
	sdata, _ = out["data"].(map[string]any)
	ups, _ := sdata["upstreams"].([]any)
	if len(ups) != 2 {
		t.Fatalf("upstreams = %d", len(ups))
	}
}

func TestAuthRequired(t *testing.T) {
	up := newMockUpstream(t, "US", []string{"us"})
	ts, client := newTestApp(t, func(id, country string) bool { return country == "us" }, up)

	resp, _ := doGet(t, client, ts.URL+"/api/stats", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without cookie, got %d", resp.StatusCode)
	}
	resp, _ = doGet(t, client, ts.URL+"/api/status", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without cookie, got %d", resp.StatusCode)
	}

	// wrong password rejected
	body := bytes.NewReader([]byte(`{"username":"admin","password":"nope"}`))
	r, _ := http.Post(ts.URL+"/api/login", "application/json", body)
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for wrong password, got %d", r.StatusCode)
	}
	r.Body.Close()
}

func TestLoginPageServed(t *testing.T) {
	up := newMockUpstream(t, "US", []string{"us"})
	ts, client := newTestApp(t, func(id, country string) bool { return country == "us" }, up)

	resp, err := client.Get(ts.URL + "/login")
	if err != nil {
		t.Fatalf("login page: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "login-form") {
		t.Fatalf("login page missing form")
	}
}

func waitFor(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("condition not met within 5s")
}

var _ = sync.Mutex{}
