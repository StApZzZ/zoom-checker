package httpapi

import (
	"bytes"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// spaHandler serves the embedded single-page app: real files when they exist,
// index.html for every other path so client-side routing works.
func spaHandler(dist fs.FS) http.Handler {
	index, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		index = []byte(`<!doctype html><html lang="ru"><meta charset="utf-8"><title>Zoom Checker</title>` +
			`<body style="font-family:sans-serif;padding:2rem"><h1>Интерфейс не собран</h1>` +
			`<p>Соберите фронтенд: <code>make build</code> (или <code>cd web &amp;&amp; npm ci &amp;&amp; npm run build</code>) и пересоберите бинарник.</p>` +
			`<p>API работает: <a href="/healthz">/healthz</a></p></body></html>`)
	}
	fileServer := http.FileServerFS(dist)
	modTime := time.Now()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p != "" && p != "index.html" {
			if st, err := fs.Stat(dist, p); err == nil && !st.IsDir() {
				if strings.HasPrefix(p, "assets/") {
					// Vite emits content-hashed file names under assets/.
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				} else {
					w.Header().Set("Cache-Control", "public, max-age=3600")
				}
				fileServer.ServeHTTP(w, r)
				return
			}
			if strings.HasPrefix(p, "assets/") || path.Ext(p) != "" {
				http.NotFound(w, r)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		http.ServeContent(w, r, "index.html", modTime, bytes.NewReader(index))
	})
}
