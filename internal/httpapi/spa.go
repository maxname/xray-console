package httpapi

import (
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"
)

func itoa(n int) string { return strconv.Itoa(n) }

// spaHandler serves the built frontend. Any path that is not a file gets
// index.html: the routes (/servers, /chains, …) exist only in the browser.
func (s *Server) spaHandler() http.Handler {
	if s.ui == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "The web interface is not part of this build. Run `make web` and rebuild.",
				http.StatusNotFound)
		})
	}
	if _, err := fs.Stat(s.ui, "index.html"); err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "The web interface has not been built. Run `make web` and rebuild.",
				http.StatusNotFound)
		})
	}
	files := http.FileServerFS(s.ui)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name != "" {
			if info, err := fs.Stat(s.ui, name); err == nil && !info.IsDir() {
				if strings.HasPrefix(name, "assets/") {
					// Vite names these by content hash: a new build is a new URL.
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
			// A missing asset is a 404, not the app shell: answering a stale
			// /assets/index-abc.js with HTML breaks the page in a way that is
			// hard to read from the console.
			if strings.HasPrefix(name, "assets/") {
				http.NotFound(w, r)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, s.ui, "index.html")
	})
}
