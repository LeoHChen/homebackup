// Package web serves the browser dashboard: a single page that talks to the
// API with the same device tokens as the apps.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed static
var static embed.FS

// Handler serves the dashboard at "/", its assets under /static/, and 404
// for everything else (API paths are matched before it).
func Handler() http.Handler {
	sub, err := fs.Sub(static, "static")
	if err != nil {
		panic(err)
	}
	files := http.StripPrefix("/static/", http.FileServer(http.FS(sub)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/":
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFileFS(w, r, sub, "index.html")
		case r.URL.Path == "/favicon.ico":
			w.Header().Set("Content-Type", "image/svg+xml")
			http.ServeFileFS(w, r, sub, "icon.svg")
		case strings.HasPrefix(r.URL.Path, "/static/"):
			w.Header().Set("Cache-Control", "no-cache")
			files.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}
