package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"audiobookrenamer/internal/model"
)

// mk creates empty files at the given paths (relative to root), making parents.
func mk(t *testing.T, root string, paths ...string) {
	t.Helper()
	for _, p := range paths {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func summarize(units []Unit) []string {
	var out []string
	for _, u := range units {
		out = append(out, "dir:"+filepath.Base(u.SourceDir)+":"+string(u.Layout)+":"+itoa(len(u.Files)))
	}
	sort.Strings(out)
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestGroup_SingleM4BPerFolder(t *testing.T) {
	root := t.TempDir()
	mk(t, root, "Brandon Sanderson/Mistborn/Mistborn.m4b", "Brandon Sanderson/Mistborn/cover.jpg")
	units, err := Group(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, summarize(units), []string{"dir:Mistborn:single:1"})
}

func TestGroup_NumberedMultiFile(t *testing.T) {
	root := t.TempDir()
	mk(t, root,
		"Book/01 - Chapter.mp3", "Book/02 - Chapter.mp3", "Book/03 - Chapter.mp3", "Book/04 - Chapter.mp3",
	)
	units, err := Group(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, summarize(units), []string{"dir:Book:multi:4"})
	if units[0].Layout != model.LayoutMulti {
		t.Fatalf("want multi layout")
	}
}

func TestGroup_IgnoresHiddenAndNonAudio(t *testing.T) {
	root := t.TempDir()
	mk(t, root,
		"Book/book.m4b",
		"Book/.DS_Store",
		"Book/notes.txt",
		".trash/old.mp3",
	)
	units, err := Group(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, summarize(units), []string{"dir:Book:single:1"})
}

// A directory the walk can't read must hard-fail the scan, not be silently
// skipped: skipping it would make a rescan prune every book under it as deleted.
func TestGroup_UnreadableDirectoryFailsScan(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("dir perms not enforced on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	root := t.TempDir()
	mk(t, root, "Readable/book.mp3", "locked/hidden.mp3")

	locked := filepath.Join(root, "locked")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	units, err := Group(context.Background(), root)
	if err == nil {
		t.Fatalf("expected error for unreadable directory, got units=%v", summarize(units))
	}
	if !strings.Contains(err.Error(), locked) {
		t.Fatalf("error %q should mention locked path %q", err, locked)
	}
}

// sequenceIndex answers only "which digit run is the track number?" - grouping
// does not consult it, so a set it rejects simply falls back to sorted order.
func TestSequenceIndex(t *testing.T) {
	seq := func(names ...string) bool {
		fs := make([]ScannedFile, len(names))
		for i, n := range names {
			fs[i] = ScannedFile{AbsPath: n, Ext: filepath.Ext(n)}
		}
		_, ok := sequenceIndex(fs)
		return ok
	}
	if !seq("01.mp3", "02.mp3", "03.mp3") {
		t.Error("numbered run should be numbered")
	}
	if !seq("Part 1.mp3", "Part 2.mp3") {
		t.Error("Part N should be numbered")
	}
	if seq("Mort.m4b", "Reaper Man.m4b") {
		t.Error("names without digits carry no track numbers")
	}
	if seq("Chapter 5.mp3", "Chapter 5.mp3") {
		t.Error("a repeated run is a constant, not an index")
	}
	// A missing file leaves a gap; the tracks that remain keep their numbers.
	if !seq("01.mp3", "03.mp3") {
		t.Error("a gapped run is still a track numbering")
	}
	// Not a track numbering: nothing here can be track one.
	if seq("02.mp3", "03.mp3", "04.mp3") {
		t.Error("run starting above 1 is not a track numbering")
	}
	if !seq("00.mp3", "01.mp3", "02.mp3") {
		t.Error("0-based run should be numbered")
	}
}

// Regression: the track index used to be read from a fixed position (the last
// digit run in the base name). Readarr-style names end in "- MP3 - Readarr", so
// that position was the "3" of "MP3" for every file - 22 identical indexes.
// Grouping no longer depends on that, but the track numbers still do.
func TestGroup_IndexSurroundedByOtherNumbers(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
	}{
		{"readarr trailing codec", "Book/Tarnished Knight (2012) (%02d) - MP3 - Readarr.mp3"},
		{"bitrate after index", "Book/Some Title - %02d - 64kbps.mp3"},
		{"numbers on both sides", "Book/The 39 Steps 2008 - Part %d - 128 kbps 2ch.mp3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			var files []string
			for i := 1; i <= 22; i++ {
				files = append(files, fmt.Sprintf(tc.pattern, i))
			}
			mk(t, root, files...)
			units, err := Group(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			eq(t, summarize(units), []string{"dir:Book:multi:22"})
			// Keyed by name, not by position: the track must come from the
			// index in the filename, and unpadded names do not sort into it.
			got := map[string]int{}
			for _, row := range fileRows(units[0]) {
				got[row.RelPath] = row.Track
			}
			for i := 1; i <= 22; i++ {
				rel := strings.TrimPrefix(fmt.Sprintf(tc.pattern, i), "Book/")
				if got[rel] != i {
					t.Fatalf("%s: track %d, want %d", rel, got[rel], i)
				}
			}
		})
	}
}

// Regression: one folder holding several unrelated naming schemes. The only
// digit run every name shares counting from the front is a series number or a
// year that repeats across files, so the left-aligned pass finds no track
// numbering. The real track number is the last run in every name; the
// right-aligned pass must pick it up rather than leaving the files numbered by
// alphabetical path order (which puts "The Horus Heresy ..." first).
func TestGroup_MixedNamingSchemesNumberedByTrailingRun(t *testing.T) {
	root := t.TempDir()
	want := map[string]int{}
	add := func(rel string, track int) {
		mk(t, root, "Book/"+rel)
		want[rel] = track
	}
	for i := 1; i <= 14; i++ {
		add(fmt.Sprintf("The_Reflection_Crackd_%02d.mp3", i), i)
	}
	for i := 15; i <= 17; i++ {
		add(fmt.Sprintf("The Horus Heresy 20 - The Primarchs (2017) - %03d.mp3", i), i)
	}
	for i := 18; i <= 26; i++ {
		add(fmt.Sprintf("The_Lion_%d.mp3", i), i)
	}
	for i := 27; i <= 30; i++ {
		add(fmt.Sprintf("The_Serpent_Beneath_%d.mp3", i), i)
	}

	units, err := Group(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 1 {
		t.Fatalf("want 1 unit, got %v", summarize(units))
	}
	for _, row := range fileRows(units[0]) {
		if want[row.RelPath] != row.Track {
			t.Fatalf("%s: track %d, want %d", row.RelPath, row.Track, want[row.RelPath])
		}
	}
}

// The found index, not the file's position, is what numbers the tracks.
func TestSequenceIndex_Values(t *testing.T) {
	seqOf := func(names ...string) []int {
		fs := make([]ScannedFile, len(names))
		for i, n := range names {
			fs[i] = ScannedFile{AbsPath: n, Ext: filepath.Ext(n)}
		}
		idx, ok := sequenceIndex(fs)
		if !ok {
			t.Fatalf("%v: expected a sequence", names)
		}
		return idx
	}
	want := func(got, exp []int) {
		t.Helper()
		if len(got) != len(exp) {
			t.Fatalf("got %v, want %v", got, exp)
		}
		for i := range got {
			if got[i] != exp[i] {
				t.Fatalf("got %v, want %v", got, exp)
			}
		}
	}
	// The index is the middle run, not the year and not the "3" of MP3.
	want(seqOf(
		"Tarnished Knight (2012) (01) - MP3 - Readarr.mp3",
		"Tarnished Knight (2012) (02) - MP3 - Readarr.mp3",
		"Tarnished Knight (2012) (03) - MP3 - Readarr.mp3",
	), []int{1, 2, 3})
	// A 0-based run on disk is still tracks 1..N in the book.
	want(seqOf("00.mp3", "01.mp3", "02.mp3"), []int{1, 2, 3})
	// Files carrying differing numbers of trailing runs still align on the left.
	want(seqOf("01 - Intro.mp3", "02 - Chapter 1.mp3", "03 - Chapter 2 (alt).mp3"), []int{1, 2, 3})
}

// A numbered set with no track tags must be numbered by its filename index,
// not by the first number in the name (which is the year here).
func TestFileRows_TrackFromSequenceIndex(t *testing.T) {
	root := t.TempDir()
	mk(t, root,
		"Book/Tarnished Knight (2012) (01) - MP3 - Readarr.mp3",
		"Book/Tarnished Knight (2012) (02) - MP3 - Readarr.mp3",
		"Book/Tarnished Knight (2012) (03) - MP3 - Readarr.mp3",
	)
	units, err := Group(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 1 {
		t.Fatalf("want 1 unit, got %v", summarize(units))
	}
	for i, row := range fileRows(units[0]) {
		if row.Track != i+1 {
			t.Fatalf("%s: track %d, want %d", row.RelPath, row.Track, i+1)
		}
	}
}

// Tracks are renumbered across merged discs in DISC-NUMBER order. Ten discs
// catch the trap: sorting the paths as strings puts CD10 between CD1 and CD2.
func TestGroup_DiscMergeNumbersTracksInDiscOrder(t *testing.T) {
	root := t.TempDir()
	var files []string
	for disc := 1; disc <= 10; disc++ {
		for track := 1; track <= 2; track++ {
			files = append(files, fmt.Sprintf("The Stand/CD%d/%02d.mp3", disc, track))
		}
	}
	mk(t, root, files...)
	units, err := Group(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 1 {
		t.Fatalf("want 1 merged unit, got %v", summarize(units))
	}
	got := map[string]int{}
	for _, row := range fileRows(units[0]) {
		got[row.RelPath] = row.Track
	}
	n := 0
	for disc := 1; disc <= 10; disc++ {
		for track := 1; track <= 2; track++ {
			n++
			rel := fmt.Sprintf("CD%d/%02d.mp3", disc, track)
			if got[rel] != n {
				t.Fatalf("%s: track %d, want %d", rel, got[rel], n)
			}
		}
	}
}

// A file whose name carries a digit run too long for an int must not shift its
// remaining runs out of alignment with its siblings'.
func TestSequenceIndex_OversizedDigitRun(t *testing.T) {
	fs := []ScannedFile{
		{AbsPath: "Book 99999999999999999999 - 01.mp3", Ext: ".mp3"},
		{AbsPath: "Book 99999999999999999999 - 02.mp3", Ext: ".mp3"},
		{AbsPath: "Book 99999999999999999999 - 03.mp3", Ext: ".mp3"},
	}
	idx, ok := sequenceIndex(fs)
	if !ok {
		t.Fatal("expected a sequence")
	}
	for i, v := range idx {
		if v != i+1 {
			t.Fatalf("got %v, want [1 2 3]", idx)
		}
	}
}

// One folder is one book, whatever the filenames look like. Each case here used
// to be split into a book per file.
func TestGroup_FolderIsAlwaysOneBook(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		want  string
	}{
		{
			name:  "distinct titles, no numbering",
			files: []string{"Book/Guards Guards.m4b", "Book/Mort.m4b", "Book/Reaper Man.m4b"},
			want:  "dir:Book:multi:3",
		},
		{
			name:  "a track is missing",
			files: []string{"Book/01.mp3", "Book/03.mp3"},
			want:  "dir:Book:multi:2",
		},
		{
			name:  "numbering does not start at one",
			files: []string{"Book/02.mp3", "Book/03.mp3", "Book/04.mp3"},
			want:  "dir:Book:multi:3",
		},
		{
			name:  "mixed formats",
			files: []string{"Book/01.mp3", "Book/02.mp3", "Book/bonus.m4b"},
			want:  "dir:Book:multi:3",
		},
		{
			name:  "a numbered series sharing a folder",
			files: []string{"Book/Fleet 01 - Dauntless (2006).m4b", "Book/Fleet 02 - Fearless (2007).m4b"},
			want:  "dir:Book:multi:2",
		},
		{
			name:  "lone file",
			files: []string{"Book/Victorious.m4b"},
			want:  "dir:Book:single:1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			mk(t, root, tc.files...)
			units, err := Group(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			eq(t, summarize(units), []string{tc.want})
		})
	}
}

// A book missing a file keeps the track numbers its filenames carry, rather
// than being renumbered 1..N and silently disagreeing with the audio.
func TestFileRows_GappedTracksKeepTheirNumbers(t *testing.T) {
	root := t.TempDir()
	mk(t, root, "Book/01.mp3", "Book/02.mp3", "Book/04.mp3")
	units, err := Group(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"01.mp3": 1, "02.mp3": 2, "04.mp3": 4}
	for _, row := range fileRows(units[0]) {
		if row.Track != want[row.RelPath] {
			t.Fatalf("%s: track %d, want %d", row.RelPath, row.Track, want[row.RelPath])
		}
	}
}
