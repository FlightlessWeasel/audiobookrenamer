package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"audiobookrenamer/internal/model"
)

// POST /api/libraries/rescan-all enqueues one scan job per enabled library and
// skips disabled ones.
func TestRescanAll_EnqueuesEnabledOnly(t *testing.T) {
	s := serverWithWorker(t)

	on1, err := s.DB.CreateLibrary(model.Library{Name: "A", RootPath: t.TempDir(), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	on2, err := s.DB.CreateLibrary(model.Library{Name: "B", RootPath: t.TempDir(), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.CreateLibrary(model.Library{Name: "C", RootPath: t.TempDir(), Enabled: false}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/libraries/rescan-all", nil)
	rr := httptest.NewRecorder()
	s.rescanAllLibraries(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body: %s)", rr.Code, rr.Body.String())
	}
	var got rescanAllResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Jobs) != 2 {
		t.Fatalf("jobs = %d, want 2", len(got.Jobs))
	}
	seen := map[string]bool{}
	for _, j := range got.Jobs {
		if j.Type != model.JobScan {
			t.Errorf("job type = %q, want scan", j.Type)
		}
		seen[j.LibraryID] = true
	}
	if !seen[on1.ID] || !seen[on2.ID] {
		t.Fatalf("expected a job for each enabled library, got %+v", got.Jobs)
	}
}
