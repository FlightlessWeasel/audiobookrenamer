package organize

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"audiobookrenamer/internal/model"
)

// Two organize/undo runs must never execute their filesystem + journal critical
// section at the same time. This drives real apply runs through the Service
// concurrently and asserts the gate serialized them.
func TestService_SerializesApplyAndUndo(t *testing.T) {
	d := openTestDB(t)
	root := t.TempDir()
	lib, err := d.CreateLibrary(model.Library{Name: "L", RootPath: root, StructureMode: model.AuthorFirst})
	if err != nil {
		t.Fatal(err)
	}

	b1 := matchedBook(t, d, lib, filepath.Join(root, "in1"), filepath.Join(root, "in1", "a.m4b"),
		model.LayoutSingle, []string{"a.m4b"}, model.Book{Title: "Book One", Author: "Author One", Year: 2020})
	b2 := matchedBook(t, d, lib, filepath.Join(root, "in2"), filepath.Join(root, "in2", "b.m4b"),
		model.LayoutSingle, []string{"b.m4b"}, model.Book{Title: "Book Two", Author: "Author Two", Year: 2021})

	svc := NewService(d, t.TempDir())

	var inFlight, maxInFlight int32
	svc.testHook = func() {
		n := atomic.AddInt32(&inFlight, 1)
		for {
			m := atomic.LoadInt32(&maxInFlight)
			if n <= m || atomic.CompareAndSwapInt32(&maxInFlight, m, n) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		atomic.AddInt32(&inFlight, -1)
	}

	job1, _ := d.CreateJobPayload(model.JobOrganize, lib.ID, "")
	job2, _ := d.CreateJobPayload(model.JobOrganize, lib.ID, "")

	var wg sync.WaitGroup
	wg.Add(2)
	errs := make([]error, 2)
	go func() {
		defer wg.Done()
		errs[0] = svc.organize(context.Background(), job1.ID, lib.ID, []string{b1.ID}, nil)
	}()
	go func() {
		defer wg.Done()
		errs[1] = svc.organize(context.Background(), job2.ID, lib.ID, []string{b2.ID}, nil)
	}()
	wg.Wait()

	for i, e := range errs {
		if e != nil {
			t.Fatalf("organize %d: %v", i, e)
		}
	}
	if maxInFlight != 1 {
		t.Fatalf("organize/undo critical sections overlapped: max concurrent = %d, want 1", maxInFlight)
	}
}

// enter must honor context cancellation while the gate is held by someone else,
// so a queued job that is canceled during shutdown doesn't block forever.
func TestService_EnterRespectsContextCancel(t *testing.T) {
	svc := NewService(nil, "")
	if err := svc.enter(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer svc.leave()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := svc.enter(ctx); err == nil {
		t.Fatal("enter should return the context error when the gate is contended and ctx is canceled")
	}
}

// A book that is already sitting at its correct organized path (because it was
// organized before, then rematched to metadata that renders the same path) has
// no file to move. The plan for it is therefore empty, but the book's DB state
// still needs to go from "matched" back to "organized" - the files are already
// exactly where an organize run would put them.
func TestService_Organize_NoOpMoveStillUpdatesState(t *testing.T) {
	d := openTestDB(t)
	root := t.TempDir()
	lib, err := d.CreateLibrary(model.Library{Name: "L", RootPath: root, StructureMode: model.AuthorFirst})
	if err != nil {
		t.Fatal(err)
	}

	b := matchedBook(t, d, lib,
		filepath.Join(root, "incoming"), filepath.Join(root, "incoming", "phm.m4b"),
		model.LayoutSingle, []string{"phm.m4b"},
		model.Book{Title: "Project Hail Mary", Author: "Andy Weir", Year: 2021},
	)

	svc := NewService(d, t.TempDir())
	job1, _ := d.CreateJobPayload(model.JobOrganize, lib.ID, "")
	if err := svc.organize(context.Background(), job1.ID, lib.ID, []string{b.ID}, nil); err != nil {
		t.Fatalf("first organize: %v", err)
	}
	organized, err := d.GetBook(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if organized.State != model.StateOrganized {
		t.Fatalf("after first organize: state = %s, want organized", organized.State)
	}

	// Rematch to the SAME metadata - as a user re-running match/accept would -
	// so the rendered path is identical and nothing needs to move. This is
	// exactly what SetBookMatch does; matcher.apply calls it the same way.
	rematched := organized
	rematched.State = model.StateMatched
	rematched.MatchedProvider, rematched.MatchedProviderID = "stub", "1"
	if err := d.SetBookMatch(b.ID, rematched); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.GetBookBare(b.ID); got.State != model.StateMatched {
		t.Fatalf("setup: state = %s, want matched", got.State)
	}

	job2, _ := d.CreateJobPayload(model.JobOrganize, lib.ID, "")
	if err := svc.organize(context.Background(), job2.ID, lib.ID, []string{b.ID}, nil); err != nil {
		t.Fatalf("second organize (no-op move): %v", err)
	}

	final, err := d.GetBookBare(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.State != model.StateOrganized {
		t.Fatalf("after re-organizing a book with no file to move: state = %s, want organized", final.State)
	}
}
