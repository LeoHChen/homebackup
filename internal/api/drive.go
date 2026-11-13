package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/LeoHChen/homebackup/internal/catalog"
	"github.com/LeoHChen/homebackup/internal/engine"
	"github.com/LeoHChen/homebackup/internal/store"
)

// ChangeJSON is one entry of the change feed.
type ChangeJSON struct {
	Seq     int64    `json:"seq"`
	Removed bool     `json:"removed,omitempty"`
	File    FileJSON `json:"file"`
}

const maxChangesPage = 1000

// changes serves GET /changes?source=&cursor=&limit=. Clients start with
// cursor 0 (full listing) and then pass back the returned cursor. A file in
// the trash appears with deletedAt; a file purged for good appears with
// removed=true.
func (s *Server) changes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	src := q.Get("source")
	if _, ok := s.Engine.SourceType(src); !ok {
		writeError(w, http.StatusBadRequest, "bad_request", "unknown source")
		return
	}
	cursor, err := strconv.ParseInt(q.Get("cursor"), 10, 64)
	if q.Get("cursor") == "" {
		cursor, err = 0, nil
	}
	if err != nil || cursor < 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid cursor")
		return
	}
	limit := maxChangesPage
	if l, err := strconv.Atoi(q.Get("limit")); err == nil && l > 0 && l < limit {
		limit = l
	}
	chs, more, err := s.Engine.Catalog.Changes(r.Context(), src, cursor, limit)
	if err != nil {
		s.internalError(w, err)
		return
	}
	out := make([]ChangeJSON, 0, len(chs))
	next := cursor
	for _, c := range chs {
		fj := fileJSON(c.File)
		if c.Removed {
			fj = FileJSON{ID: strconv.FormatInt(c.File.ID, 10), Source: src, Path: c.File.Path, Name: path.Base(c.File.Path), IsDir: c.File.IsDir}
		}
		out = append(out, ChangeJSON{Seq: c.Seq, Removed: c.Removed, File: fj})
		next = c.Seq
	}
	if !more && len(chs) == 0 && cursor == 0 {
		// Empty source: hand out the current position so the client does not
		// re-list from 0 forever.
		if latest, err := s.Engine.Catalog.LatestSeq(r.Context()); err == nil {
			next = latest
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"changes": out, "cursor": strconv.FormatInt(next, 10), "more": more})
}

func (s *Server) createFolder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Source string `json:"source"`
		Path   string `json:"path"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	f, err := s.Engine.MkDir(r.Context(), req.Source, req.Path)
	if s.writeDriveError(w, err) {
		return
	}
	writeJSON(w, http.StatusCreated, fileJSON(f))
}

func (s *Server) moveFile(w http.ResponseWriter, r *http.Request) {
	f, ok := s.fileByPathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil || req.Path == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "path is required")
		return
	}
	got, err := s.Engine.MoveItem(r.Context(), f.ID, req.Path)
	if s.writeDriveError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, fileJSON(got))
}

func (s *Server) deleteFile(w http.ResponseWriter, r *http.Request) {
	f, ok := s.fileByPathID(w, r)
	if !ok {
		return
	}
	if s.writeDriveError(w, s.Engine.DeleteItem(r.Context(), f.ID)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeDriveError writes the response for an error from a drive operation
// and reports whether there was one.
func (s *Server) writeDriveError(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, engine.ErrReadOnly):
		writeError(w, http.StatusForbidden, "read_only", err.Error())
	case errors.Is(err, engine.ErrUnknownSource):
		writeError(w, http.StatusBadRequest, "bad_request", "unknown source")
	case errors.Is(err, store.ErrExists), errors.Is(err, catalog.ErrExists):
		writeError(w, http.StatusConflict, "exists", "an item with that name already exists")
	case errors.Is(err, store.ErrInvalidPath):
		writeError(w, http.StatusBadRequest, "bad_request", "invalid path")
	case errors.Is(err, catalog.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "file not found")
	default:
		s.internalError(w, err)
	}
	return true
}

// driveUploadTarget decides which item and path an upload to a drive source
// writes. With fileId it adds a new version of that file, unless the file
// changed since baseSha256, in which case the upload is kept as a conflicted
// copy next to it. Without fileId it creates a new file at path, and fails
// with 409 if the path is taken by another file.
//
// Item IDs are derived from the request, so creating the same upload again
// (an app restart) resumes the same session, and repeating a finished upload
// reports "exists" instead of making a copy.
func (s *Server) driveUploadTarget(r *http.Request, src, p, fileID, baseSHA, sha string) (itemID, target string, status int, code, msg string) {
	ctx := r.Context()
	dev := PrincipalFrom(ctx).DeviceID
	if fileID == "" {
		itemID = derivedItemID("new", dev, src, p, sha)
		if f, err := s.Engine.Catalog.LiveFileByPath(ctx, src, p); err == nil && f.SourceItemID != itemID {
			return "", "", http.StatusConflict, "exists", "a file already exists at that path; pass its fileId to replace it"
		}
		return itemID, p, 0, "", ""
	}
	id, err := strconv.ParseInt(fileID, 10, 64)
	if err != nil {
		return "", "", http.StatusNotFound, "not_found", "file not found"
	}
	f, err := s.Engine.Catalog.GetFile(ctx, id)
	if err != nil || f.Source != src || f.IsDir {
		return "", "", http.StatusNotFound, "not_found", "file not found"
	}
	if f.DeletedAt == nil && (baseSHA == "" || baseSHA == f.SHA256 || sha == f.SHA256) {
		return f.SourceItemID, f.Path, 0, "", ""
	}
	return derivedItemID("conflict", dev, fileID, baseSHA, sha), conflictName(f.Path, deviceLabel(r), time.Now()), 0, "", ""
}

// derivedItemID returns a stable "local:" item ID for the given parts.
func derivedItemID(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "local:" + hex.EncodeToString(h[:12])
}

func deviceLabel(r *http.Request) string {
	if id := PrincipalFrom(r.Context()).DeviceID; id != "" {
		return "device " + id[:min(6, len(id))]
	}
	return "admin"
}

// conflictName returns "dir/name (conflict from X 2026-11-13 0512).ext".
func conflictName(p, from string, t time.Time) string {
	dir, name := path.Split(p)
	ext := path.Ext(name)
	if ext == name {
		ext = ""
	}
	return dir + strings.TrimSuffix(name, ext) + fmt.Sprintf(" (conflict from %s %s)", from, t.Format("2006-01-02 1504")) + ext
}
