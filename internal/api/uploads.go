package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/LeoHChen/homebackup/internal/catalog"
	"github.com/LeoHChen/homebackup/internal/engine"
	"github.com/LeoHChen/homebackup/internal/source"
)

// maxUploadSize bounds a single upload (large 4K/ProRes videos fit).
const maxUploadSize = 256 << 30

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// uploadLocks serializes PATCH/complete per session.
type uploadLocks struct{ m sync.Map }

func (l *uploadLocks) lock(id string) func() {
	v, _ := l.m.LoadOrStore(id, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (s *Server) uploadRoutes(mux *http.ServeMux) {
	locks := &uploadLocks{}
	h := func(pattern string, fn http.HandlerFunc) { mux.Handle(pattern, s.auth(fn)) }
	h("POST /api/v1/uploads", s.createUpload)
	h("HEAD /api/v1/uploads/{id}", func(w http.ResponseWriter, r *http.Request) { s.uploadOffset(w, r) })
	h("PATCH /api/v1/uploads/{id}", func(w http.ResponseWriter, r *http.Request) { s.appendUpload(w, r, locks) })
	h("POST /api/v1/uploads/{id}/complete", func(w http.ResponseWriter, r *http.Request) { s.completeUpload(w, r, locks) })
}

func (s *Server) createUpload(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Source  string    `json:"source"`
		Path    string    `json:"path"`
		Size    int64     `json:"size"`
		SHA256  string    `json:"sha256"`
		AssetID string    `json:"assetId"`
		Created time.Time `json:"created"`
		// Drive sources: FileID replaces an existing file (new version);
		// BaseSHA256 is the content the client last saw, for conflict detection.
		FileID     string `json:"fileId"`
		BaseSHA256 string `json:"baseSha256"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	typ, ok := s.Engine.SourceType(req.Source)
	if !ok || (typ != "device-upload" && typ != engine.WritableType) {
		writeError(w, http.StatusBadRequest, "bad_request", "source must be a configured device-upload or drive source")
		return
	}
	if _, err := s.Engine.Store.DataPath(req.Source, req.Path); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid path")
		return
	}
	if req.Size < 0 || req.Size > maxUploadSize || !sha256Hex.MatchString(req.SHA256) {
		writeError(w, http.StatusBadRequest, "bad_request", "size and lowercase hex sha256 are required")
		return
	}
	itemID := req.AssetID
	if itemID == "" {
		itemID = "sha256:" + req.SHA256
	}
	if typ == engine.WritableType {
		var status int
		var code, msg string
		itemID, req.Path, status, code, msg = s.driveUploadTarget(r, req.Source, req.Path, req.FileID, req.BaseSHA256, req.SHA256)
		if status != 0 {
			writeError(w, status, code, msg)
			return
		}
	}

	// Already backed up with the same content?
	if f, err := s.Engine.Catalog.FileBySourceItem(r.Context(), req.Source, itemID); err == nil && f.DeletedAt == nil && f.SHA256 == req.SHA256 {
		writeJSON(w, http.StatusOK, map[string]any{"status": "exists", "file": fileJSON(f)})
		return
	} else if err != nil && !errors.Is(err, catalog.ErrNotFound) {
		s.internalError(w, err)
		return
	}

	me := PrincipalFrom(r.Context()).DeviceID
	modified := req.Created
	if modified.IsZero() {
		modified = time.Now()
	}
	u, created, err := s.Engine.Catalog.CreateUpload(r.Context(), catalog.Upload{
		DeviceID: me, Source: req.Source, ItemID: itemID, Path: req.Path,
		Size: req.Size, SHA256: req.SHA256, Modified: modified, CreatedAt: time.Now(),
	})
	if err != nil {
		s.internalError(w, err)
		return
	}
	if !created && (u.DeviceID != me || u.SHA256 != req.SHA256 || u.Size != req.Size) {
		writeError(w, http.StatusConflict, "conflict", "another upload of this item is in progress")
		return
	}
	if created {
		u.TmpPath = filepath.Join(s.Engine.Store.TmpDir(), "upload-"+u.ID)
		f, err := os.OpenFile(u.TmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err == nil {
			err = f.Close()
		}
		if err == nil {
			err = s.Engine.Catalog.SetUploadTmpPath(r.Context(), u.ID, u.TmpPath)
		}
		if err != nil {
			s.internalError(w, err)
			return
		}
	}
	off, err := fileSize(u.TmpPath)
	if err != nil {
		s.internalError(w, err)
		return
	}
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	w.Header().Set("Upload-Offset", strconv.FormatInt(off, 10))
	writeJSON(w, status, map[string]any{"uploadId": u.ID, "offset": off})
}

func fileSize(p string) (int64, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

// session loads an upload owned by the caller.
func (s *Server) session(w http.ResponseWriter, r *http.Request) (catalog.Upload, bool) {
	u, err := s.Engine.Catalog.GetUpload(r.Context(), r.PathValue("id"))
	if errors.Is(err, catalog.ErrNotFound) || (err == nil && u.DeviceID != PrincipalFrom(r.Context()).DeviceID) {
		writeError(w, http.StatusNotFound, "not_found", "upload not found")
		return catalog.Upload{}, false
	}
	if err != nil {
		s.internalError(w, err)
		return catalog.Upload{}, false
	}
	return u, true
}

func (s *Server) uploadOffset(w http.ResponseWriter, r *http.Request) {
	u, ok := s.session(w, r)
	if !ok {
		return
	}
	off, err := fileSize(u.TmpPath)
	if err != nil {
		s.internalError(w, err)
		return
	}
	w.Header().Set("Upload-Offset", strconv.FormatInt(off, 10))
	w.Header().Set("Upload-Length", strconv.FormatInt(u.Size, 10))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) appendUpload(w http.ResponseWriter, r *http.Request, locks *uploadLocks) {
	u, ok := s.session(w, r)
	if !ok {
		return
	}
	defer locks.lock(u.ID)()
	off, err := fileSize(u.TmpPath)
	if err != nil {
		s.internalError(w, err)
		return
	}
	w.Header().Set("Upload-Offset", strconv.FormatInt(off, 10))
	want, err := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
	if err != nil || want != off {
		writeError(w, http.StatusConflict, "offset_mismatch", fmt.Sprintf("resume from offset %d", off))
		return
	}
	f, err := os.OpenFile(u.TmpPath, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		s.internalError(w, err)
		return
	}
	remaining := u.Size - off
	n, copyErr := io.Copy(f, io.LimitReader(r.Body, remaining+1))
	if n > remaining {
		// Too much data: keep only what fits.
		f.Truncate(u.Size)
		n = remaining
		copyErr = errTooLarge
	}
	if err := f.Close(); err != nil && copyErr == nil {
		copyErr = err
	}
	w.Header().Set("Upload-Offset", strconv.FormatInt(off+n, 10))
	switch {
	case errors.Is(copyErr, errTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "chunk exceeds declared size")
	case copyErr != nil:
		// Client disconnected mid-chunk: what arrived is kept; resume from Upload-Offset.
		writeError(w, http.StatusBadRequest, "incomplete", "chunk interrupted; resume from Upload-Offset")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

var errTooLarge = errors.New("too large")

func (s *Server) completeUpload(w http.ResponseWriter, r *http.Request, locks *uploadLocks) {
	u, ok := s.session(w, r)
	if !ok {
		return
	}
	defer locks.lock(u.ID)()
	f, err := os.Open(u.TmpPath)
	if err != nil {
		s.internalError(w, err)
		return
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		f.Close()
		s.internalError(w, err)
		return
	}
	if n != u.Size {
		f.Close()
		w.Header().Set("Upload-Offset", strconv.FormatInt(n, 10))
		writeError(w, http.StatusConflict, "incomplete", fmt.Sprintf("received %d of %d bytes", n, u.Size))
		return
	}
	if hex.EncodeToString(h.Sum(nil)) != u.SHA256 {
		f.Close()
		s.dropUpload(r, u)
		writeError(w, http.StatusUnprocessableEntity, "checksum_mismatch", "SHA-256 does not match; upload discarded")
		return
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		s.internalError(w, err)
		return
	}
	err = s.Engine.Put(r.Context(), u.Source, source.Item{
		SourceID: u.ItemID, Path: u.Path, Size: u.Size, Modified: u.Modified, Checksum: "sha256:" + u.SHA256,
	}, f)
	f.Close()
	if err != nil {
		s.internalError(w, err)
		return
	}
	s.dropUpload(r, u)
	got, err := s.Engine.Catalog.FileBySourceItem(r.Context(), u.Source, u.ItemID)
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, fileJSON(got))
}

func (s *Server) dropUpload(r *http.Request, u catalog.Upload) {
	os.Remove(u.TmpPath)
	if err := s.Engine.Catalog.DeleteUpload(r.Context(), u.ID); err != nil && s.Log != nil {
		s.Log.Error("delete upload", "id", u.ID, "err", err)
	}
}
