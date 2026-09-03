package organize

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"audiobookrenamer/internal/db"
	"audiobookrenamer/internal/model"
)

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// matchedBook creates a book on disk + in the DB, already matched.
func matchedBook(t *testing.T, d *db.DB, lib model.Library, srcDir, srcFile string, layout model.Layout, rels []string, meta model.Book) model.Book {
	t.Helper()
	for _, r := range rels {
		if srcFile != "" {
			writeFile(t, srcFile)
		} else {
			writeFile(t, filepath.Join(srcDir, filepath.FromSlash(r)))
		}
	}
	b := model.Book{
		LibraryID: lib.ID, SourceDir: srcDir, SourceFile: srcFile, Layout: layout,
		State: model.StateUnmatched,
	}
	saved, err := d.UpsertBook(b)
	if err != nil {
		t.Fatal(err)
	}
	files := make([]model.BookFile, len(rels))
	for i, r := range rels {
		files[i] = model.BookFile{RelPath: r, Ext: filepath.Ext(r), Track: i + 1}
	}
	if err := d.ReplaceBookFiles(saved.ID, files); err != nil {
		t.Fatal(err)
	}
	meta.ID = saved.ID
	meta.State = model.StateMatched
	if err := d.SetBookMatch(saved.ID, meta); err != nil {
		t.Fatal(err)
	}
	out, err := d.GetBook(saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestOrganize_RoundTripAuthorFirst(t *testing.T) {
	d := openTestDB(t)
	root := t.TempDir()
	lib, err := d.CreateLibrary(model.Library{Name: "L", RootPath: root, StructureMode: model.AuthorFirst})
	if err != nil {
		t.Fatal(err)
	}

	// A loose single file in a messy folder.
	single := matchedBook(t, d, lib,
		filepath.Join(root, "incoming"), filepath.Join(root, "incoming", "phm.m4b"),
		model.LayoutSingle, []string{"phm.m4b"},
		model.Book{Title: "Project Hail Mary", Author: "Andy Weir", Year: 2021},
	)

	// A multi-file book in its own folder, part of a series.
	multi := matchedBook(t, d, lib,
		filepath.Join(root, "Mistborn 1"), "",
		model.LayoutMulti, []string{"01.mp3", "02.mp3"},
		model.Book{Title: "The Final Empire", Author: "Brandon Sanderson", Series: "Mistborn", SeriesIndex: "1", Year: 2006},
	)

	plan, err := BuildPlan(d, lib.ID, []string{single.ID, multi.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Changed() {
		t.Fatal("expected a non-empty plan")
	}
	for _, bp := range plan.Books {
		if bp.Skip {
			t.Fatalf("book %s unexpectedly skipped: %s", bp.Title, bp.Reason)
		}
	}

	job, err := d.CreateJobPayload(model.JobOrganize, lib.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := Execute(context.Background(), d, job.ID, plan, nil); err != nil {
		t.Fatalf("execute: %v", err)
	}

	wantSingle := filepath.Join(root, "Andy Weir", "Project Hail Mary (2021)", "Project Hail Mary (2021) - Andy Weir.m4b")
	if _, err := os.Stat(wantSingle); err != nil {
		t.Fatalf("expected %s: %v", wantSingle, err)
	}
	wantMulti := filepath.Join(root, "Brandon Sanderson", "Mistborn", "The Final Empire (2006)", "The Final Empire (2006) - 02.mp3")
	if _, err := os.Stat(wantMulti); err != nil {
		t.Fatalf("expected %s: %v", wantMulti, err)
	}
	if _, err := os.Stat(filepath.Join(root, "incoming")); !os.IsNotExist(err) {
		t.Errorf("empty source dir 'incoming' should have been pruned")
	}

	gotSingle, _ := d.GetBook(single.ID)
	if gotSingle.State != model.StateOrganized {
		t.Errorf("single book state = %s, want organized", gotSingle.State)
	}
	if gotSingle.Files[0].RelPath != "Project Hail Mary (2021) - Andy Weir.m4b" {
		t.Errorf("single file rel_path not updated: %q", gotSingle.Files[0].RelPath)
	}

	// Undo restores everything.
	if _, err := Undo(context.Background(), d, job.ID); err != nil {
		t.Fatalf("undo: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "incoming", "phm.m4b")); err != nil {
		t.Errorf("undo should have restored the original file: %v", err)
	}
	if _, err := os.Stat(wantSingle); !os.IsNotExist(err) {
		t.Errorf("undo should have removed the organized file")
	}
	back, _ := d.GetBook(single.ID)
	if back.State != model.StateMatched {
		t.Errorf("after undo state = %s, want matched", back.State)
	}
	if back.SourceFile != filepath.Join(root, "incoming", "phm.m4b") {
		t.Errorf("after undo source_file = %q", back.SourceFile)
	}
}

// Undoing a re-organize of an already-organized book must restore it to
// "organized" — its real state before that run — not to "matched".
func TestUndo_RestoresPriorOrganizedState(t *testing.T) {
	d := openTestDB(t)
	root := t.TempDir()
	lib, err := d.CreateLibrary(model.Library{Name: "L", RootPath: root, StructureMode: model.AuthorFirst})
	if err != nil {
		t.Fatal(err)
	}

	src := filepath.Join(root, "incoming", "phm.m4b")
	book := matchedBook(t, d, lib, filepath.Join(root, "incoming"), src,
		model.LayoutSingle, []string{"phm.m4b"},
		model.Book{Title: "Project Hail Mary", Author: "Andy Weir", Year: 2021})

	// First organize: matched -> organized.
	plan1, err := BuildPlan(d, lib.ID, []string{book.ID})
	if err != nil {
		t.Fatal(err)
	}
	job1, _ := d.CreateJobPayload(model.JobOrganize, lib.ID, "")
	if err := Execute(context.Background(), d, job1.ID, plan1, nil); err != nil {
		t.Fatalf("first execute: %v", err)
	}
	if got, _ := d.GetBook(book.ID); got.State != model.StateOrganized {
		t.Fatalf("after first organize, state = %s, want organized", got.State)
	}

	// Retitle the accepted metadata so a second organize has real moves to make,
	// while the book stays in the organized state.
	if err := d.SetBookMatch(book.ID, model.Book{
		State: model.StateOrganized,
		Title: "Project Hail Mary (Deluxe)", Author: "Andy Weir", Year: 2021,
	}); err != nil {
		t.Fatal(err)
	}

	// Second organize: organized -> organized at a new path.
	plan2, err := BuildPlan(d, lib.ID, []string{book.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !plan2.Changed() {
		t.Fatal("expected the re-organize to have real moves")
	}
	job2, _ := d.CreateJobPayload(model.JobOrganize, lib.ID, "")
	if err := Execute(context.Background(), d, job2.ID, plan2, nil); err != nil {
		t.Fatalf("second execute: %v", err)
	}

	// Undo the re-organize.
	if _, err := Undo(context.Background(), d, job2.ID); err != nil {
		t.Fatalf("undo: %v", err)
	}
	back, err := d.GetBook(book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if back.State != model.StateOrganized {
		t.Fatalf("after undo, state = %s, want organized", back.State)
	}
}

func TestBuildPlan_RejectsBookFromAnotherLibrary(t *testing.T) {
	d := openTestDB(t)
	rootA, rootB := t.TempDir(), t.TempDir()
	libA, _ := d.CreateLibrary(model.Library{Name: "A", RootPath: rootA, StructureMode: model.AuthorFirst})
	libB, _ := d.CreateLibrary(model.Library{Name: "B", RootPath: rootB, StructureMode: model.AuthorFirst})

	book := matchedBook(t, d, libB, filepath.Join(rootB, "in"), filepath.Join(rootB, "in", "x.m4b"),
		model.LayoutSingle, []string{"x.m4b"}, model.Book{Title: "X", Author: "Y", Year: 2020})

	if _, err := BuildPlan(d, libA.ID, []string{book.ID}); !errors.Is(err, ErrBookNotInLibrary) {
		t.Fatalf("BuildPlan err = %v, want ErrBookNotInLibrary", err)
	}
	if err := ValidateBooks(d, libA.ID, []string{book.ID}); !errors.Is(err, ErrBookNotInLibrary) {
		t.Fatalf("ValidateBooks err = %v, want ErrBookNotInLibrary", err)
	}
}

func TestExecute_RefusesSymlinkedSource(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is restricted on Windows CI")
	}
	d := openTestDB(t)
	root := t.TempDir()
	lib, _ := d.CreateLibrary(model.Library{Name: "L", RootPath: root, StructureMode: model.AuthorFirst})

	outside := filepath.Join(t.TempDir(), "real.m4b")
	writeFile(t, outside)
	link := filepath.Join(root, "in", "book.m4b")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	b := model.Book{LibraryID: lib.ID, SourceDir: filepath.Join(root, "in"), SourceFile: link, Layout: model.LayoutSingle, State: model.StateUnmatched}
	saved, err := d.UpsertBook(b)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.ReplaceBookFiles(saved.ID, []model.BookFile{{RelPath: "book.m4b", Ext: ".m4b"}}); err != nil {
		t.Fatal(err)
	}
	meta := model.Book{ID: saved.ID, State: model.StateMatched, Title: "Sym", Author: "A", Year: 2020}
	if err := d.SetBookMatch(saved.ID, meta); err != nil {
		t.Fatal(err)
	}

	plan, err := BuildPlan(d, lib.ID, []string{saved.ID})
	if err != nil {
		t.Fatal(err)
	}
	// The planner skips the symlinked book rather than planning a move for it.
	for _, bp := range plan.Books {
		if bp.BookID == saved.ID && !bp.Skip {
			t.Fatal("expected the symlinked book to be skipped by the planner")
		}
	}
}

// When the atomic DB finalization fails after the filesystem moves, Execute
// must roll the moves back so disk and database stay consistent.
func TestExecute_RollsBackFilesystemWhenFinalizeFails(t *testing.T) {
	d := openTestDB(t)
	root := t.TempDir()
	lib, _ := d.CreateLibrary(model.Library{Name: "L", RootPath: root, StructureMode: model.AuthorFirst})

	src := filepath.Join(root, "incoming", "phm.m4b")
	book := matchedBook(t, d, lib, filepath.Join(root, "incoming"), src,
		model.LayoutSingle, []string{"phm.m4b"},
		model.Book{Title: "Project Hail Mary", Author: "Andy Weir", Year: 2021})

	plan, err := BuildPlan(d, lib.ID, []string{book.ID})
	if err != nil {
		t.Fatal(err)
	}
	job, _ := d.CreateJobPayload(model.JobOrganize, lib.ID, "")

	// Delete the book row so FinalizeOrganize's UPDATE ... WHERE id=? touches
	// zero rows and the whole finalization transaction fails.
	if err := d.DeleteBooks([]string{book.ID}); err != nil {
		t.Fatal(err)
	}

	if err := Execute(context.Background(), d, job.ID, plan, nil); err == nil {
		t.Fatal("expected Execute to fail when finalization can't complete")
	}

	if _, err := os.Stat(src); err != nil {
		t.Errorf("original file should have been rolled back into place: %v", err)
	}
	organized := filepath.Join(root, "Andy Weir", "Project Hail Mary (2021)", "Project Hail Mary (2021) - Andy Weir.m4b")
	if _, err := os.Stat(organized); !os.IsNotExist(err) {
		t.Errorf("organized file should not remain after rollback")
	}
}

// Undo reverses the filesystem before it touches the database: if a reverse
// move can't complete, the book rows must be left describing the organized
// layout (which is still what's on disk), not the original one.
func TestUndo_KeepsDBOrganizedWhenFilesystemReverseFails(t *testing.T) {
	d := openTestDB(t)
	root := t.TempDir()
	lib, _ := d.CreateLibrary(model.Library{Name: "L", RootPath: root, StructureMode: model.AuthorFirst})

	src := filepath.Join(root, "incoming", "phm.m4b")
	book := matchedBook(t, d, lib, filepath.Join(root, "incoming"), src,
		model.LayoutSingle, []string{"phm.m4b"},
		model.Book{Title: "Project Hail Mary", Author: "Andy Weir", Year: 2021})

	plan, err := BuildPlan(d, lib.ID, []string{book.ID})
	if err != nil {
		t.Fatal(err)
	}
	job, _ := d.CreateJobPayload(model.JobOrganize, lib.ID, "")
	if err := Execute(context.Background(), d, job.ID, plan, nil); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got, _ := d.GetBook(book.ID); got.State != model.StateOrganized {
		t.Fatalf("precondition: state = %s, want organized", got.State)
	}

	// Occupy the original path so the reverse move hits "destination already
	// exists" during undo pass 1.
	writeFile(t, src)

	if _, err := Undo(context.Background(), d, job.ID); err == nil {
		t.Fatal("expected Undo to fail when a reverse move is blocked")
	}

	got, err := d.GetBook(book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != model.StateOrganized {
		t.Fatalf("after failed undo, state = %s, want it left as organized", got.State)
	}
}

// A book reachable only through a symlinked parent that escapes the library
// root is skipped, not moved.
func TestBuildPlan_SkipsSymlinkedParentEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is restricted on Windows CI")
	}
	d := openTestDB(t)
	root := t.TempDir()
	lib, _ := d.CreateLibrary(model.Library{Name: "L", RootPath: root, StructureMode: model.AuthorFirst})

	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "esc")); err != nil {
		t.Fatal(err)
	}
	// Physically create the file under `outside`, addressed via the symlink.
	realFile := filepath.Join(outside, "book", "x.m4b")
	writeFile(t, realFile)

	viaLink := filepath.Join(root, "esc", "book")
	b := model.Book{LibraryID: lib.ID, SourceDir: viaLink, Layout: model.LayoutSingle, State: model.StateUnmatched}
	saved, err := d.UpsertBook(b)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.ReplaceBookFiles(saved.ID, []model.BookFile{{RelPath: "x.m4b", Ext: ".m4b"}}); err != nil {
		t.Fatal(err)
	}
	if err := d.SetBookMatch(saved.ID, model.Book{ID: saved.ID, State: model.StateMatched, Title: "Esc", Author: "A", Year: 2020}); err != nil {
		t.Fatal(err)
	}

	plan, err := BuildPlan(d, lib.ID, []string{saved.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, bp := range plan.Books {
		if bp.BookID == saved.ID && !bp.Skip {
			t.Fatal("book behind a symlinked-parent escape should be skipped")
		}
	}
}

// A pre-existing foreign file at a book's target path is detected at plan time,
// not left for the executor to abort on.
func TestBuildPlan_SkipsWhenTargetPathOccupied(t *testing.T) {
	d := openTestDB(t)
	root := t.TempDir()
	lib, _ := d.CreateLibrary(model.Library{Name: "L", RootPath: root, StructureMode: model.AuthorFirst})

	book := matchedBook(t, d, lib, filepath.Join(root, "incoming"), filepath.Join(root, "incoming", "phm.m4b"),
		model.LayoutSingle, []string{"phm.m4b"},
		model.Book{Title: "Project Hail Mary", Author: "Andy Weir", Year: 2021})

	// Drop an unrelated file exactly where the book would be renamed to.
	target := filepath.Join(root, "Andy Weir", "Project Hail Mary (2021)", "Project Hail Mary (2021) - Andy Weir.m4b")
	writeFile(t, target)

	plan, err := BuildPlan(d, lib.ID, []string{book.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, bp := range plan.Books {
		if bp.BookID == book.ID && !bp.Skip {
			t.Fatal("book whose target path is already occupied should be skipped")
		}
	}
}

// caseSensitiveFS reports whether dir is on a case-sensitive filesystem.
func caseSensitiveFS(t *testing.T, dir string) bool {
	t.Helper()
	probe := filepath.Join(dir, "abrCaseProbe")
	if err := os.WriteFile(probe, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(probe)
	_, err := os.Stat(filepath.Join(dir, "ABRCASEPROBE"))
	return os.IsNotExist(err)
}

// caseFixBook sets up a single-file book already sitting at its target layout
// except that a parent folder is lower-cased, so organizing it is a pure
// case-only rename. Returns the book plus its current (lower-cased) and
// expected (properly-cased) absolute file paths.
func caseFixBook(t *testing.T, d *db.DB, lib model.Library, root string) (model.Book, string, string) {
	t.Helper()
	name := "Project Hail Mary (2021) - Andy Weir.m4b"
	srcDir := filepath.Join(root, "andy weir", "Project Hail Mary (2021)")
	srcFile := filepath.Join(srcDir, name)
	book := matchedBook(t, d, lib, srcDir, srcFile, model.LayoutSingle, []string{name},
		model.Book{Title: "Project Hail Mary", Author: "Andy Weir", Year: 2021})
	want := filepath.Join(root, "Andy Weir", "Project Hail Mary (2021)", name)
	return book, srcFile, want
}

// A pure case-only rename is applied and then fully undone.
func TestOrganize_CaseFixRoundTrip(t *testing.T) {
	d := openTestDB(t)
	root := t.TempDir()
	lib, _ := d.CreateLibrary(model.Library{Name: "L", RootPath: root, StructureMode: model.AuthorFirst})

	book, src, want := caseFixBook(t, d, lib, root)

	plan, err := BuildPlan(d, lib.ID, []string{book.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Changed() {
		t.Fatal("expected a case-fix plan")
	}
	for _, bp := range plan.Books {
		if bp.Skip {
			t.Fatalf("book skipped: %s", bp.Reason)
		}
	}

	job, _ := d.CreateJobPayload(model.JobOrganize, lib.ID, "")
	if err := Execute(context.Background(), d, job.ID, plan, nil); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("case-fixed file missing: %v", err)
	}

	if _, err := Undo(context.Background(), d, job.ID); err != nil {
		t.Fatalf("undo: %v", err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("undo should have restored the original path: %v", err)
	}
}

// The case-fix second leg must not overwrite a foreign file that appeared at
// the destination after planning; Execute fails and rolls back.
func TestExecute_CaseFixRefusesToOverwriteDestination(t *testing.T) {
	d := openTestDB(t)
	root := t.TempDir()
	if !caseSensitiveFS(t, root) {
		t.Skip("needs a case-sensitive filesystem: on case-insensitive FS the case-fix source and destination are the same path")
	}
	lib, _ := d.CreateLibrary(model.Library{Name: "L", RootPath: root, StructureMode: model.AuthorFirst})

	book, src, want := caseFixBook(t, d, lib, root)

	plan, err := BuildPlan(d, lib.ID, []string{book.ID})
	if err != nil {
		t.Fatal(err)
	}

	// A different file lands exactly where the case-fix would move ours.
	if err := os.MkdirAll(filepath.Dir(want), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, []byte("FOREIGN"), 0o644); err != nil {
		t.Fatal(err)
	}

	job, _ := d.CreateJobPayload(model.JobOrganize, lib.ID, "")
	if err := Execute(context.Background(), d, job.ID, plan, nil); err == nil {
		t.Fatal("expected Execute to refuse overwriting the destination")
	}

	if _, err := os.Stat(src); err != nil {
		t.Errorf("original file should have been rolled back: %v", err)
	}
	got, err := os.ReadFile(want)
	if err != nil || string(got) != "FOREIGN" {
		t.Errorf("foreign file at destination was clobbered: %q, err=%v", got, err)
	}
}

// Undo is safe to run twice: the second call is a no-op that returns nil and
// leaves the filesystem alone.
func TestUndo_Idempotent(t *testing.T) {
	d := openTestDB(t)
	root := t.TempDir()
	lib, _ := d.CreateLibrary(model.Library{Name: "L", RootPath: root, StructureMode: model.AuthorFirst})

	src := filepath.Join(root, "incoming", "phm.m4b")
	book := matchedBook(t, d, lib, filepath.Join(root, "incoming"), src,
		model.LayoutSingle, []string{"phm.m4b"},
		model.Book{Title: "Project Hail Mary", Author: "Andy Weir", Year: 2021})

	plan, err := BuildPlan(d, lib.ID, []string{book.ID})
	if err != nil {
		t.Fatal(err)
	}
	job, _ := d.CreateJobPayload(model.JobOrganize, lib.ID, "")
	if err := Execute(context.Background(), d, job.ID, plan, nil); err != nil {
		t.Fatalf("execute: %v", err)
	}

	if _, err := Undo(context.Background(), d, job.ID); err != nil {
		t.Fatalf("first undo: %v", err)
	}
	fi1, err := os.Stat(src)
	if err != nil {
		t.Fatalf("after first undo, original missing: %v", err)
	}

	if _, err := Undo(context.Background(), d, job.ID); err != nil {
		t.Fatalf("second undo should be a no-op, got: %v", err)
	}
	fi2, err := os.Stat(src)
	if err != nil {
		t.Fatalf("after second undo, original missing: %v", err)
	}
	if !fi1.ModTime().Equal(fi2.ModTime()) {
		t.Errorf("second undo touched the file (mtime changed)")
	}
	back, _ := d.GetBook(book.ID)
	if back.State != model.StateMatched {
		t.Errorf("state after double undo = %s, want matched", back.State)
	}
}

// FinalizeOrganize must fail loudly (and roll its whole transaction back) when
// its file map names a row that isn't owned by the book the OrganizeFinal is
// for, rather than silently repointing nothing and leaving the database
// describing a layout that never landed on disk.
func TestFinalizeOrganize_RejectsCrossBookFileIDs(t *testing.T) {
	d := openTestDB(t)
	root := t.TempDir()
	lib, _ := d.CreateLibrary(model.Library{Name: "L", RootPath: root, StructureMode: model.AuthorFirst})

	a := matchedBook(t, d, lib, filepath.Join(root, "a"), filepath.Join(root, "a", "a.m4b"),
		model.LayoutSingle, []string{"a.m4b"}, model.Book{Title: "A", Author: "AA", Year: 2001})
	b := matchedBook(t, d, lib, filepath.Join(root, "b"), filepath.Join(root, "b", "b.m4b"),
		model.LayoutSingle, []string{"b.m4b"}, model.Book{Title: "B", Author: "BB", Year: 2002})

	bFileID := b.Files[0].ID
	bRelBefore := b.Files[0].RelPath
	aDirBefore := a.SourceDir

	job, _ := d.CreateJobPayload(model.JobOrganize, lib.ID, "")
	// The final claims to be for book A but its file map names book B's file.
	err := d.FinalizeOrganize(job.ID, 0, []db.OrganizeFinal{{
		BookID:       a.ID,
		NewSourceDir: a.SourceDir,
		Snapshot:     "{}",
		FileRelByID:  map[string]string{bFileID: "hijacked/path.m4b"},
	}})
	if err == nil {
		t.Fatal("expected FinalizeOrganize to reject a file id not owned by the book")
	}

	// Whole transaction rolled back: book B's file untouched, and book A's
	// location update didn't land either.
	gotB, err := d.GetBook(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotB.Files[0].RelPath != bRelBefore {
		t.Errorf("book B's file was repointed via book A's final: %q -> %q", bRelBefore, gotB.Files[0].RelPath)
	}
	gotA, err := d.GetBook(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotA.SourceDir != aDirBefore {
		t.Errorf("book A's location changed despite the failed finalize: %q -> %q", aDirBefore, gotA.SourceDir)
	}
}

func TestBuildPlan_SkipsUnmatchedAndCollisions(t *testing.T) {
	d := openTestDB(t)
	root := t.TempDir()
	lib, _ := d.CreateLibrary(model.Library{Name: "L", RootPath: root, StructureMode: model.AuthorFirst})

	// Two different books whose metadata renders to the same path.
	a := matchedBook(t, d, lib, filepath.Join(root, "a"), filepath.Join(root, "a", "x.m4b"),
		model.LayoutSingle, []string{"x.m4b"}, model.Book{Title: "Dup", Author: "Same Author", Year: 2000})
	b := matchedBook(t, d, lib, filepath.Join(root, "b"), filepath.Join(root, "b", "y.m4b"),
		model.LayoutSingle, []string{"y.m4b"}, model.Book{Title: "Dup", Author: "Same Author", Year: 2000})

	plan, err := BuildPlan(d, lib.ID, []string{a.ID, b.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Conflicts) == 0 {
		t.Fatal("expected a collision to be reported")
	}
	skipped := 0
	for _, bp := range plan.Books {
		if bp.Skip {
			skipped++
		}
	}
	if skipped == 0 {
		t.Fatal("expected the colliding book to be skipped")
	}
}

// A journal write that fails after the filesystem step has already been
// reversed must abort the undo, not just log and continue: otherwise the
// journal keeps calling the step "done" while disk no longer reflects it. A
// retry after the fault clears re-lists the ops and resumes.
func TestUndo_AbortsWhenJournalUpdateFails(t *testing.T) {
	d := openTestDB(t)
	root := t.TempDir()
	lib, _ := d.CreateLibrary(model.Library{Name: "L", RootPath: root, StructureMode: model.AuthorFirst})

	src := filepath.Join(root, "incoming", "phm.m4b")
	book := matchedBook(t, d, lib, filepath.Join(root, "incoming"), src,
		model.LayoutSingle, []string{"phm.m4b"},
		model.Book{Title: "Project Hail Mary", Author: "Andy Weir", Year: 2021})

	plan, err := BuildPlan(d, lib.ID, []string{book.ID})
	if err != nil {
		t.Fatal(err)
	}
	job, _ := d.CreateJobPayload(model.JobOrganize, lib.ID, "")
	if err := Execute(context.Background(), d, job.ID, plan, nil); err != nil {
		t.Fatalf("execute: %v", err)
	}

	// Fault injection: SELECTs on rename_ops still work (undo lists them) but any
	// status write aborts, standing in for a journal write that dies mid-undo.
	if _, err := d.Exec(
		`CREATE TRIGGER abr_block_rename_ops_update BEFORE UPDATE ON rename_ops
		 BEGIN SELECT RAISE(ABORT, 'journal write blocked for test'); END`,
	); err != nil {
		t.Fatal(err)
	}

	_, err = Undo(context.Background(), d, job.ID)
	if err == nil {
		t.Fatal("expected Undo to abort when the journal update fails")
	}
	if !strings.Contains(err.Error(), "journal update for step") {
		t.Fatalf("error should name the failed journal update, got: %v", err)
	}

	// Clear the fault; a retry must resume and finish cleanly.
	if _, err := d.Exec(`DROP TRIGGER abr_block_rename_ops_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := Undo(context.Background(), d, job.ID); err != nil {
		t.Fatalf("resumed undo after fault cleared: %v", err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Errorf("resumed undo should have restored the original file: %v", err)
	}
	back, _ := d.GetBook(book.ID)
	if back.State != model.StateMatched {
		t.Errorf("after resumed undo state = %s, want matched", back.State)
	}
}

// A successful rmdir whose journal "done" write fails must abort the run and
// roll back, exactly like a failed mkdir/move journal write. Undo only reverses
// steps whose status == "done", so a pruned folder left non-"done" would be
// silently skipped by a later undo and never recreated.
func TestExecute_AbortsWhenRmdirJournalUpdateFails(t *testing.T) {
	d := openTestDB(t)
	root := t.TempDir()
	lib, _ := d.CreateLibrary(model.Library{Name: "L", RootPath: root, StructureMode: model.AuthorFirst})

	src := filepath.Join(root, "incoming", "phm.m4b")
	book := matchedBook(t, d, lib, filepath.Join(root, "incoming"), src,
		model.LayoutSingle, []string{"phm.m4b"},
		model.Book{Title: "Project Hail Mary", Author: "Andy Weir", Year: 2021})

	plan, err := BuildPlan(d, lib.ID, []string{book.ID})
	if err != nil {
		t.Fatal(err)
	}

	// Fault injection: only the rmdir step's status write aborts. The mkdir and
	// move inserts/updates are untouched, so the run gets as far as pruning the
	// emptied "incoming" dir and then fails flipping that journal row to "done".
	if _, err := d.Exec(
		`CREATE TRIGGER abr_block_rmdir_done BEFORE UPDATE ON rename_ops
		 WHEN NEW.op = 'rmdir'
		 BEGIN SELECT RAISE(ABORT, 'rmdir journal write blocked for test'); END`,
	); err != nil {
		t.Fatal(err)
	}

	oldDir := filepath.Join(root, "incoming")
	job, _ := d.CreateJobPayload(model.JobOrganize, lib.ID, "")
	err = Execute(context.Background(), d, job.ID, plan, nil)
	if err == nil {
		t.Fatal("expected Execute to abort when the rmdir journal update fails")
	}
	if !strings.Contains(err.Error(), "journal update for rmdir") {
		t.Fatalf("error should name the failed rmdir journal update, got: %v", err)
	}

	// Rollback recreates an OpRmdir dir, so the pruned folder must be back on
	// disk and the original file restored.
	if _, err := os.Stat(oldDir); err != nil {
		t.Errorf("pruned dir not restored by rollback: %v", err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Errorf("original file not restored by rollback: %v", err)
	}
}

// synthMovePlan builds a one-book, one-move plan by hand. BuildPlan skips
// unsafe moves, so a test that wants to drive Execute's own containment guard
// has to construct the plan directly.
func synthMovePlan(root, fromRel, toRel string) *Plan {
	return &Plan{
		RootPath: root,
		Books: []BookPlan{{
			BookID:       "synthetic",
			Title:        "T",
			Moves:        []FileMove{{FromRel: fromRel, ToRel: toRel}},
			OldSourceDir: filepath.Dir(filepath.Join(root, filepath.FromSlash(fromRel))),
			NewSourceDir: filepath.Dir(filepath.Join(root, filepath.FromSlash(toRel))),
			OldFileRel:   map[string]string{},
			NewFileRel:   map[string]string{},
		}},
	}
}

func runSynthMove(t *testing.T, root, fromRel, toRel string) error {
	t.Helper()
	d := openTestDB(t)
	lib, err := d.CreateLibrary(model.Library{Name: "L", RootPath: root, StructureMode: model.AuthorFirst})
	if err != nil {
		t.Fatal(err)
	}
	job, err := d.CreateJobPayload(model.JobOrganize, lib.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	return Execute(context.Background(), d, job.ID, synthMovePlan(root, fromRel, toRel), nil)
}

// Execute's containment guard rejects an unsafe move whether it classifies as a
// plain move or a case-only rename: a symlinked source is refused, and a
// destination that resolves outside the library root is refused. The per-op
// re-check before each os.Rename shares this guard, so the same wording holds.
func TestExecute_ContainmentGuardRejectsUnsafeMoves(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is restricted on Windows CI")
	}

	t.Run("plain move rejects symlinked source", func(t *testing.T) {
		root := t.TempDir()
		real := filepath.Join(root, "real", "book.m4b")
		writeFile(t, real)
		link := filepath.Join(root, "in", "book.m4b")
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		err := runSynthMove(t, root, "in/book.m4b", "Andy Weir/Book (2020)/Book (2020) - Andy Weir.m4b")
		if err == nil || !strings.Contains(err.Error(), "refusing to move symlink") {
			t.Fatalf("plain move should refuse a symlinked source, got: %v", err)
		}
		if _, statErr := os.Stat(real); statErr != nil {
			t.Errorf("the symlink target must be left untouched: %v", statErr)
		}
	})

	t.Run("case-fix move rejects symlinked source", func(t *testing.T) {
		root := t.TempDir()
		real := filepath.Join(root, "real", "book.m4b")
		writeFile(t, real)
		link := filepath.Join(root, "Sub", "book.m4b")
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		// FromRel/ToRel differ only by case -> Execute classifies this as a case-fix.
		err := runSynthMove(t, root, "Sub/book.m4b", "sub/book.m4b")
		if err == nil || !strings.Contains(err.Error(), "refusing to move symlink") {
			t.Fatalf("case-fix move should refuse a symlinked source, got: %v", err)
		}
	})

	t.Run("plain move rejects out-of-root destination", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, filepath.Join(root, "in", "book.m4b"))
		err := runSynthMove(t, root, "in/book.m4b", "../escaped/book.m4b")
		if err == nil || !strings.Contains(err.Error(), "refusing move outside library root") {
			t.Fatalf("plain move should refuse an out-of-root destination, got: %v", err)
		}
		if _, statErr := os.Stat(filepath.Join(root, "in", "book.m4b")); statErr != nil {
			t.Errorf("source must be untouched after a refused move: %v", statErr)
		}
	})

	t.Run("case-fix move rejects out-of-root destination", func(t *testing.T) {
		root := t.TempDir()
		outside := t.TempDir()
		writeFile(t, filepath.Join(outside, "Book.m4b"))
		// A symlinked directory inside the root escapes it; the case-only rename
		// then resolves both endpoints outside the library root.
		if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
			t.Fatal(err)
		}
		err := runSynthMove(t, root, "link/Book.m4b", "link/book.m4b")
		if err == nil || !strings.Contains(err.Error(), "refusing move outside library root") {
			t.Fatalf("case-fix move should refuse an out-of-root destination, got: %v", err)
		}
	})
}

// A union or pooled filesystem (mergerfs, mhddfs) — the usual reason a homelab
// library lives under a "/DataPool" mount — rejects a cross-directory rename
// with EPERM rather than EXDEV. The move must fall back to copy-then-delete
// instead of failing the whole organize run and leaving the book at "matched".
func TestExecute_RenameEPERMFallsBackToCopy(t *testing.T) {
	orig := renameFile
	var tripped int
	renameFile = func(oldpath, newpath string) error {
		if strings.HasSuffix(oldpath, ".mp3") {
			tripped++
			return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EPERM}
		}
		return orig(oldpath, newpath)
	}
	t.Cleanup(func() { renameFile = orig })

	d := openTestDB(t)
	root := t.TempDir()
	lib, err := d.CreateLibrary(model.Library{Name: "L", RootPath: root, StructureMode: model.AuthorFirst})
	if err != nil {
		t.Fatal(err)
	}

	b := matchedBook(t, d, lib,
		filepath.Join(root, "incoming", "Long Night"), "",
		model.LayoutMulti, []string{"01.mp3", "02.mp3"},
		model.Book{Title: "The Long Night", Author: "Aaron Dembski-Bowden", Series: "The Horus Heresy", SeriesIndex: "35", Year: 2017},
	)

	plan, err := BuildPlan(d, lib.ID, []string{b.ID})
	if err != nil {
		t.Fatal(err)
	}
	job, err := d.CreateJobPayload(model.JobOrganize, lib.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := Execute(context.Background(), d, job.ID, plan, nil); err != nil {
		t.Fatalf("execute should have recovered via the copy fallback: %v", err)
	}
	if tripped == 0 {
		t.Fatal("test never exercised the EPERM rename path")
	}

	want := filepath.Join(root, "Aaron Dembski-Bowden", "The Horus Heresy", "The Long Night (2017)", "The Long Night (2017) - 02.mp3")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected renamed file at %s: %v", want, err)
	}
	if _, err := os.Stat(filepath.Join(root, "incoming", "Long Night", "01.mp3")); !os.IsNotExist(err) {
		t.Error("source file should have been removed after the copy fallback")
	}
	if got, _ := d.GetBook(b.ID); got.State != model.StateOrganized {
		t.Errorf("book state = %s, want organized", got.State)
	}
}
