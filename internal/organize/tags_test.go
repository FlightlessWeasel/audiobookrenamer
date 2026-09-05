package organize

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"audiobookrenamer/internal/db"
	"audiobookrenamer/internal/model"
	"audiobookrenamer/internal/tagwrite"
)

// writeAudioPlaceholder writes enough bytes for the container writers used in
// these tests to accept "no tags yet" gracefully. dhowden's/bogem's ID3v2
// reader treats anything under its 10-byte header as a parse error rather than
// "absent" — a real audio file is always far longer, but the placeholder
// content in these tests otherwise doesn't matter.
func writeAudioPlaceholder(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("placeholder audio content, not a real container"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// tagWriteLibrary creates a library with WriteTags (and optionally EmbedCover)
// on, and a single matched .mp3 book (ID3v2 tolerates the placeholder content,
// unlike the MP4/FLAC writers, which need a real box/block structure).
func tagWriteLibrary(t *testing.T, d *db.DB, embedCover bool) (model.Library, model.Book) {
	t.Helper()
	root := t.TempDir()
	lib, err := d.CreateLibrary(model.Library{
		Name: "L", RootPath: root, StructureMode: model.AuthorFirst,
		WriteTags: true, EmbedCover: embedCover,
	})
	if err != nil {
		t.Fatal(err)
	}
	srcFile := filepath.Join(root, "incoming", "book.mp3")
	writeAudioPlaceholder(t, srcFile)

	b, err := d.UpsertBook(model.Book{
		LibraryID: lib.ID, SourceDir: filepath.Dir(srcFile), SourceFile: srcFile,
		Layout: model.LayoutSingle, State: model.StateUnmatched,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.ReplaceBookFiles(b.ID, []model.BookFile{{RelPath: "book.mp3", Ext: ".mp3", Track: 1}}); err != nil {
		t.Fatal(err)
	}
	meta := model.Book{
		ID: b.ID, State: model.StateMatched,
		Title: "Elantris", Author: "Brandon Sanderson", Narrator: "Reader A", Year: 2005,
	}
	if err := d.SetBookMatch(b.ID, meta); err != nil {
		t.Fatal(err)
	}
	out, err := d.GetBook(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	return lib, out
}

func readTags(t *testing.T, path string) tagwrite.TagSet {
	t.Helper()
	w, err := tagwrite.WriterFor(filepath.Ext(path))
	if err != nil {
		t.Fatalf("WriterFor: %v", err)
	}
	ts, err := w.Read(path)
	if err != nil {
		t.Fatalf("Read(%s): %v", path, err)
	}
	return ts
}

func countTagWriteOps(t *testing.T, d *db.DB, jobID string) int {
	t.Helper()
	ops, err := d.ListRenameOps(jobID)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, o := range ops {
		if o.Kind == string(OpTagWrite) {
			n++
		}
	}
	return n
}

func organizedPath(lib model.Library) string {
	return filepath.Join(lib.RootPath, "Brandon Sanderson", "Elantris (2005)", "Elantris (2005) - Brandon Sanderson.mp3")
}

// Execute writes embedded tags for a library with WriteTags on, journals the
// step, and the file ends up carrying the accepted metadata.
func TestExecute_WritesTagsWhenLibraryWriteTagsOn(t *testing.T) {
	d := openTestDB(t)
	lib, b := tagWriteLibrary(t, d, false)

	plan, err := BuildPlan(d, lib.ID, []string{b.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Books) != 1 || !plan.Books[0].TagFiles[0].Writable || !plan.Books[0].TagFiles[0].Changed {
		t.Fatalf("plan tag state = %+v, want writable+changed", plan.Books[0].TagFiles)
	}

	job, err := d.CreateJobPayload(model.JobOrganize, lib.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	plan.BackupDir = t.TempDir()
	if err := Execute(context.Background(), d, job.ID, plan, nil); err != nil {
		t.Fatalf("execute: %v", err)
	}

	target := organizedPath(lib)
	got := readTags(t, target)
	if got.Title != "Elantris" || got.Artist != "Brandon Sanderson" || got.Composer != "Reader A" || got.Year != 2005 {
		t.Fatalf("tags after organize = %+v", got)
	}
	if n := countTagWriteOps(t, d, job.ID); n != 1 {
		t.Fatalf("tagwrite ops journaled = %d, want 1", n)
	}
}

// A library with WriteTags off must not touch file contents at all: no
// TagFiles are even planned, and no tagwrite op is journaled.
func TestExecute_SkipsTagWriteWhenLibraryWriteTagsOff(t *testing.T) {
	d := openTestDB(t)
	root := t.TempDir()
	lib, err := d.CreateLibrary(model.Library{Name: "L", RootPath: root, StructureMode: model.AuthorFirst})
	if err != nil {
		t.Fatal(err)
	}
	srcFile := filepath.Join(root, "incoming", "book.mp3")
	b := matchedBook(t, d, lib, filepath.Dir(srcFile), srcFile, model.LayoutSingle, []string{"book.mp3"},
		model.Book{Title: "Elantris", Author: "Brandon Sanderson", Year: 2005})
	// matchedBook's own placeholder content is too short for a real ID3v2
	// reader to treat as "no tag" rather than "truncated"; this test verifies
	// tags via readTags, so it needs the longer placeholder even though
	// WriteTags is off and organize itself never reads this file's tags.
	writeAudioPlaceholder(t, srcFile)

	plan, err := BuildPlan(d, lib.ID, []string{b.ID})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Books[0].TagFiles != nil {
		t.Fatalf("TagFiles = %+v, want nil when WriteTags is off", plan.Books[0].TagFiles)
	}

	job, err := d.CreateJobPayload(model.JobOrganize, lib.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := Execute(context.Background(), d, job.ID, plan, nil); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if n := countTagWriteOps(t, d, job.ID); n != 0 {
		t.Fatalf("tagwrite ops journaled = %d, want 0 when WriteTags is off", n)
	}

	target := filepath.Join(root, "Brandon Sanderson", "Elantris (2005)", "Elantris (2005) - Brandon Sanderson.mp3")
	got := readTags(t, target)
	if got.Title != "" {
		t.Fatalf("tags = %+v, want untouched (empty)", got)
	}
}

// A second organize run, once tags already match, plans no change and writes
// nothing again — the same NoOp idempotency a file move already has.
func TestExecute_TagWriteIsNoOpOnSecondRun(t *testing.T) {
	d := openTestDB(t)
	lib, b := tagWriteLibrary(t, d, false)

	plan, err := BuildPlan(d, lib.ID, []string{b.ID})
	if err != nil {
		t.Fatal(err)
	}
	job1, _ := d.CreateJobPayload(model.JobOrganize, lib.ID, "")
	plan.BackupDir = t.TempDir()
	if err := Execute(context.Background(), d, job1.ID, plan, nil); err != nil {
		t.Fatalf("execute 1: %v", err)
	}

	plan2, err := BuildPlan(d, lib.ID, []string{b.ID})
	if err != nil {
		t.Fatal(err)
	}
	if plan2.Books[0].TagFiles[0].Changed {
		t.Fatalf("second plan says tags changed; want them already matching")
	}
	job2, _ := d.CreateJobPayload(model.JobOrganize, lib.ID, "")
	plan2.BackupDir = plan.BackupDir
	if err := Execute(context.Background(), d, job2.ID, plan2, nil); err != nil {
		t.Fatalf("execute 2: %v", err)
	}
	if n := countTagWriteOps(t, d, job2.ID); n != 0 {
		t.Fatalf("second run journaled %d tagwrite ops, want 0 (no-op)", n)
	}
}

// A tag-write's backup is removed as soon as the run that made it fully
// commits — there is nothing left for that run's own rollback to restore, and
// leaving it in place would mean a library's first-ever retag leaves a full
// duplicate of it sitting in the backup directory forever, since nothing
// would ever supersede a first-time backup to reclaim the space.
func TestExecute_RemovesTagBackupAfterSuccess(t *testing.T) {
	d := openTestDB(t)
	lib, b := tagWriteLibrary(t, d, false)

	plan, err := BuildPlan(d, lib.ID, []string{b.ID})
	if err != nil {
		t.Fatal(err)
	}
	job, _ := d.CreateJobPayload(model.JobOrganize, lib.ID, "")
	backupDir := t.TempDir()
	plan.BackupDir = backupDir
	if err := Execute(context.Background(), d, job.ID, plan, nil); err != nil {
		t.Fatalf("execute: %v", err)
	}

	target := organizedPath(lib)
	if got := readTags(t, target); got.Title != "Elantris" {
		t.Fatalf("tags after execute = %+v", got)
	}

	entries, err := os.ReadDir(backupDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("backup dir has %d leftover file(s) after a successful run: %v", len(entries), entries)
	}
}

// Once a job has fully committed, Undo can still reverse its file move but
// can no longer restore the tags it wrote — their backup is already gone (see
// TestExecute_RemovesTagBackupAfterSuccess). This is the accepted cost of not
// keeping a duplicate of the library around indefinitely: only a same-run
// failure (TestExecute_TagWriteFailureRollsBackMoves) still has the backup
// available to restore from.
func TestUndo_CannotRestoreTagsOnceJobHasCommitted(t *testing.T) {
	d := openTestDB(t)
	lib, b := tagWriteLibrary(t, d, false)

	plan, err := BuildPlan(d, lib.ID, []string{b.ID})
	if err != nil {
		t.Fatal(err)
	}
	job, _ := d.CreateJobPayload(model.JobOrganize, lib.ID, "")
	plan.BackupDir = t.TempDir()
	if err := Execute(context.Background(), d, job.ID, plan, nil); err != nil {
		t.Fatalf("execute: %v", err)
	}

	target := organizedPath(lib)
	if got := readTags(t, target); got.Title != "Elantris" {
		t.Fatalf("tags before undo = %+v", got)
	}

	res, err := Undo(context.Background(), d, job.ID)
	if err != nil {
		t.Fatalf("undo: %v", err)
	}
	if len(res.UnrestoredTagFiles) != 1 || res.UnrestoredTagFiles[0] != target {
		t.Fatalf("undo unrestored = %v, want [%s]", res.UnrestoredTagFiles, target)
	}

	// The move still reverses...
	srcFile := filepath.Join(lib.RootPath, "incoming", "book.mp3")
	if _, err := os.Stat(srcFile); err != nil {
		t.Fatalf("undo did not reverse the file move: %v", err)
	}
	// ...but the tags organize wrote are left standing, not reverted to empty.
	if got := readTags(t, srcFile); got.Title != "Elantris" {
		t.Fatalf("tags after undo = %+v, want left as written (not restorable)", got)
	}

	_, _, ok, err := d.CurrentTagBackupOwner(target)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("tag backup ownership row survived undo; want it gone (reverted)")
	}
}

// A tag-write failure must roll back the moves from the same run too, not
// just abandon the tag step.
func TestExecute_TagWriteFailureRollsBackMoves(t *testing.T) {
	d := openTestDB(t)
	lib, b := tagWriteLibrary(t, d, false)
	srcFile := filepath.Join(lib.RootPath, "incoming", "book.mp3")

	plan, err := BuildPlan(d, lib.ID, []string{b.ID})
	if err != nil {
		t.Fatal(err)
	}
	// A regular file where the backup directory needs to be: MkdirAll fails
	// deterministically (not a directory), which fails the tag-write before it
	// touches anything.
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan.BackupDir = filepath.Join(blocked, "tagbackups")

	job, _ := d.CreateJobPayload(model.JobOrganize, lib.ID, "")
	if err := Execute(context.Background(), d, job.ID, plan, nil); err == nil {
		t.Fatal("expected Execute to fail")
	}

	if _, err := os.Stat(srcFile); err != nil {
		t.Fatalf("move was not rolled back: original file missing: %v", err)
	}
	if _, err := os.Stat(organizedPath(lib)); !os.IsNotExist(err) {
		t.Fatal("organized file exists despite the run failing")
	}
}

// Two successive retags of the same file, each committing before the next
// runs: job A's backup is deleted the moment job A succeeds, so job B (which
// retags the same path in place) finds no existing backup to reuse and
// allocates a fresh one — proving reuseOrAllocBackupPath handles a
// DB-owned-but-missing backup file by allocating rather than erroring. That
// fresh backup is in turn deleted the moment job B succeeds, so neither job's
// Undo can restore tag content afterward; both still reverse whatever they
// moved, and both report the file as tag-unrestored rather than failing.
func TestUndo_SkipsTagRestoreWhenBackupSuperseded(t *testing.T) {
	d := openTestDB(t)
	lib, b := tagWriteLibrary(t, d, false)
	backupDir := t.TempDir()
	srcFile := filepath.Join(lib.RootPath, "incoming", "book.mp3")

	planA, err := BuildPlan(d, lib.ID, []string{b.ID})
	if err != nil {
		t.Fatal(err)
	}
	jobA, _ := d.CreateJobPayload(model.JobOrganize, lib.ID, "")
	planA.BackupDir = backupDir
	if err := Execute(context.Background(), d, jobA.ID, planA, nil); err != nil {
		t.Fatalf("execute A: %v", err)
	}
	target := organizedPath(lib)
	if got := readTags(t, target); got.Composer != "Reader A" {
		t.Fatalf("tags after job A = %+v", got)
	}

	// Change only the narrator (not part of the file template), so job B
	// retags the same file in place rather than moving it — the scenario
	// where a later tag-write can supersede an earlier one without also
	// superseding its move.
	reloaded, err := d.GetBook(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	reloaded.Narrator = "Reader B"
	if err := d.SetBookMatch(b.ID, reloaded); err != nil {
		t.Fatal(err)
	}

	planB, err := BuildPlan(d, lib.ID, []string{b.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !planB.Books[0].TagFiles[0].Changed {
		t.Fatal("expected the narrator change to be planned as a tag change")
	}
	jobB, _ := d.CreateJobPayload(model.JobOrganize, lib.ID, "")
	planB.BackupDir = backupDir
	if err := Execute(context.Background(), d, jobB.ID, planB, nil); err != nil {
		t.Fatalf("execute B: %v", err)
	}
	if got := readTags(t, target); got.Composer != "Reader B" {
		t.Fatalf("tags after job B = %+v", got)
	}

	// Undo the most recent job first: its own backup was deleted the instant
	// job B committed, so there is nothing left to restore from — the tags
	// stay exactly as job B wrote them.
	resB, err := Undo(context.Background(), d, jobB.ID)
	if err != nil {
		t.Fatalf("undo B: %v", err)
	}
	if len(resB.UnrestoredTagFiles) != 1 || resB.UnrestoredTagFiles[0] != target {
		t.Fatalf("undo B unrestored = %v, want [%s]", resB.UnrestoredTagFiles, target)
	}
	if got := readTags(t, target); got.Composer != "Reader B" {
		t.Fatalf("tags after undoing job B = %+v, want left as Reader B (not restorable)", got)
	}

	// Undo the older job next: its move still reverses (nothing else moved
	// this file), and its own backup was likewise deleted on its own success,
	// so the tags are left exactly as they stand (Reader B, from job B).
	resA, err := Undo(context.Background(), d, jobA.ID)
	if err != nil {
		t.Fatalf("undo A: %v", err)
	}
	if len(resA.UnrestoredTagFiles) != 1 || resA.UnrestoredTagFiles[0] != target {
		t.Fatalf("undo A unrestored = %v, want [%s]", resA.UnrestoredTagFiles, target)
	}
	if _, err := os.Stat(srcFile); err != nil {
		t.Fatalf("undo A did not reverse its own file move: %v", err)
	}
	if got := readTags(t, srcFile); got.Composer != "Reader B" {
		t.Fatalf("tags after undoing job A = %+v, want left as Reader B (not restorable further)", got)
	}
}

// A file whose container has no tag writer is left alone, with a reason
// visible in the plan, and the rest of the book's files are unaffected.
func TestBuildPlan_TagFiles_UnsupportedFormatFlagged(t *testing.T) {
	d := openTestDB(t)
	root := t.TempDir()
	lib, err := d.CreateLibrary(model.Library{
		Name: "L", RootPath: root, StructureMode: model.AuthorFirst, WriteTags: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "incoming")
	writeAudioPlaceholder(t, filepath.Join(dir, "01.ogg"))
	writeAudioPlaceholder(t, filepath.Join(dir, "02.mp3"))

	b, err := d.UpsertBook(model.Book{LibraryID: lib.ID, SourceDir: dir, Layout: model.LayoutMulti, State: model.StateUnmatched})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.ReplaceBookFiles(b.ID, []model.BookFile{
		{RelPath: "01.ogg", Ext: ".ogg", Track: 1},
		{RelPath: "02.mp3", Ext: ".mp3", Track: 2},
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.SetBookMatch(b.ID, model.Book{ID: b.ID, State: model.StateMatched, Title: "Mixed", Author: "A"}); err != nil {
		t.Fatal(err)
	}

	plan, err := BuildPlan(d, lib.ID, []string{b.ID})
	if err != nil {
		t.Fatal(err)
	}
	tf := plan.Books[0].TagFiles
	if len(tf) != 2 {
		t.Fatalf("TagFiles = %+v, want 2 entries", tf)
	}
	if tf[0].Writable || tf[0].Reason == "" {
		t.Fatalf(".ogg entry = %+v, want not writable with a reason", tf[0])
	}
	if !tf[1].Writable || !tf[1].Changed {
		t.Fatalf(".mp3 entry = %+v, want writable and changed", tf[1])
	}
}

// EmbedCover attaches the book's cached cover to the desired tag set, which
// makes an otherwise-already-tagged file register as changed the moment a
// cover becomes available.
func TestBuildPlan_TagFiles_EmbedCoverMakesFileChanged(t *testing.T) {
	d := openTestDB(t)
	lib, b := tagWriteLibrary(t, d, false) // EmbedCover off, first pass

	// Write the (coverless) tags once, so the only remaining difference is the
	// cover itself.
	plan, err := BuildPlan(d, lib.ID, []string{b.ID})
	if err != nil {
		t.Fatal(err)
	}
	job, _ := d.CreateJobPayload(model.JobOrganize, lib.ID, "")
	plan.BackupDir = t.TempDir()
	if err := Execute(context.Background(), d, job.ID, plan, nil); err != nil {
		t.Fatalf("execute: %v", err)
	}

	png := append([]byte("\x89PNG\r\n\x1a\n"), []byte("cover bytes")...)
	if err := d.SetBookCover(b.ID, "image/png", png, "https://example.test/cover.jpg"); err != nil {
		t.Fatal(err)
	}

	lib.EmbedCover = true
	if _, err := d.UpdateLibrary(lib); err != nil {
		t.Fatal(err)
	}

	plan2, err := BuildPlan(d, lib.ID, []string{b.ID})
	if err != nil {
		t.Fatal(err)
	}
	tf := plan2.Books[0].TagFiles[0]
	if !tf.Changed {
		t.Fatal("expected the newly available cover to register as a tag change")
	}
}
