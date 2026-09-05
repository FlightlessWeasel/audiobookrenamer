package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"audiobookrenamer/internal/model"
	"audiobookrenamer/internal/selfupdate"
)

// updateStatus is the GET /api/update body.
type updateStatus struct {
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	HasUpdate bool   `json:"has_update"`
	Notes     string `json:"notes"`
	URL       string `json:"url"`
	CanApply  bool   `json:"can_apply"`
	Reason    string `json:"reason"`
	CheckedAt string `json:"checked_at"`
}

// getUpdate reports the running version, the latest published release, and
// whether an in-place update is available and possible on this install. A
// failing upstream fetch is not an error: it returns 200 with has_update false
// and the fetch failure in reason, so a transient GitHub outage does not break
// the page.
func (s *Server) getUpdate(w http.ResponseWriter, r *http.Request) {
	canApply, reason := s.Updater.CanApply()
	resp := updateStatus{
		Current:   s.Updater.CurrentVersion(),
		CanApply:  canApply,
		Reason:    reason,
		CheckedAt: time.Now().UTC().Format(time.RFC3339),
	}

	rel, err := s.Updater.LatestCached(r.Context())
	if err != nil {
		resp.Reason = "could not check for updates: " + err.Error()
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp.Latest = rel.Version
	resp.Notes = rel.Notes
	resp.URL = rel.URL
	resp.HasUpdate = s.Updater.HasUpdate(rel)
	writeJSON(w, http.StatusOK, resp)
}

type applyUpdateRequest struct {
	Version string `json:"version"`
}

// applyUpdate enqueues the self-update worker job. The optional body {"version":
// "vX.Y.Z"} pins a target; omitted means the latest release. It refuses (409)
// when this install cannot self-update, when a self-update job is already
// queued or running, when there is no newer version, or when a pinned version
// is not strictly newer than the running build (no downgrade/reinstall).
//
// It is a state-changing endpoint that accepts an empty body, so it carries its
// own CSRF guard (see requireNonCrossSite) rather than relying on a session
// cookie alone: a cross-origin form/no-cors fetch must not be able to trigger a
// self-update.
func (s *Server) applyUpdate(w http.ResponseWriter, r *http.Request) {
	if !requireNonCrossSite(w, r) {
		return
	}

	var req applyUpdateRequest
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "could not read request body")
		return
	}
	if len(bytes.TrimSpace(body)) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
	}

	if ok, reason := s.Updater.CanApply(); !ok {
		writeErr(w, http.StatusConflict, reason)
		return
	}

	if active, err := s.DB.ActiveJobExists(model.JobSelfUpdate); err != nil {
		writeDBErr(w, err)
		return
	} else if active {
		writeErr(w, http.StatusConflict, "an update is already in progress")
		return
	}

	rel, err := s.Updater.LatestCached(r.Context())
	if err != nil {
		writeErr(w, http.StatusConflict, "could not check for updates: "+err.Error())
		return
	}

	// Only the latest release has a resolvable set of assets to fetch, so a
	// pinned version must name it. That request still has to clear HasUpdate
	// below, which is what enforces "strictly newer than current".
	if req.Version != "" && selfupdate.NormalizeVersion(req.Version) != selfupdate.NormalizeVersion(rel.Version) {
		writeErr(w, http.StatusConflict, "only the latest release ("+rel.Version+") can be installed")
		return
	}
	if !s.Updater.HasUpdate(rel) {
		writeErr(w, http.StatusConflict, "already up to date")
		return
	}

	payload, err := json.Marshal(rel)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	job, err := s.Worker.EnqueuePayload(model.JobSelfUpdate, "", string(payload))
	if err != nil {
		writeDBErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

// requireNonCrossSite is a targeted CSRF guard for the self-update apply
// endpoint. It is deliberately local to this handler, not global middleware.
//
//   - Content-Type must be application/json. A cross-origin caller cannot set
//     that header without triggering a CORS preflight, which this server never
//     answers, so the real request is never sent.
//   - If the browser supplied Sec-Fetch-Site, it must be same-origin or
//     same-site; an explicit cross-site value is rejected.
//
// It writes the error response and returns false when the request is rejected.
func requireNonCrossSite(w http.ResponseWriter, r *http.Request) bool {
	ct := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	if ct != "application/json" {
		writeErr(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return false
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "same-site":
		return true
	default:
		writeErr(w, http.StatusForbidden, "cross-site request blocked")
		return false
	}
}
