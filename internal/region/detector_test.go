package region

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

func newTestDetector(t *testing.T, available map[string]bool) (*Detector, *httptest.Server, *int32) {
	t.Helper()
	var hits int32
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		country := r.URL.Query().Get("country")
		if available[country] {
			fmt.Fprintf(w, `{"resultCount":1,"results":[{"trackId":1}]}`)
		} else {
			fmt.Fprintf(w, `{"resultCount":0,"results":[]}`)
		}
	}))
	det := NewDetector(Options{
		CacheTTL:      time.Minute,
		NotFoundTTL:   time.Minute,
		Concurrency:   2,
		LookupTimeout: 5 * time.Second,
		LookupBase:    srv.URL,
	})
	return det, srv, &hits
}

func TestDetectFindsRegion(t *testing.T) {
	det, srv, hits := newTestDetector(t, map[string]bool{"us": true, "cn": false})
	defer srv.Close()

	avail, err := det.Detect(context.Background(), "123456789", []string{"us", "cn"})
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if len(avail) != 1 || avail[0] != "us" {
		t.Fatalf("available = %v, want [us]", avail)
	}
	if *hits != 2 {
		t.Fatalf("hits = %d, want 2", *hits)
	}
}

func TestDetectCachesResults(t *testing.T) {
	det, srv, hits := newTestDetector(t, map[string]bool{"us": true, "cn": false})
	defer srv.Close()

	for i := 0; i < 3; i++ {
		avail, err := det.Detect(context.Background(), "987654321", []string{"us", "cn"})
		if err != nil {
			t.Fatalf("detect: %v", err)
		}
		if len(avail) != 1 || avail[0] != "us" {
			t.Fatalf("available = %v, want [us]", avail)
		}
	}
	if *hits != 2 {
		t.Fatalf("hits = %d, want 2 (cached)", *hits)
	}
}

func TestDetectNotFound(t *testing.T) {
	det, srv, _ := newTestDetector(t, map[string]bool{"us": false, "cn": false})
	defer srv.Close()

	avail, err := det.Detect(context.Background(), "000000000", []string{"us", "cn"})
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if len(avail) != 0 {
		t.Fatalf("available = %v, want empty", avail)
	}
}

func TestDetectPartialLookupFailure(t *testing.T) {
	var mu sync.Mutex
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		callCount++
		n := callCount
		mu.Unlock()
		if n == 1 { // first region request fails
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(w, `{"resultCount":1,"results":[]}`)
	}))
	defer srv.Close()

	det := NewDetector(Options{
		CacheTTL:      time.Minute,
		NotFoundTTL:   time.Minute,
		Concurrency:   2,
		LookupTimeout: 5 * time.Second,
		LookupBase:    srv.URL,
	})

	avail, err := det.Detect(context.Background(), "555555555", []string{"us", "cn"})
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	// one region succeeded, so no error and that region is returned
	if len(avail) == 0 {
		t.Fatalf("expected at least one region from partial success")
	}
}

func TestNormalizeUnique(t *testing.T) {
	got := normalizeUnique([]string{"US", "us", " cn ", "", "CN", "cn"})
	if len(got) != 2 || got[0] != "cn" || got[1] != "us" {
		t.Fatalf("normalize = %v", got)
	}
}

var _ = strconv.Itoa // keep import if unused in future edits
