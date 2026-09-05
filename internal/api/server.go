// Package api exposes the JSON HTTP API and serves the embedded SPA.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"

	"audiobookrenamer/internal/config"
	"audiobookrenamer/internal/db"
	"audiobookrenamer/internal/matcher"
	"audiobookrenamer/internal/selfupdate"
	"audiobookrenamer/internal/worker"
)

// Server bundles the dependencies shared by all handlers.
type Server struct {
	Cfg     config.Config
	DB      *db.DB
	Worker  *worker.Manager
	Matcher *matcher.Matcher

	// version is the running build's version string (from main, via ldflags).
	// Surfaced on /api/healthz and used by the self-update endpoints.
	version string
	// Updater backs the /api/update endpoints. Always constructed; CanApply
	// gates whether an update can actually be applied for this install.
	Updater *selfupdate.Updater

	// authSecret is the session-signing key, loaded/generated once at startup
	// so no request ever has to write it.
	authSecret string

	// shutdown is closed by Close to tell long-lived handlers (the SSE stream)
	// to return, so a graceful http.Server shutdown doesn't wait them out.
	shutdown     chan struct{}
	shutdownOnce sync.Once
}

// New returns a Server, loading (or generating and persisting) the auth signing
// secret up front. version is the running build's version string, surfaced on
// /api/healthz and driving the self-update endpoints.
func New(cfg config.Config, database *db.DB, wm *worker.Manager, mm *matcher.Matcher, version string) (*Server, error) {
	secret, err := loadOrCreateAuthSecret(database)
	if err != nil {
		return nil, fmt.Errorf("initialize auth secret: %w", err)
	}
	return &Server{
		Cfg: cfg, DB: database, Worker: wm, Matcher: mm,
		version:    version,
		Updater:    selfupdate.New(version),
		authSecret: secret,
		shutdown:   make(chan struct{}),
	}, nil
}

// Close signals long-lived handlers to wind down. Safe to call more than once.
func (s *Server) Close() {
	s.shutdownOnce.Do(func() { close(s.shutdown) })
}

// AuthEnabled reports whether API requests currently require authentication.
// main uses it at startup to warn about unauthenticated exposure on a
// non-loopback bind address.
func (s *Server) AuthEnabled() bool { return s.authSettings().Enabled }

const maxBodyBytes = 1 << 20 // 1 MiB

func (s *Server) decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			writeErr(w, http.StatusBadRequest, "empty request body")
			return false
		}
		writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	// Reject anything after the first JSON value. Decoding into a throwaway
	// must yield io.EOF; any other outcome means trailing tokens/garbage.
	if err := dec.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "request body must contain a single JSON value")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if v == nil {
		w.WriteHeader(status)
		return
	}
	// Marshal before writing the status line so an encode failure can still
	// become a 500 instead of a truncated 200 body.
	b, err := json.Marshal(v)
	if err != nil {
		slog.Error("encode response", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal error"}`))
		return
	}
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

type errBody struct {
	Error string `json:"error"`
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errBody{Error: msg})
}

// writeDBErr maps common db errors to HTTP status codes.
func writeDBErr(w http.ResponseWriter, err error) {
	if errors.Is(err, db.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	slog.Error("db error", "err", err)
	writeErr(w, http.StatusInternalServerError, "internal error")
}
