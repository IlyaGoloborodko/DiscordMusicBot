package adminui

import (
	"embed"
	"io/fs"
	"net/http"
)

// staticFiles is the panel itself: hand-written HTML, CSS and JavaScript with no
// build step.
//
// No bundler on purpose. Images are built on the server — one core, 2GB, sharing
// the box with Postgres, Redis and Vosk — and a Vite build there is slow enough
// to risk being killed for memory. A panel of a few screens does not need a
// framework, and embedding the files means the container is a single binary with
// nothing to mount.
//
//go:embed static
var staticFiles embed.FS

func (g *Gateway) staticHandler() http.Handler {
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		// Only reachable if the embed directive and the directory disagree,
		// which is a build-time mistake, not a runtime condition.
		panic("adminui: embedded static files are missing: " + err.Error())
	}
	files := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The panel is a single page; unknown paths fall back to it rather than
		// 404ing, so a bookmarked or reloaded view still loads.
		if r.URL.Path != "/" {
			if _, err := fs.Stat(sub, r.URL.Path[1:]); err != nil {
				r = r.Clone(r.Context())
				r.URL.Path = "/"
			}
		}
		// The panel shows other people's voice transcripts and playback history.
		// Nothing here should sit in a shared cache, and a stale build is a
		// confusing thing to debug.
		w.Header().Set("Cache-Control", "no-store")
		files.ServeHTTP(w, r)
	})
}
