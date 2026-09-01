package scanner

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"audiobookrenamer/internal/db"
	"audiobookrenamer/internal/model"
	"audiobookrenamer/internal/worker"
)

// id3v2 builds a minimal ID3v2.3 tag carrying TIT2/TALB/TPE1 text frames, so a
// test can produce a file whose embedded tags dhowden/tag can read without a
// tag-writing dependency.
func id3v2(title, album, artist string) []byte {
	frame := func(id, val string) []byte {
		payload := append([]byte{0x00}, []byte(val)...) // 0x00 = ISO-8859-1
		b := []byte(id)
		var sz [4]byte
		binary.BigEndian.PutUint32(sz[:], uint32(len(payload))) // v2.3: plain uint32
		b = append(b, sz[:]...)
		b = append(b, 0x00, 0x00) // frame flags
		return append(b, payload...)
	}
	var frames []byte
	if title != "" {
		frames = append(frames, frame("TIT2", title)...)
	}
	if album != "" {
		frames = append(frames, frame("TALB", album)...)
	}
	if artist != "" {
		frames = append(frames, frame("TPE1", artist)...)
	}

	n := len(frames)
	// Header tag size is synchsafe (7 significant bits per byte).
	ss := []byte{byte((n >> 21) & 0x7f), byte((n >> 14) & 0x7f), byte((n >> 7) & 0x7f), byte(n & 0x7f)}
	hdr := append([]byte("ID3"), 0x03, 0x00, 0x00) // version 2.3.0, no flags
	hdr = append(hdr, ss...)
	return append(hdr, frames...)
}

func writeTagged(t *testing.T, path, title, album, artist string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := append(id3v2(title, album, artist), make([]byte, 512)...) // filler "audio"
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func runScan(t *testing.T, d *db.DB, libID string) {
	t.Helper()
	wm := worker.New(d, 1)
	defer wm.Shutdown()
	Register(wm, d)
	job, err := wm.Enqueue(model.JobScan, libID)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, err := d.GetJob(job.ID)
		if err != nil {
			t.Fatal(err)
		}
		switch got.Status {
		case model.JobDone:
			return
		case model.JobFailed, model.JobCanceled:
			t.Fatalf("scan job ended %s: %s", got.Status, got.Error)
		}
		if time.Now().After(deadline) {
			t.Fatalf("scan job did not finish (last status %s)", got.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A tag-only rewrite that leaves the file's size and mtime untouched must still
// be picked up by a rescan: the tag subset that book metadata is derived from is
// folded into the scan fingerprint. Before that fix the second scan saw an
// unchanged fingerprint and skipped the book, so title/author stayed stale.
func TestScan_TagOnlyChangeTriggersRescan(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })

	root := t.TempDir()
	lib, err := d.CreateLibrary(model.Library{Name: "L", RootPath: root, StructureMode: model.AuthorFirst})
	if err != nil {
		t.Fatal(err)
	}

	file := filepath.Join(root, "Author", "book.mp3")
	writeTagged(t, file, "Old Title", "Old Title", "Old Author")

	fi, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	origSize, origMod := fi.Size(), fi.ModTime()

	runScan(t, d, lib.ID)

	books, err := d.ListBooks(db.BookFilter{LibraryID: lib.ID})
	if err != nil || len(books) != 1 {
		t.Fatalf("after first scan: books=%d err=%v", len(books), err)
	}
	b1 := books[0]
	if b1.Title != "Old Title" || b1.Author != "Old Author" {
		t.Fatalf("first scan metadata = %q / %q", b1.Title, b1.Author)
	}

	// Rewrite the tags (same byte length -> same size), then restore mtime so
	// the change is invisible to a path/size/mtime-only fingerprint.
	writeTagged(t, file, "New Title", "New Title", "New Author")
	if fi, err := os.Stat(file); err != nil {
		t.Fatal(err)
	} else if fi.Size() != origSize {
		t.Fatalf("rewrite changed file size: %d -> %d (test fixture bug)", origSize, fi.Size())
	}
	if err := os.Chtimes(file, origMod, origMod); err != nil {
		t.Fatal(err)
	}

	runScan(t, d, lib.ID)

	books, err = d.ListBooks(db.BookFilter{LibraryID: lib.ID})
	if err != nil || len(books) != 1 {
		t.Fatalf("after second scan: books=%d err=%v", len(books), err)
	}
	b2 := books[0]
	if b2.Title != "New Title" || b2.Author != "New Author" {
		t.Fatalf("second scan did not pick up the tag rewrite: title=%q author=%q", b2.Title, b2.Author)
	}
	if b2.ScanFingerprint == b1.ScanFingerprint {
		t.Fatalf("fingerprint unchanged after tag rewrite: %s", b2.ScanFingerprint)
	}
}
