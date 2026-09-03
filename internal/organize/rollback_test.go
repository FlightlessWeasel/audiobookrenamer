package organize

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"audiobookrenamer/internal/model"
)

// A run that fails after moving files rolls the filesystem back — and must say
// so in the journal. Leaving the reversed steps marked "done" describes moves
// that no longer exist on disk, and the undo of that failed job would then try
// to reverse them a second time and abort on the first missing file.
func TestExecute_RollbackMarksStepsReverted(t *testing.T) {
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

	// Delete the book row so finalization fails after every file has moved.
	if err := d.DeleteBooks([]string{book.ID}); err != nil {
		t.Fatal(err)
	}
	if err := Execute(context.Background(), d, job.ID, plan, nil); err == nil {
		t.Fatal("expected Execute to fail when finalization can't complete")
	}

	ops, err := d.ListRenameOps(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) == 0 {
		t.Fatal("expected journal steps for the run")
	}
	for _, o := range ops {
		if o.Status == "done" {
			t.Errorf("step %s (%s -> %s) still marked done after rollback", o.Kind, o.Src, o.Dst)
		}
	}

	// With the journal accurate, undoing the failed job is a supported no-op
	// rather than a cascade of "destination already exists" failures.
	if _, err := Undo(context.Background(), d, job.ID); err != nil {
		t.Fatalf("undo of a rolled-back job should succeed: %v", err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Errorf("original file should still be in place after undo: %v", err)
	}
}

// A rollback whose journal write does not land leaves a step marked "done"
// whose file is already back at its source. Undo is the retry for that job, so
// it has to treat such a step as already reversed instead of failing on
// doMove's "destination already exists" guard.
func TestReverseMove_TreatsAlreadyReversedStepAsDone(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.m4b")
	dst := filepath.Join(dir, "b.m4b")
	writeFile(t, src)

	if err := reverseMove(src, dst); err != nil {
		t.Fatalf("reversing an already-reversed move should be a no-op: %v", err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Errorf("source should be untouched: %v", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Errorf("no file should have been created at the destination")
	}

	// The ordinary case still moves.
	if err := reverseMove(dst, src); err != nil {
		t.Fatalf("reverseMove: %v", err)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Errorf("file should have been moved back to %s: %v", dst, err)
	}
}

// os.Rename cannot move between filesystems, which a library root spanning
// several Docker bind mounts routinely asks it to do. The fallback must land a
// complete copy and only then drop the source, preserving mode and mtime so a
// rescan doesn't read the file as changed.
func TestMoveAcrossDevices_CopiesThenRemovesSource(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src", "a.m4b")
	dst := filepath.Join(dir, "dst", "b.m4b")
	writeFile(t, src)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	want := time.Now().Add(-72 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(src, want, want); err != nil {
		t.Fatal(err)
	}

	if err := moveAcrossDevices(src, dst); err != nil {
		t.Fatalf("moveAcrossDevices: %v", err)
	}

	body, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("destination missing: %v", err)
	}
	if string(body) != "audio" {
		t.Errorf("destination content = %q, want %q", body, "audio")
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("source should have been removed after a successful copy")
	}
	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.ModTime().Truncate(time.Second); !got.Equal(want) {
		t.Errorf("mtime = %s, want %s (a rescan would read this as changed)", got, want)
	}

	// Never overwrite: a second move onto an occupied destination must refuse.
	writeFile(t, src)
	if err := doMove("", src, dst); err == nil {
		t.Error("expected doMove to refuse an occupied destination")
	}
}

// When the copy succeeds but the source can't be deleted (a read-only mergerfs
// branch, an immutable file), the just-placed copy is removed again so the tree
// is left untouched — a stranded duplicate would also stop the caller's
// rollback from pruning the new directory.
func TestMoveAcrossDevices_UndoesCopyWhenSourceCannotBeRemoved(t *testing.T) {
	dir := t.TempDir()
	srcDir := filepath.Join(dir, "src")
	src := filepath.Join(srcDir, "a.m4b")
	dst := filepath.Join(dir, "dst", "b.m4b")
	writeFile(t, src)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	// Unlink needs write on the parent directory; drop it so os.Remove(src)
	// fails. Windows chmod and root ignore this, so probe with a throwaway
	// file and skip where the mode isn't enforced.
	probe := filepath.Join(srcDir, "probe")
	if err := os.WriteFile(probe, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(srcDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(srcDir, 0o755) })
	if os.Remove(probe) == nil {
		t.Skip("filesystem does not enforce directory write permission for unlink")
	}

	err := moveAcrossDevices(src, dst)
	if err == nil || !strings.Contains(err.Error(), "could not remove source") {
		t.Fatalf("err = %v, want a 'could not remove source' failure", err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Errorf("source must be left in place when it can't be removed: %v", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Errorf("the copy at dst must be undone, leaving no duplicate")
	}
}

// padRoot returns a library root at least want runes long, built from nested
// segments short enough to stay inside the per-component limit on every OS.
func padRoot(t *testing.T, want int) string {
	t.Helper()
	root := t.TempDir()
	for len(root) < want {
		root = filepath.Join(root, strings.Repeat("d", 50))
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

// The author folder falls back from the sort name to the display name, so a
// book that has only a sort name has a perfectly usable author segment. It must
// not be skipped as authorless, in either layout.
func TestBuildPlan_UsesAuthorSortWhenDisplayNameEmpty(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode model.StructureMode
		want string
	}{
		{"author first", model.AuthorFirst, filepath.Join("King, Stephen", "The Dark Tower", "The Gunslinger (1982)")},
		{"series first", model.SeriesFirst, filepath.Join("The Dark Tower", "King, Stephen", "The Gunslinger (1982)")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := openTestDB(t)
			root := t.TempDir()
			lib, _ := d.CreateLibrary(model.Library{Name: "L", RootPath: root, StructureMode: tc.mode})

			book := matchedBook(t, d, lib,
				filepath.Join(root, "incoming"), filepath.Join(root, "incoming", "g.m4b"),
				model.LayoutSingle, []string{"g.m4b"},
				model.Book{Title: "The Gunslinger", AuthorSort: "King, Stephen", Series: "The Dark Tower", Year: 1982})

			plan, err := BuildPlan(d, lib.ID, []string{book.ID})
			if err != nil {
				t.Fatal(err)
			}
			bp := plan.Books[0]
			if bp.Skip {
				t.Fatalf("book skipped: %s", bp.Reason)
			}
			wantDir := filepath.Join(root, tc.want)
			if bp.NewSourceDir != wantDir {
				t.Errorf("target dir = %q, want %q", bp.NewSourceDir, wantDir)
			}
		})
	}
}

// Each segment is clamped to 180 runes, but their sum is not: root + author +
// series + book folder + file name blows past MAX_PATH on Windows long before
// any single segment is oversized. Folders are shortened to fit; file names,
// which carry the track number at the end, are left intact.
func TestBuildPlan_ShortensFoldersToFitPathBudget(t *testing.T) {
	d := openTestDB(t)
	// Leave ~250 runes of headroom, so the same tight budget applies whatever
	// MaxPathLen is on this platform.
	root := padRoot(t, MaxPathLen-250)
	lib, _ := d.CreateLibrary(model.Library{Name: "L", RootPath: root, StructureMode: model.AuthorFirst})

	long := strings.Repeat("A", 170)
	book := matchedBook(t, d, lib,
		filepath.Join(root, "incoming"), "",
		model.LayoutMulti, []string{"01.mp3", "02.mp3"},
		model.Book{Title: "Some Title", Author: long, Series: long, Year: 2021})

	plan, err := BuildPlan(d, lib.ID, []string{book.ID})
	if err != nil {
		t.Fatal(err)
	}
	bp := plan.Books[0]
	if bp.Skip {
		t.Fatalf("book skipped: %s", bp.Reason)
	}
	for _, m := range bp.Moves {
		abs := filepath.Join(root, filepath.FromSlash(m.ToRel))
		if len([]rune(abs)) > MaxPathLen {
			t.Errorf("target path is %d runes, over the %d limit: %s", len([]rune(abs)), MaxPathLen, abs)
		}
		// The track number lives at the end of the multi-file template; losing
		// it would merge the two tracks onto one name.
		if !strings.HasSuffix(m.ToRel, "01.mp3") && !strings.HasSuffix(m.ToRel, "02.mp3") {
			t.Errorf("file name was truncated, dropping its track number: %s", m.ToRel)
		}
	}
}

// When even the fully-shortened folder chain cannot fit, the book is skipped
// with a reason the preview can show — not planned into a move that fails
// halfway through the run.
func TestBuildPlan_SkipsWhenPathCannotFit(t *testing.T) {
	d := openTestDB(t)
	// Only ~50 runes of headroom: not even the shortest legible folder chain
	// fits under it.
	root := padRoot(t, MaxPathLen-50)
	lib, _ := d.CreateLibrary(model.Library{Name: "L", RootPath: root, StructureMode: model.AuthorFirst})

	book := matchedBook(t, d, lib,
		filepath.Join(root, "incoming"), filepath.Join(root, "incoming", "g.m4b"),
		model.LayoutSingle, []string{"g.m4b"},
		model.Book{Title: "The Gunslinger", Author: "Stephen King", Year: 1982})

	plan, err := BuildPlan(d, lib.ID, []string{book.ID})
	if err != nil {
		t.Fatal(err)
	}
	bp := plan.Books[0]
	if !bp.Skip {
		t.Fatalf("expected the book to be skipped, got target %q", bp.NewSourceDir)
	}
	if !strings.Contains(bp.Reason, "path length") {
		t.Errorf("skip reason = %q, want it to name the path length limit", bp.Reason)
	}
}
