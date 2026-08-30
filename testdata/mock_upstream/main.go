// mock_upstream simulates a wrapper API backend for testing wrapper-lite.
//
// Usage:
//
//	go run ./testdata/mock_upstream --name "US" --addr :3001 --regions us
//
// Optional --fail makes /status always return an error so you can watch the
// health probe mark the upstream offline and enter backoff mode.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
)

func main() {
	name := flag.String("name", "mock", "upstream display name")
	addr := flag.String("addr", ":3001", "listen address")
	regionsFlag := flag.String("regions", "us", "comma separated region codes")
	fail := flag.Bool("fail", false, "always fail /status")
	flag.Parse()

	var regions []string
	for _, r := range strings.Split(*regionsFlag, ",") {
		if r = strings.TrimSpace(r); r != "" {
			regions = append(regions, r)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		if *fail {
			http.Error(w, `{"code":500,"msg":"mock failure"}`, http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{
			"code": 0, "msg": "SUCCESS",
			"data": map[string]any{"regions": regions},
		})
	})
	for _, ep := range []string{"/m3u8", "/key", "/lyrics", "/webplayback"} {
		epCopy := ep
		mux.HandleFunc("GET "+epCopy, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, map[string]any{
				"code": 0, "msg": "SUCCESS",
				"data": map[string]any{
					"upstream": *name,
					"endpoint": epCopy,
					"adamId":   r.URL.Query().Get("adamId"),
					"uri":      r.URL.Query().Get("uri"),
				},
			})
		})
	}
	mux.HandleFunc("POST /license", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"code": 0, "msg": "SUCCESS",
			"data": map[string]any{"upstream": *name, "endpoint": "/license"},
		})
	})

	log.Printf("mock upstream %q listening on %s, regions=%v, fail=%v", *name, *addr, regions, *fail)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		fmt.Fprintln(w, err)
	}
}
