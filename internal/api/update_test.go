package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"audiobookrenamer/internal/model"
	"audiobookrenamer/internal/selfupdate"
)

// hostRewrite redirects every request to a test server, keeping the path so the
// fake can route on it. Lets the Updater keep its hardcoded api.github.com URL.
type hostRewrite struct{ scheme, host string }

func (rt *hostRewrite) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme = rt.scheme
	r.URL.Host = rt.host
	r.Host = ""
	return http.DefaultTransport.RoundTrip(r)
}

func redirectUpdater(t *testing.T, s *Server, h http.HandlerFunc) {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	base, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("parse test server url: %v", err)
	}
	s.Updater = selfupdate.New(testVersion, selfupdate.WithHTTPClient(
		&http.Client{Transport: &hostRewrite{scheme: base.Scheme, host: base.Host}},
	))
}

func TestHealthz_IncludesVersion(t *testing.T) {
	s := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/healthz", nil)
	rr := httptest.NewRecorder()
	s.healthz(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var got healthResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Version != testVersion {
		t.Fatalf("version = %q, want %q", got.Version, testVersion)
	}
}

func TestGetUpdate_Shape(t *testing.T) {
	s := newTestServer(t)
	redirectUpdater(t, s, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/releases/latest") {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name":     "v1.2.3",
			"body":         "the release notes",
			"html_url":     "https://example.test/releases/v1.2.3",
			"published_at": time.Now().UTC(),
		})
	})

	req := httptest.NewRequest(http.MethodGet, "/api/update", nil)
	rr := httptest.NewRecorder()
	s.getUpdate(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rr.Code, rr.Body)
	}
	var got updateStatus
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Current != testVersion {
		t.Errorf("current = %q, want %q", got.Current, testVersion)
	}
	if got.Latest != "v1.2.3" {
		t.Errorf("latest = %q, want v1.2.3", got.Latest)
	}
	if !got.HasUpdate {
		t.Errorf("has_update = false, want true (v1.2.3 > %s)", testVersion)
	}
	if got.Notes != "the release notes" {
		t.Errorf("notes = %q", got.Notes)
	}
	if got.URL != "https://example.test/releases/v1.2.3" {
		t.Errorf("url = %q", got.URL)
	}
	if _, err := time.Parse(time.RFC3339, got.CheckedAt); err != nil {
		t.Errorf("checked_at = %q is not RFC3339: %v", got.CheckedAt, err)
	}
}

func TestGetUpdate_UpstreamFailureIs200(t *testing.T) {
	s := newTestServer(t)
	redirectUpdater(t, s, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/update", nil)
	rr := httptest.NewRecorder()
	s.getUpdate(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 on upstream failure", rr.Code)
	}
	var got updateStatus
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.HasUpdate {
		t.Errorf("has_update = true, want false when the check failed")
	}
	if !strings.Contains(got.Reason, "could not check for updates") {
		t.Errorf("reason = %q, want it to describe the fetch failure", got.Reason)
	}
}

func TestApplyUpdate_RejectsDowngrade(t *testing.T) {
	s := newTestServer(t)
	redirectUpdater(t, s, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/releases/latest") {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name":     "v0.1.0", // older than testVersion (v1.0.0)
			"html_url":     "https://example.test/releases/v0.1.0",
			"published_at": time.Now().UTC(),
		})
	})

	req := httptest.NewRequest(http.MethodPost, "/api/update/apply", nil)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.applyUpdate(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 when no newer version exists (body: %s)", rr.Code, rr.Body)
	}
}

func TestApplyUpdate_RejectsNonJSONContentType(t *testing.T) {
	s := newTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/update/apply", strings.NewReader("version=v9.9.9"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	s.applyUpdate(rr, req)

	if rr.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415 for a non-JSON Content-Type (body: %s)", rr.Code, rr.Body)
	}
}

func TestApplyUpdate_RejectsMissingContentType(t *testing.T) {
	s := newTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/update/apply", nil)
	rr := httptest.NewRecorder()
	s.applyUpdate(rr, req)

	if rr.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415 when Content-Type is absent (body: %s)", rr.Code, rr.Body)
	}
}

func TestApplyUpdate_RejectsCrossSite(t *testing.T) {
	s := newTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/update/apply", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rr := httptest.NewRecorder()
	s.applyUpdate(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a cross-site Sec-Fetch-Site (body: %s)", rr.Code, rr.Body)
	}
}

func TestApplyUpdate_AllowsSameOriginSecFetchSite(t *testing.T) {
	s := newTestServer(t)
	redirectUpdater(t, s, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r) // fail the release check, but only after the CSRF guard has passed
	})

	req := httptest.NewRequest(http.MethodPost, "/api/update/apply", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	rr := httptest.NewRecorder()
	s.applyUpdate(rr, req)

	if rr.Code == http.StatusUnsupportedMediaType || rr.Code == http.StatusForbidden {
		t.Fatalf("same-origin request was blocked by the CSRF guard: %d (body: %s)", rr.Code, rr.Body)
	}
}

func TestApplyUpdate_ConflictWhenUpdateJobActive(t *testing.T) {
	s := serverWithWorker(t)
	if _, err := s.DB.CreateJobPayload(model.JobSelfUpdate, "", `{"version":"v9.9.9"}`); err != nil {
		t.Fatalf("seed active job: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/update/apply", nil)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.applyUpdate(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 when a self-update job is already active (body: %s)", rr.Code, rr.Body)
	}
	if !strings.Contains(rr.Body.String(), "already in progress") {
		t.Fatalf("body = %s, want it to mention an update already in progress", rr.Body)
	}
}
