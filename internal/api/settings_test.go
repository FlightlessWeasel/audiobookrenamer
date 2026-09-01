package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"audiobookrenamer/internal/metadata"
)

// getSettings must not swallow a real DB read failure behind silently-wrong
// defaults: a stored value that can't be unmarshalled is surfaced as a 500,
// not treated as "unset -> use default".
func TestGetSettings_PropagatesReadError(t *testing.T) {
	s := newTestServer(t)

	// Store JSON that is valid on its own but is the wrong shape for
	// AudibleConfig, so GetSetting returns a non-"not found" error.
	if _, err := s.DB.Exec(
		`INSERT INTO settings (key, value) VALUES (?, ?)`,
		metadata.KeyAudible, `"not an object"`,
	); err != nil {
		t.Fatalf("seed corrupt setting: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	rr := httptest.NewRecorder()
	s.getSettings(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body: %s)", rr.Code, rr.Body.String())
	}
}

func patchSettings(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/api/settings", strings.NewReader(body))
	rr := httptest.NewRecorder()
	s.patchSettings(rr, req)
	return rr
}

func storedSetting(t *testing.T, s *Server, key string) string {
	t.Helper()
	var v string
	if err := s.DB.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v); err != nil {
		t.Fatalf("read stored setting %q: %v", key, err)
	}
	return v
}

// A plain enable/disable toggle (no api_key in the body) must not wipe the
// stored Google Books API key: the read-modify-write happens inside the
// committing transaction and an empty in.APIKey leaves the stored key intact.
func TestPatchSettings_GoogleToggleKeepsAPIKey(t *testing.T) {
	s := newTestServer(t)
	if _, err := s.DB.Exec(
		`INSERT INTO settings (key, value) VALUES (?, ?)`,
		metadata.KeyGoogleBooks, `{"enabled":true,"api_key":"K"}`,
	); err != nil {
		t.Fatalf("seed google_books: %v", err)
	}

	rr := patchSettings(t, s, `{"google_books":{"enabled":false}}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}

	var g metadata.GoogleConfig
	if _, err := s.DB.GetSetting(metadata.KeyGoogleBooks, &g); err != nil {
		t.Fatal(err)
	}
	if g.APIKey != "K" {
		t.Errorf("api_key = %q, want %q (toggle wiped the stored key)", g.APIKey, "K")
	}
	if g.Enabled {
		t.Errorf("enabled = true, want false")
	}
}

// A corrupt stored google_books row must make the PATCH fail (500) with the row
// left untouched, rather than the unmarshal error being dropped and a
// zero-value config (empty api_key) overwriting the stored one.
func TestPatchSettings_GoogleCorruptRowFailsAndPreserves(t *testing.T) {
	s := newTestServer(t)
	const corrupt = `{"enabled":true,"api_key":` // truncated: invalid JSON
	if _, err := s.DB.Exec(
		`INSERT INTO settings (key, value) VALUES (?, ?)`,
		metadata.KeyGoogleBooks, corrupt,
	); err != nil {
		t.Fatalf("seed corrupt google_books: %v", err)
	}

	rr := patchSettings(t, s, `{"google_books":{"enabled":false}}`)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body: %s)", rr.Code, rr.Body.String())
	}
	if got := storedSetting(t, s, metadata.KeyGoogleBooks); got != corrupt {
		t.Errorf("stored row changed despite failed PATCH: %q -> %q", corrupt, got)
	}
}
