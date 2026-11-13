// Package api implements the homebackupd HTTP API (see api/openapi.yaml).
package api

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/LeoHChen/homebackup/internal/engine"
)

// TokenChecker reports whether a bearer token belongs to a paired device.
type TokenChecker interface {
	Valid(token string) bool
}

// StaticTokens is a TokenChecker backed by a fixed list. It is used until
// device pairing is implemented in the catalog.
type StaticTokens []string

// Valid implements TokenChecker using constant-time comparison.
func (s StaticTokens) Valid(token string) bool {
	ok := false
	for _, t := range s {
		if t != "" && subtle.ConstantTimeCompare([]byte(t), []byte(token)) == 1 {
			ok = true
		}
	}
	return ok
}

// SourceStatus is the sync state of one source.
type SourceStatus struct {
	ID       string     `json:"id"`
	Type     string     `json:"type"`
	LastSync *time.Time `json:"lastSync,omitempty"`
	LastErr  string     `json:"lastError,omitempty"`
}

// Status is returned by GET /api/v1/status.
type Status struct {
	Version string         `json:"version"`
	Started time.Time      `json:"started"`
	Root    string         `json:"root"`
	Sources []SourceStatus `json:"sources"`
}

// StatusFunc returns the current server status.
type StatusFunc func() Status

// Server serves the HTTP API.
type Server struct {
	Version string
	Tokens  TokenChecker
	Status  StatusFunc
	Log     *slog.Logger
	// Engine backs the sources, files, and trash endpoints. Optional.
	Engine *engine.Engine
}

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", s.health)
	mux.Handle("GET /api/v1/status", s.auth(http.HandlerFunc(s.status)))
	if s.Engine != nil {
		s.fileRoutes(mux)
	}
	return s.logRequests(mux)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": s.Version})
}

func (s *Server) status(w http.ResponseWriter, _ *http.Request) {
	st := s.Status()
	if st.Sources == nil {
		st.Sources = []SourceStatus{}
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || s.Tokens == nil || !s.Tokens.Valid(token) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="homebackup"`)
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid device token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.code = code
	r.ResponseWriter.WriteHeader(code)
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(rec, r)
		if s.Log != nil {
			s.Log.Info("http", "method", r.Method, "path", r.URL.Path, "status", rec.code, "dur", time.Since(start))
		}
	})
}

// Error is the JSON error body returned by every endpoint.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]Error{"error": {Code: code, Message: msg}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
