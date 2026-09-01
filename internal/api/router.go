package api

import (
	"net/http"
	"time"

	"audiobookrenamer/internal/webui"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// Handler builds the full HTTP handler: JSON API under /api and the embedded
// SPA for everything else.
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	// A blanket request timeout would kill the long-lived SSE stream, so it is
	// applied to every path except that one.
	r.Use(timeoutExcept(60*time.Second, "/api/jobs/stream"))

	r.Route("/api", func(api chi.Router) {
		api.NotFound(func(w http.ResponseWriter, _ *http.Request) {
			writeErr(w, http.StatusNotFound, "no such endpoint")
		})
		api.Get("/healthz", s.healthz)

		api.Route("/auth", func(ar chi.Router) {
			ar.Get("/status", s.authStatus)
			ar.Post("/login", s.login)
			ar.Post("/logout", s.logout)
		})

		api.Group(func(pr chi.Router) {
			pr.Use(s.authMiddleware)

			pr.Route("/libraries", func(lr chi.Router) {
				lr.Get("/", s.listLibraries)
				lr.Post("/", s.createLibrary)
				lr.Route("/{id}", func(one chi.Router) {
					one.Get("/", s.getLibrary)
					one.Patch("/", s.updateLibrary)
					one.Delete("/", s.deleteLibrary)
					one.Post("/scan", s.scanLibrary)
					one.Post("/match", s.matchLibrary)
				})
			})

			pr.Get("/browse", s.browseDirs)

			pr.Post("/search", s.searchMetadata)

			pr.Route("/organize", func(or chi.Router) {
				or.Post("/preview", s.organizePreview)
				or.Post("/apply", s.organizeApply)
			})

			pr.Route("/books", func(br chi.Router) {
				br.Get("/", s.listBooks)
				br.Post("/accept-top", s.acceptTopCandidates)
				br.Route("/{id}", func(one chi.Router) {
					one.Get("/", s.getBook)
					one.Patch("/", s.patchBook)
					one.Get("/candidates", s.listCandidates)
					one.Post("/match", s.matchBook)
				})
			})

			pr.Route("/jobs", func(jr chi.Router) {
				jr.Get("/", s.listJobs)
				jr.Get("/stream", s.streamJobs)
				jr.Get("/{id}", s.getJob)
				jr.Post("/{id}/cancel", s.cancelJob)
				jr.Post("/{id}/undo", s.undoJob)
			})

			pr.Get("/settings", s.getSettings)
			pr.Patch("/settings", s.patchSettings)
		})
	})

	r.NotFound(webui.Handler().ServeHTTP)
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	})
	return r
}

// timeoutExcept applies middleware.Timeout(d) to every request whose path is
// not in skip. Streaming endpoints must be excluded or the timeout cancels
// them mid-stream.
func timeoutExcept(d time.Duration, skip ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		timed := middleware.Timeout(d)(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, p := range skip {
				if r.URL.Path == p {
					next.ServeHTTP(w, r)
					return
				}
			}
			timed.ServeHTTP(w, r)
		})
	}
}

type healthResponse struct {
	Status string `json:"status"`
	Time   string `json:"time"`
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok", Time: time.Now().UTC().Format(time.RFC3339)})
}
