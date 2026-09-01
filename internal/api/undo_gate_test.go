package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"audiobookrenamer/internal/config"
	"audiobookrenamer/internal/db"
	"audiobookrenamer/internal/model"
	"audiobookrenamer/internal/worker"

	"github.com/go-chi/chi/v5"
)

// serverWithWorker is newTestServer plus a real worker, for the handlers that
// enqueue jobs.
func serverWithWorker(t *testing.T) *Server {
	t.Helper()
	d, err := db.Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	wm := worker.New(d, 1)
	t.Cleanup(wm.Shutdown)
	s, err := New(config.Config{}, d, wm, nil)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

func undoRequest(t *testing.T, s *Server, jobID string) *httptest.ResponseRecorder {
	t.Helper()
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", jobID)
	req := httptest.NewRequest(http.MethodPost, "/api/jobs/"+jobID+"/undo", nil).
		WithContext(context.WithValue(context.Background(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()
	s.undoJob(rr, req)
	return rr
}

// An organize run that failed partway has already applied some moves. Its
// executor rolls back what it can, but a rollback can itself fail — a
// permission error, a full disk — and leave files at their new paths. Undo is
// the retry for exactly those steps, so refusing it on a failed job strands a
// half-renamed library with no supported way back.
func TestUndoJob_AllowedForFailedAndCanceledOrganizeJobs(t *testing.T) {
	for _, status := range []model.JobStatus{model.JobDone, model.JobFailed, model.JobCanceled} {
		t.Run(string(status), func(t *testing.T) {
			s := serverWithWorker(t)
			job, err := s.DB.CreateJobPayload(model.JobOrganize, "lib", "")
			if err != nil {
				t.Fatal(err)
			}
			if err := s.DB.FinishJob(job.ID, status, ""); err != nil {
				t.Fatal(err)
			}

			rr := undoRequest(t, s, job.ID)
			if rr.Code != http.StatusAccepted {
				t.Fatalf("status = %d, want 202 (body: %s)", rr.Code, rr.Body.String())
			}
		})
	}
}

// A job that has not stopped yet is a different matter: undoing it would
// interleave with the run still applying moves.
func TestUndoJob_RefusedWhileStillRunning(t *testing.T) {
	for _, status := range []model.JobStatus{model.JobQueued, model.JobRunning} {
		t.Run(string(status), func(t *testing.T) {
			s := serverWithWorker(t)
			job, err := s.DB.CreateJobPayload(model.JobOrganize, "lib", "")
			if err != nil {
				t.Fatal(err)
			}
			if status == model.JobRunning {
				if err := s.DB.UpdateJobProgress(job.ID, model.JobRunning, 0, 0, ""); err != nil {
					t.Fatal(err)
				}
			}

			rr := undoRequest(t, s, job.ID)
			if rr.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409 (body: %s)", rr.Code, rr.Body.String())
			}
		})
	}
}
