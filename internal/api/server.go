// Package api implements the homebackupd HTTP API (see api/openapi.yaml).
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/LeoHChen/homebackup/internal/engine"
	"github.com/LeoHChen/homebackup/internal/thumbs"
)

// Principal is the authenticated caller.
type Principal struct {
	// DeviceID is the paired device, or "" for the admin token.
	DeviceID string
}

// Authenticator maps a bearer token to a principal.
type Authenticator interface {
	Authenticate(ctx context.Context, token string) (Principal, bool)
}

// StaticTokens is an Authenticator backed by a fixed list of admin tokens
// (HOMEBACKUP_TOKEN).
type StaticTokens []string

// Authenticate implements Authenticator using constant-time comparison.
func (s StaticTokens) Authenticate(_ context.Context, token string) (Principal, bool) {
	ok := false
	for _, t := range s {
		if t != "" && subtle.ConstantTimeCompare([]byte(t), []byte(token)) == 1 {
			ok = true
		}
	}
	return Principal{}, ok
}

// Authenticators tries each Authenticator in order.
type Authenticators []Authenticator

// Authenticate implements Authenticator.
func (as Authenticators) Authenticate(ctx context.Context, token string) (Principal, bool) {
	for _, a := range as {
		if p, ok := a.Authenticate(ctx, token); ok {
			return p, true
		}
	}
	return Principal{}, false
}

type principalKey struct{}

// PrincipalFrom returns the authenticated caller of a request.
func PrincipalFrom(ctx context.Context) Principal {
	p, _ := ctx.Value(principalKey{}).(Principal)
	return p
}

// SourceStatus is the sync state of one source, with how much it holds.
type SourceStatus struct {
	ID       string            `json:"id"`
	Type     string            `json:"type"`
	Schedule string            `json:"schedule,omitempty"`
	Running  bool              `json:"running"`
	LastSync *time.Time        `json:"lastSync,omitempty"`
	LastErr  string            `json:"lastError,omitempty"`
	Options  map[string]string `json:"options,omitempty"`
	// Files and Bytes count current files; Trashed* count deleted files
	// still kept in the trash.
	Files        int64 `json:"files"`
	Bytes        int64 `json:"bytes"`
	TrashedFiles int64 `json:"trashedFiles"`
	TrashedBytes int64 `json:"trashedBytes"`
}

// Disk is the usage of the volume that holds the backup root.
type Disk struct {
	Total uint64 `json:"total"`
	Free  uint64 `json:"free"`
	Used  uint64 `json:"used"`
}

// Retention mirrors the retention settings for display.
type Retention struct {
	TrashDays   int `json:"trashDays"`
	VersionDays int `json:"versionDays"`
	MaxVersions int `json:"maxVersions"`
}

// Status is returned by GET /api/v1/status.
type Status struct {
	Version   string         `json:"version"`
	Started   time.Time      `json:"started"`
	Hostname  string         `json:"hostname,omitempty"`
	Listen    string         `json:"listen,omitempty"`
	TLS       bool           `json:"tls"`
	Bonjour   bool           `json:"bonjour"`
	Root      string         `json:"root"`
	Disk      *Disk          `json:"disk,omitempty"`
	Retention *Retention     `json:"retention,omitempty"`
	Devices   int            `json:"devices"`
	Sources   []SourceStatus `json:"sources"`
}

// StatusFunc returns the current server status.
type StatusFunc func() Status

// LogSource provides the most recent log lines (see package logbuf).
type LogSource interface {
	Lines(n int) []string
}

// Server serves the HTTP API.
type Server struct {
	Version string
	// CertFingerprint is the SHA-256 of the server's TLS public key, which
	// apps pin at pairing time. Empty when serving plain HTTP.
	CertFingerprint string
	Tokens          Authenticator
	Status          StatusFunc
	Log             *slog.Logger
	// Logs serves GET /api/v1/log. Optional.
	Logs LogSource
	// Engine backs the sources, files, and trash endpoints. Optional.
	Engine *engine.Engine
	// Thumbs makes photo and video thumbnails. Optional.
	Thumbs *thumbs.Generator
	// UI serves the web dashboard for every GET outside /api. Optional.
	UI http.Handler
}

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", s.health)
	mux.Handle("GET /api/v1/status", s.auth(http.HandlerFunc(s.status)))
	mux.Handle("GET /api/v1/log", s.auth(http.HandlerFunc(s.recentLog)))
	if s.Engine != nil {
		s.fileRoutes(mux)
	}
	if s.UI != nil {
		mux.Handle("GET /", s.UI)
	}
	return s.logRequests(mux)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	body := map[string]string{"status": "ok", "version": s.Version}
	if s.CertFingerprint != "" {
		body["certFingerprint"] = s.CertFingerprint
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *Server) status(w http.ResponseWriter, _ *http.Request) {
	st := s.Status()
	if st.Sources == nil {
		st.Sources = []SourceStatus{}
	}
	writeJSON(w, http.StatusOK, st)
}

// recentLog returns the last n log lines (default 200, at most 2000).
func (s *Server) recentLog(w http.ResponseWriter, r *http.Request) {
	n := 200
	if v := r.URL.Query().Get("n"); v != "" {
		if i, err := strconv.Atoi(v); err != nil || i < 1 {
			writeError(w, http.StatusBadRequest, "bad_request", "n must be a positive number")
			return
		} else {
			n = min(i, 2000)
		}
	}
	lines := []string{}
	if s.Logs != nil {
		lines = s.Logs.Lines(n)
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": lines})
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		var p Principal
		if ok && token != "" && s.Tokens != nil {
			p, ok = s.Tokens.Authenticate(r.Context(), token)
		} else {
			ok = false
		}
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="homebackup"`)
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid device token")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
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

// quietPaths are polled by the dashboard and would drown the log.
func quietPath(p string) bool {
	return p == "/api/v1/log" || strings.HasPrefix(p, "/static/") || p == "/favicon.ico"
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(rec, r)
		if s.Log != nil && (!quietPath(r.URL.Path) || rec.code >= 400) {
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
