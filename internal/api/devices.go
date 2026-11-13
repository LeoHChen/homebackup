package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/LeoHChen/homebackup/internal/catalog"
)

// DeviceTokens authenticates paired devices against the catalog.
type DeviceTokens struct {
	Catalog *catalog.Catalog
}

// Authenticate implements Authenticator.
func (d DeviceTokens) Authenticate(ctx context.Context, token string) (Principal, bool) {
	dev, err := d.Catalog.DeviceByToken(ctx, token, time.Now())
	if err != nil {
		return Principal{}, false
	}
	return Principal{DeviceID: dev.ID}, true
}

// DeviceJSON is the API representation of a paired device.
type DeviceJSON struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Platform string     `json:"platform"`
	Created  time.Time  `json:"created"`
	LastSeen *time.Time `json:"lastSeen,omitempty"`
}

var platforms = map[string]bool{"ios": true, "macos": true, "cli": true}

// pairLimiter slows down guessing across all clients: at most one attempt
// per second on average, bursting to 5.
type pairLimiter struct {
	mu     sync.Mutex
	tokens float64
	last   time.Time
}

func (l *pairLimiter) allow(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.last.IsZero() {
		l.tokens = 5
	} else {
		l.tokens = min(5, l.tokens+now.Sub(l.last).Seconds())
	}
	l.last = now
	if l.tokens < 1 {
		return false
	}
	l.tokens--
	return true
}

func (s *Server) deviceRoutes(mux *http.ServeMux) {
	lim := &pairLimiter{}
	mux.HandleFunc("POST /api/v1/pair", func(w http.ResponseWriter, r *http.Request) { s.pair(w, r, lim) })
	mux.Handle("GET /api/v1/devices", s.auth(http.HandlerFunc(s.listDevices)))
	mux.Handle("DELETE /api/v1/devices/{id}", s.auth(http.HandlerFunc(s.deleteDevice)))
}

func (s *Server) pair(w http.ResponseWriter, r *http.Request, lim *pairLimiter) {
	if !lim.allow(time.Now()) {
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many pairing attempts")
		return
	}
	var req struct {
		Code       string `json:"code"`
		DeviceName string `json:"deviceName"`
		Platform   string `json:"platform"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	req.DeviceName = strings.TrimSpace(req.DeviceName)
	if req.DeviceName == "" || len(req.DeviceName) > 100 || !platforms[req.Platform] {
		writeError(w, http.StatusBadRequest, "bad_request", "deviceName (1-100 chars) and platform (ios, macos, cli) are required")
		return
	}
	dev, token, err := s.Engine.Catalog.Pair(r.Context(), strings.ReplaceAll(req.Code, " ", ""), req.DeviceName, req.Platform, time.Now())
	if errors.Is(err, catalog.ErrBadCode) {
		writeError(w, http.StatusUnauthorized, "bad_code", "invalid or expired pairing code")
		return
	}
	if err != nil {
		s.internalError(w, err)
		return
	}
	if s.Log != nil {
		s.Log.Info("device paired", "id", dev.ID, "name", dev.Name, "platform", dev.Platform)
	}
	resp := map[string]string{"deviceId": dev.ID, "token": token}
	if s.CertFingerprint != "" {
		resp["certFingerprint"] = s.CertFingerprint
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) listDevices(w http.ResponseWriter, r *http.Request) {
	ds, err := s.Engine.Catalog.ListDevices(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	out := make([]DeviceJSON, 0, len(ds))
	for _, d := range ds {
		out = append(out, DeviceJSON{ID: d.ID, Name: d.Name, Platform: d.Platform, Created: d.CreatedAt, LastSeen: d.LastSeen})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) deleteDevice(w http.ResponseWriter, r *http.Request) {
	err := s.Engine.Catalog.DeleteDevice(r.Context(), r.PathValue("id"))
	if errors.Is(err, catalog.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "device not found")
		return
	}
	if err != nil {
		s.internalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
