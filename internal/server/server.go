// Package server wires the aggregated public API, the admin API and the
// embedded web dashboard into a single HTTP server.
package server

import (
	"bytes"
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"wrapper-lite/internal/auth"
	"wrapper-lite/internal/config"
	"wrapper-lite/internal/region"
	"wrapper-lite/internal/stats"
	"wrapper-lite/internal/upstream"
)

//go:embed all:web
var webFS embed.FS

type Server struct {
	cfg    *config.Config
	auth   *auth.Auth
	stats  *stats.Stats
	up     *upstream.Manager
	det    *region.Detector
	client *http.Client

	rrMu sync.Mutex
	rr   map[string]int
}

func New(cfg *config.Config, a *auth.Auth, st *stats.Stats, up *upstream.Manager, det *region.Detector) *Server {
	return &Server{
		cfg:    cfg,
		auth:   a,
		stats:  st,
		up:     up,
		det:    det,
		client: &http.Client{Timeout: cfg.UpstreamTimeout.Duration()},
		rr:     map[string]int{},
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Public aggregated endpoints.
	for _, p := range []string{"/m3u8", "/key", "/lyrics", "/webplayback"} {
		mux.HandleFunc("GET "+p, s.proxyHandler(p))
	}
	mux.HandleFunc("POST /license", s.proxyHandler("/license"))
	mux.HandleFunc("GET /status", s.handlePublicStatus)

	// Auth.
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	mux.HandleFunc("GET /api/me", s.handleMe)

	// Admin API.
	mux.Handle("GET /api/status", s.requireAuth(http.HandlerFunc(s.handleAdminStatus)))
	mux.Handle("GET /api/stats", s.requireAuth(http.HandlerFunc(s.handleAdminStats)))
	mux.Handle("GET /api/regions", s.requireAuth(http.HandlerFunc(s.handleAdminRegions)))

	// Frontend.
	mux.HandleFunc("GET /login", s.serveLogin)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(s.webSub()))))
	mux.HandleFunc("GET /", s.serveIndex)

	return logMiddleware(mux)
}

func (s *Server) webSub() fs.FS {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		panic(err)
	}
	return sub
}

// ---- Public aggregated endpoints ----

func (s *Server) handlePublicStatus(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]any{
		"code": 0, "msg": "SUCCESS",
		"data": map[string]any{"regions": s.up.Regions()},
	})
}

func (s *Server) proxyHandler(path string) http.HandlerFunc {
	endpoint := strings.TrimPrefix(path, "/")
	return func(w http.ResponseWriter, r *http.Request) {
		adamID, err := s.extractAdamID(r)
		if err != nil {
			s.writeJSON(w, http.StatusBadRequest, errorResponse(400, err.Error()))
			return
		}

		regions := s.up.Regions()
		if len(regions) == 0 {
			s.writeJSON(w, http.StatusServiceUnavailable, errorResponse(503, "no upstream available"))
			return
		}

		available, err := s.det.Detect(r.Context(), adamID, regions)
		if err != nil {
			s.writeJSON(w, http.StatusBadGateway, errorResponse(502, "region detection failed: "+err.Error()))
			return
		}
		if len(available) == 0 {
			s.writeJSON(w, http.StatusNotFound, errorResponse(404, "resource not found in supported regions"))
			return
		}

		u := s.pickUpstream(available)
		if u == nil {
			s.writeJSON(w, http.StatusServiceUnavailable, errorResponse(503, "no online upstream for detected region"))
			return
		}

		target := u.BaseURL + path
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		outReq, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
		if err != nil {
			s.writeJSON(w, http.StatusInternalServerError, errorResponse(500, "failed to build request"))
			return
		}
		outReq.Header = r.Header.Clone()
		outReq.Header.Set("User-Agent", "wrapper-lite/1.0")

		resp, err := s.client.Do(outReq)
		if err != nil {
			s.writeJSON(w, http.StatusBadGateway, errorResponse(502, "upstream request failed: "+err.Error()))
			return
		}
		defer resp.Body.Close()

		s.stats.Incr(u.Name, endpoint)

		copyHeaders(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}
}

func (s *Server) extractAdamID(r *http.Request) (string, error) {
	var adamID string
	if r.Method == http.MethodPost {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return "", err
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		var req struct {
			AdamID string `json:"adamId"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			return "", err
		}
		adamID = req.AdamID
	} else {
		adamID = r.URL.Query().Get("adamId")
	}
	adamID = strings.TrimSpace(adamID)
	if adamID == "" {
		return "", nil
	}
	return adamID, nil
}

func (s *Server) pickUpstream(available []string) *upstream.Upstream {
	s.rrMu.Lock()
	defer s.rrMu.Unlock()
	for _, region := range available {
		cands := s.up.OnlineSupporting(region)
		if len(cands) == 0 {
			continue
		}
		idx := s.rr[region]
		s.rr[region] = (idx + 1) % len(cands)
		return cands[idx]
	}
	return nil
}

// ---- Admin API ----

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.auth.Valid(sessionToken(r)) {
			s.writeJSON(w, http.StatusUnauthorized, errorResponse(401, "unauthorized"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func sessionToken(r *http.Request) string {
	if c, err := r.Cookie("wl_token"); err == nil && c.Value != "" {
		return c.Value
	}
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return ""
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		s.writeJSON(w, http.StatusBadRequest, errorResponse(400, "invalid request"))
		return
	}
	token, ok := s.auth.Login(req.Username, req.Password)
	if !ok {
		s.writeJSON(w, http.StatusUnauthorized, errorResponse(401, "invalid username or password"))
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "wl_token",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(s.cfg.SessionTTL.Duration().Seconds()),
	})
	s.writeJSON(w, http.StatusOK, map[string]any{
		"code": 0, "msg": "SUCCESS",
		"data": map[string]any{"token": token},
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.auth.Logout(sessionToken(r))
	http.SetCookie(w, &http.Cookie{Name: "wl_token", Value: "", Path: "/", HttpOnly: true, MaxAge: -1})
	s.writeJSON(w, http.StatusOK, successResponse())
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	if !s.auth.Valid(sessionToken(r)) {
		s.writeJSON(w, http.StatusUnauthorized, errorResponse(401, "unauthorized"))
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"code": 0, "msg": "SUCCESS",
		"data": map[string]any{"username": s.cfg.Auth.Username},
	})
}

func (s *Server) handleAdminStatus(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]any{
		"code": 0, "msg": "SUCCESS",
		"data": map[string]any{
			"regions":   s.up.Regions(),
			"upstreams": s.up.Snapshot(),
		},
	})
}

func (s *Server) handleAdminStats(w http.ResponseWriter, r *http.Request) {
	days := 7
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 90 {
			days = n
		}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"code": 0, "msg": "SUCCESS",
		"data": s.stats.Snapshot(days),
	})
}

func (s *Server) handleAdminRegions(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]any{
		"code": 0, "msg": "SUCCESS",
		"data": map[string]any{"regions": s.up.Regions()},
	})
}

// ---- Frontend ----

func (s *Server) serveLogin(w http.ResponseWriter, r *http.Request) {
	b, err := webFS.ReadFile("web/login.html")
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(b)
}

func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	b, err := webFS.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(b)
}

// ---- Helpers ----

func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write json: %v", err)
	}
}

func errorResponse(code int, msg string) map[string]any {
	return map[string]any{"code": code, "msg": msg}
}

func successResponse() map[string]any {
	return map[string]any{"code": 0, "msg": "SUCCESS"}
}

var hopHeaders = []string{
	"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
	"Te", "Trailer", "Transfer-Encoding", "Upgrade", "Host", "Content-Length",
}

func copyHeaders(dst, src http.Header) {
	for k, vv := range src {
		if isHopHeader(k) {
			continue
		}
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

func isHopHeader(k string) bool {
	for _, h := range hopHeaders {
		if strings.EqualFold(k, h) {
			return true
		}
	}
	return false
}

func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		if !strings.HasPrefix(r.URL.Path, "/static/") {
			log.Printf("%s %s -> %d (%s)", r.Method, r.URL.Path, sw.status, time.Since(start).Round(time.Millisecond))
		}
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}
