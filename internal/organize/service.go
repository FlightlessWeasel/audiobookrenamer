package organize

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"audiobookrenamer/internal/db"
	"audiobookrenamer/internal/model"
	"audiobookrenamer/internal/worker"
)

// Service wires the organize/undo job handlers to the worker.
//
// Organize-apply and undo both mutate the library tree and the rename journal.
// Running two of them at once would interleave doMove/journal writes and
// corrupt the tree, so the Service funnels every apply and every undo through a
// single gate: at most one organize-or-undo critical section runs at a time,
// process-wide. Planning happens inside the gate too, so a concurrent undo
// can't move files out from under a plan between BuildPlan and Execute.
type Service struct {
	db  *db.DB
	sem chan struct{} // capacity 1: the organize/undo gate

	// testHook, when set, runs while the gate is held. Tests use it to prove
	// two apply/undo runs never overlap.
	testHook func()
}

// NewService returns a Service.
func NewService(database *db.DB) *Service {
	return &Service{db: database, sem: make(chan struct{}, 1)}
}

// Register binds the organize and undo job types.
func Register(wm *worker.Manager, s *Service) {
	wm.Register(model.JobOrganize, s.runOrganize)
	wm.Register(model.JobUndo, s.runUndo)
}

// OrganizePayload is the JSON attached to a JobOrganize.
type OrganizePayload struct {
	BookIDs []string `json:"book_ids"`
}

// UndoPayload is the JSON attached to a JobUndo.
type UndoPayload struct {
	TargetJobID string `json:"target_job_id"`
}

// enter acquires the organize/undo gate, or returns ctx.Err() if the job is
// canceled while waiting.
func (s *Service) enter(ctx context.Context) error {
	select {
	case s.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	if s.testHook != nil {
		s.testHook()
	}
	return nil
}

func (s *Service) leave() { <-s.sem }

func (s *Service) runOrganize(ctx context.Context, job model.Job, p *worker.Progress) error {
	var payload OrganizePayload
	if job.Payload != "" {
		if err := json.Unmarshal([]byte(job.Payload), &payload); err != nil {
			return fmt.Errorf("bad payload: %w", err)
		}
	}
	return s.organize(ctx, job.ID, job.LibraryID, payload.BookIDs, p.Set)
}

// organize plans and applies a rename run while holding the organize/undo gate.
func (s *Service) organize(ctx context.Context, jobID, libraryID string, bookIDs []string, progress ProgressFunc) error {
	if err := s.enter(ctx); err != nil {
		return err
	}
	defer s.leave()

	plan, err := BuildPlan(s.db, libraryID, bookIDs)
	if err != nil {
		return err
	}
	if !plan.HasWork() {
		if progress != nil {
			progress(1, 1, "nothing to organize")
		}
		return nil
	}
	return Execute(ctx, s.db, jobID, plan, progress)
}

func (s *Service) runUndo(ctx context.Context, job model.Job, p *worker.Progress) error {
	var payload UndoPayload
	if err := json.Unmarshal([]byte(job.Payload), &payload); err != nil {
		return fmt.Errorf("bad payload: %w", err)
	}
	if payload.TargetJobID == "" {
		return fmt.Errorf("target_job_id is required")
	}
	p.Set(0, 1, "reverting "+payload.TargetJobID)

	retained, err := s.undo(ctx, payload.TargetJobID)
	if err != nil {
		return err
	}
	if len(retained) > 0 {
		p.Set(1, 1, fmt.Sprintf("reverted; kept %d non-empty folder(s): %s",
			len(retained), strings.Join(retained, "; ")))
		return nil
	}
	p.Set(1, 1, "reverted")
	return nil
}

// undo reverses a completed organize job while holding the organize/undo gate,
// returning any folders that were retained because the user had added files to
// them.
func (s *Service) undo(ctx context.Context, targetJobID string) ([]string, error) {
	if err := s.enter(ctx); err != nil {
		return nil, err
	}
	defer s.leave()
	return Undo(ctx, s.db, targetJobID)
}
