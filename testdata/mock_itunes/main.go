// mock_itunes simulates the itunes.apple.com/lookup endpoint for testing.
// Usage:
//
//	go run ./testdata/mock_itunes --addr :4000
//
// By default, even-numbered adamIds are available in "us", odd in "cn".
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"strconv"
)

func main() {
	addr := flag.String("addr", ":4000", "listen address")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/lookup", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		country := r.URL.Query().Get("country")
		n, _ := strconv.Atoi(id)
		available := (n%2 == 0 && country == "us") || (n%2 == 1 && country == "cn")
		var resultCount int
		if available {
			resultCount = 1
		}
		json.NewEncoder(w).Encode(map[string]any{
			"resultCount": resultCount,
			"results":     []any{},
		})
	})
	log.Printf("mock iTunes on %s (even=us, odd=cn)", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}


