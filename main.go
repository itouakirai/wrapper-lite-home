// wrapper-lite aggregates multiple Apple Music decryption wrapper APIs behind
// a single HTTP port, routing requests by the storefront region of the
// requested adamId. It ships an AdGuard-Home-style admin dashboard.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"wrapper-lite/internal/auth"
	"wrapper-lite/internal/config"
	"wrapper-lite/internal/region"
	"wrapper-lite/internal/server"
	"wrapper-lite/internal/stats"
	"wrapper-lite/internal/upstream"
)

func main() {
	configPath := flag.String("config", "config.json", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		log.Fatalf("invalid config: %v", err)
	}
	if cfg.Auth.Password == "admin" {
		log.Println("warning: you are using the default admin password, please change it in the config")
	}

	st := stats.New(cfg.StatsFile, cfg.StatsInterval.Duration())
	if err := st.Load(); err != nil {
		log.Printf("warning: load stats: %v", err)
	}
	st.Start()

	authSvc := auth.New(cfg.Auth.Username, cfg.Auth.Password, cfg.SessionTTL.Duration())

	up := upstream.NewManager(*cfg, st)
	up.Start(context.Background())

	det := region.NewDetector(region.Options{
		CacheTTL:      cfg.Region.CacheTTL.Duration(),
		NotFoundTTL:   cfg.Region.NotFoundTTL.Duration(),
		Concurrency:   cfg.Region.Concurrency,
		LookupTimeout: cfg.Region.LookupTimeout.Duration(),
		LookupBase:    cfg.Region.LookupBase,
	})

	srv := server.New(cfg, authSvc, st, up, det)

	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("wrapper-lite listening on http://%s", cfg.Listen)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig

	log.Println("shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
	up.Stop()
	st.Stop()
	log.Println("bye")
}
