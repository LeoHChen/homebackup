package api

import (
	"errors"
	"net/http"
	"os"
	"strconv"

	"github.com/LeoHChen/homebackup/internal/thumbs"
)

// fileThumbnail serves GET /files/{id}/thumbnail?size=small|large as JPEG.
// Thumbnails are cached by content, so the ETag is stable across renames.
func (s *Server) fileThumbnail(w http.ResponseWriter, r *http.Request) {
	if s.Thumbs == nil {
		writeError(w, http.StatusNotFound, "not_found", "thumbnails are not enabled")
		return
	}
	f, ok := s.fileByPathID(w, r)
	if !ok {
		return
	}
	size := thumbs.Small
	switch r.URL.Query().Get("size") {
	case "", "small":
	case "large":
		size = thumbs.Large
	default:
		writeError(w, http.StatusBadRequest, "bad_request", "size must be small or large")
		return
	}
	if f.IsDir {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported", "folders have no thumbnail")
		return
	}
	src := f.TrashPath
	if f.DeletedAt == nil {
		var err error
		if src, err = s.Engine.Store.DataPath(f.Source, f.Path); err != nil {
			s.internalError(w, err)
			return
		}
	}
	p, err := s.Thumbs.Get(r.Context(), src, f.Name(), f.SHA256, size)
	switch {
	case errors.Is(err, thumbs.ErrUnsupported):
		writeError(w, http.StatusUnsupportedMediaType, "unsupported", "no thumbnail for this file type")
		return
	case errors.Is(err, os.ErrNotExist) || src == "":
		writeError(w, http.StatusNotFound, "not_found", "content missing on disk")
		return
	case r.Context().Err() != nil:
		return // client went away
	case err != nil:
		s.internalError(w, err)
		return
	}
	fh, err := os.Open(p)
	if err != nil {
		s.internalError(w, err)
		return
	}
	defer fh.Close()
	fi, err := fh.Stat()
	if err != nil {
		s.internalError(w, err)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	if f.SHA256 != "" {
		w.Header().Set("ETag", `"`+f.SHA256+"-"+strconv.Itoa(size)+`"`)
	}
	http.ServeContent(w, r, "", fi.ModTime(), fh)
}
