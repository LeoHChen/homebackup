package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/LeoHChen/homebackup/internal/catalog"
	"github.com/LeoHChen/homebackup/internal/engine"
	"github.com/LeoHChen/homebackup/internal/store"
)

func (s *Server) fileRoutes(mux *http.ServeMux) {
	h := func(pattern string, fn http.HandlerFunc) { mux.Handle(pattern, s.auth(fn)) }
	h("GET /api/v1/sources", s.listSources)
	h("POST /api/v1/sources/{id}/sync", s.syncSource)
	h("GET /api/v1/files", s.listFiles)
	h("GET /api/v1/files/{id}", s.getFile)
	h("GET /api/v1/files/{id}/content", s.fileContent)
	h("GET /api/v1/files/{id}/versions", s.fileVersions)
	h("POST /api/v1/files/{id}/restore", s.restoreFile)
	h("GET /api/v1/trash", s.listTrash)
	h("GET /api/v1/changes", s.changes)
	h("POST /api/v1/folders", s.createFolder)
	h("PATCH /api/v1/files/{id}", s.moveFile)
	h("DELETE /api/v1/files/{id}", s.deleteFile)
	s.deviceRoutes(mux)
	s.uploadRoutes(mux)
}

// FileJSON is the API representation of a file or directory.
type FileJSON struct {
	ID        string     `json:"id,omitempty"`
	Source    string     `json:"source"`
	Path      string     `json:"path"`
	Name      string     `json:"name"`
	IsDir     bool       `json:"isDir"`
	Size      int64      `json:"size,omitempty"`
	Modified  *time.Time `json:"modified,omitempty"`
	SHA256    string     `json:"sha256,omitempty"`
	DeletedAt *time.Time `json:"deletedAt,omitempty"`
}

// VersionJSON is the API representation of a previous version.
type VersionJSON struct {
	ID       string    `json:"id"`
	Size     int64     `json:"size"`
	SHA256   string    `json:"sha256"`
	Modified time.Time `json:"modified"`
	Created  time.Time `json:"created"`
}

// SourceJSON is the API representation of a source.
type SourceJSON struct {
	ID        string     `json:"id"`
	Type      string     `json:"type"`
	Schedule  string     `json:"schedule,omitempty"`
	Running   bool       `json:"running"`
	LastSync  *time.Time `json:"lastSync,omitempty"`
	LastError string     `json:"lastError,omitempty"`
}

func fileJSON(f catalog.File) FileJSON {
	m := f.Modified
	return FileJSON{
		ID: strconv.FormatInt(f.ID, 10), Source: f.Source, Path: f.Path, Name: f.Name(),
		Size: f.Size, Modified: &m, SHA256: f.SHA256, DeletedAt: f.DeletedAt, IsDir: f.IsDir,
	}
}

func (s *Server) listSources(w http.ResponseWriter, r *http.Request) {
	infos, err := s.Engine.Sources(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	out := make([]SourceJSON, 0, len(infos))
	for _, in := range infos {
		sj := SourceJSON{ID: in.ID, Type: in.Type, Running: in.Running, LastSync: in.State.LastSync, LastError: in.State.LastError}
		if in.Schedule > 0 {
			sj.Schedule = in.Schedule.String()
		}
		out = append(out, sj)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) syncSource(w http.ResponseWriter, r *http.Request) {
	if err := s.Engine.Trigger(r.PathValue("id")); errors.Is(err, engine.ErrUnknownSource) {
		writeError(w, http.StatusNotFound, "not_found", "unknown source")
		return
	} else if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
}

func (s *Server) listFiles(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	src := q.Get("source")
	if src == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "source is required")
		return
	}
	dir := q.Get("path")
	entries, err := s.Engine.Catalog.ListDir(r.Context(), src, dir)
	if err != nil {
		s.internalError(w, err)
		return
	}
	items := make([]FileJSON, 0, len(entries))
	for _, e := range entries {
		if e.IsDir && e.File != nil {
			items = append(items, fileJSON(*e.File))
			continue
		}
		if e.IsDir {
			p := e.Name
			if dir != "" {
				p = dir + "/" + e.Name
			}
			items = append(items, FileJSON{Source: src, Path: p, Name: e.Name, IsDir: true})
			continue
		}
		items = append(items, fileJSON(*e.File))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) fileByPathID(w http.ResponseWriter, r *http.Request) (catalog.File, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "file not found")
		return catalog.File{}, false
	}
	f, err := s.Engine.Catalog.GetFile(r.Context(), id)
	if errors.Is(err, catalog.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "file not found")
		return catalog.File{}, false
	}
	if err != nil {
		s.internalError(w, err)
		return catalog.File{}, false
	}
	return f, true
}

func (s *Server) getFile(w http.ResponseWriter, r *http.Request) {
	if f, ok := s.fileByPathID(w, r); ok {
		writeJSON(w, http.StatusOK, fileJSON(f))
	}
}

func (s *Server) fileContent(w http.ResponseWriter, r *http.Request) {
	f, ok := s.fileByPathID(w, r)
	if !ok {
		return
	}
	var p string
	mod := f.Modified
	if vs := r.URL.Query().Get("version"); vs != "" {
		vid, err := strconv.ParseInt(vs, 10, 64)
		v, verr := s.Engine.Catalog.GetVersion(r.Context(), vid)
		if err != nil || errors.Is(verr, catalog.ErrNotFound) || (verr == nil && v.FileID != f.ID) {
			writeError(w, http.StatusNotFound, "not_found", "version not found")
			return
		} else if verr != nil {
			s.internalError(w, verr)
			return
		}
		p, mod = v.StoredPath, v.Modified
	} else if f.DeletedAt != nil {
		p = f.TrashPath
	} else {
		var err error
		if p, err = s.Engine.Store.DataPath(f.Source, f.Path); err != nil {
			s.internalError(w, err)
			return
		}
	}
	fh, err := os.Open(p)
	if errors.Is(err, os.ErrNotExist) || p == "" {
		writeError(w, http.StatusNotFound, "not_found", "content missing on disk")
		return
	} else if err != nil {
		s.internalError(w, err)
		return
	}
	defer fh.Close()
	w.Header().Set("Content-Disposition", contentDisposition(f.Name()))
	if f.SHA256 != "" && r.URL.Query().Get("version") == "" {
		w.Header().Set("ETag", `"`+f.SHA256+`"`)
	}
	http.ServeContent(w, r, f.Name(), mod, fh) // handles Range and If-* headers
}

func contentDisposition(name string) string {
	// RFC 6266 / 5987: ASCII fallback plus UTF-8 filename*.
	ascii := make([]rune, 0, len(name))
	for _, c := range name {
		if c < 0x20 || c > 0x7e || c == '"' || c == '\\' {
			c = '_'
		}
		ascii = append(ascii, c)
	}
	return `attachment; filename="` + string(ascii) + `"; filename*=UTF-8''` + urlPathEscape(name)
}

func urlPathEscape(s string) string {
	const hex = "0123456789ABCDEF"
	var b []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9') || c == '-' || c == '.' || c == '_' || c == '~' {
			b = append(b, c)
		} else {
			b = append(b, '%', hex[c>>4], hex[c&15])
		}
	}
	return string(b)
}

func (s *Server) fileVersions(w http.ResponseWriter, r *http.Request) {
	f, ok := s.fileByPathID(w, r)
	if !ok {
		return
	}
	vs, err := s.Engine.Catalog.ListVersions(r.Context(), f.ID)
	if err != nil {
		s.internalError(w, err)
		return
	}
	out := make([]VersionJSON, 0, len(vs))
	for _, v := range vs {
		out = append(out, VersionJSON{ID: strconv.FormatInt(v.ID, 10), Size: v.Size, SHA256: v.SHA256, Modified: v.Modified, Created: v.CreatedAt})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) restoreFile(w http.ResponseWriter, r *http.Request) {
	f, ok := s.fileByPathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Version string `json:"version"`
		Path    string `json:"path"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
			return
		}
	}
	var (
		got catalog.File
		err error
	)
	if req.Version != "" {
		vid, perr := strconv.ParseInt(req.Version, 10, 64)
		if perr != nil {
			writeError(w, http.StatusNotFound, "not_found", "version not found")
			return
		}
		got, err = s.Engine.RestoreVersion(r.Context(), f.ID, vid)
	} else {
		got, err = s.Engine.RestoreTrash(r.Context(), f.ID, req.Path)
	}
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, fileJSON(got))
	case errors.Is(err, catalog.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "version not found")
	case errors.Is(err, engine.ErrNotDeleted), errors.Is(err, engine.ErrNotInTrash), errors.Is(err, store.ErrExists):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, store.ErrInvalidPath):
		writeError(w, http.StatusBadRequest, "bad_request", "invalid path")
	default:
		s.internalError(w, err)
	}
}

func (s *Server) listTrash(w http.ResponseWriter, r *http.Request) {
	fs, err := s.Engine.Catalog.ListTrash(r.Context(), r.URL.Query().Get("source"))
	if err != nil {
		s.internalError(w, err)
		return
	}
	out := make([]FileJSON, 0, len(fs))
	for _, f := range fs {
		out = append(out, fileJSON(f))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) internalError(w http.ResponseWriter, err error) {
	if s.Log != nil {
		s.Log.Error("internal error", "err", err)
	}
	writeError(w, http.StatusInternalServerError, "internal", "internal error")
}
