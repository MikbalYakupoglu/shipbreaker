package api

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed dist
var staticFiles embed.FS

// staticHandler serves the embedded SPA. Unknown routes fall back to index.html (SPA routing).
func staticHandler() http.Handler {
	sub, err := fs.Sub(staticFiles, "dist")
	if err != nil {
		panic("embed dist: " + err.Error())
	}

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

		http.FileServer(http.FS(sub)).ServeHTTP(w, r)
	})
}

func serveIndex(w http.ResponseWriter, sub fs.FS) {
	data, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}
