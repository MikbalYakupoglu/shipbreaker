package api

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed dist
var staticFiles embed.FS

// staticHandler serves the embedded SPA. Unknown routes fall back to index.html (SPA routing).
func staticHandler() http.Handler {
	sub, err := fs.Sub(staticFiles, "dist")
	if err != nil {
		panic("embed dist: " + err.Error())
	}

	fileServer := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/" || path == "" {
			serveIndex(w, sub)
			return
		}

		// Try exact file
		f, err := sub.Open(path[1:]) // strip leading "/"
		if err != nil {
			// SPA fallback — let the frontend router handle it
			serveIndex(w, sub)
			return
		}
		defer f.Close()

		// Hashed assets (Vite puts them under /assets/) are immutable — cache forever.
		// Everything else gets no-cache so deploys are picked up immediately.
		if strings.HasPrefix(path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}

		fileServer.ServeHTTP(w, r)
	})
}

func serveIndex(w http.ResponseWriter, sub fs.FS) {
	data, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}
