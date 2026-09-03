// Package webui embeds the built React SPA (internal/webui/dist) and serves it
// with a history-API fallback so client-side routes resolve to index.html.
//
// Only dist/.gitkeep is committed; it is enough for `//go:embed all:dist` to
// compile before a frontend build has run, so `go build` and `go test` work on
// a bare checkout. `make build` (Vite) writes the real index.html + assets/.
// When they are absent the handler below simply serves the SPA shell path and
// the file server returns 404 — expected when the frontend has not been built.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// Handler returns an http.Handler that serves the embedded SPA. Requests for
// existing files are served directly; anything else falls back to index.html.
func Handler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(sub, p); err != nil {
			// Not a real asset — serve the SPA shell for client routing.
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		if strings.HasPrefix(p, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		fileServer.ServeHTTP(w, r)
	})
}
