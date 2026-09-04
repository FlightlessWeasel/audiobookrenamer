package pathguard

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSamePath(t *testing.T) {
	root := t.TempDir()
	if !SamePath(root, filepath.Join(root, "sub", "..")) {
		t.Error("SamePath should equate a path with its own cleaned form")
	}
	if SamePath(root, filepath.Join(root, "sub")) {
		t.Error("SamePath must not equate root with a subdirectory")
	}
	// A case-variant of a real subdirectory must never be read as the root on a
	// case-sensitive filesystem.
	folded := SamePath(filepath.FromSlash("/lib/Books"), filepath.FromSlash("/lib/books"))
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		if !folded {
			t.Error("SamePath should fold case on a case-insensitive OS")
		}
	} else if folded {
		t.Error("SamePath must not fold case on a case-sensitive OS")
	}
}

func TestRemoveWithin_RefusalsAreErrRefused(t *testing.T) {
	root := t.TempDir()
	if err := RemoveWithin(root, root, true); !errors.Is(err, ErrRefused) {
		t.Errorf("deleting the root: got %v, want ErrRefused", err)
	}
	if err := RemoveWithin(root, filepath.Join(root, "..", "escape"), true); !errors.Is(err, ErrRefused) {
		t.Errorf("escaping path: got %v, want ErrRefused", err)
	}
}

func TestWithinRoot(t *testing.T) {
	root := filepath.FromSlash("/lib")
	cases := []struct {
		p    string
		want bool
	}{
		{filepath.FromSlash("/lib"), true},
		{filepath.FromSlash("/lib/a/b"), true},
		{filepath.FromSlash("/lib/../lib/a"), true},
		{filepath.FromSlash("/lib/.."), false},
		{filepath.FromSlash("/other"), false},
		{filepath.FromSlash("/lib/../other"), false},
	}
	for _, c := range cases {
		if got := WithinRoot(root, c.p); got != c.want {
			t.Errorf("WithinRoot(%q, %q) = %v, want %v", root, c.p, got, c.want)
		}
	}
}

func TestRemoveWithin_RecursiveFolder(t *testing.T) {
	root := t.TempDir()
	book := filepath.Join(root, "Author", "Book")
	if err := os.MkdirAll(filepath.Join(book, "CD1"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(book, "CD1", "01.mp3"), "x")
	writeFile(t, filepath.Join(book, "cover.jpg"), "x")

	if err := RemoveWithin(root, book, true); err != nil {
		t.Fatalf("RemoveWithin: %v", err)
	}
	if _, err := os.Stat(book); !os.IsNotExist(err) {
		t.Fatalf("book dir still present: %v", err)
	}
}

func TestRemoveWithin_RefusesRoot(t *testing.T) {
	root := t.TempDir()
	if err := RemoveWithin(root, root, true); err == nil {
		t.Fatal("expected RemoveWithin to refuse the root itself")
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("root should be untouched: %v", err)
	}
}

func TestRemoveWithin_RefusesOutside(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	victim := filepath.Join(outside, "keep.txt")
	writeFile(t, victim, "x")

	if err := RemoveWithin(root, filepath.Join(root, "..", filepath.Base(outside), "keep.txt"), false); err == nil {
		t.Fatal("expected RemoveWithin to refuse a path outside root")
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("outside file should be untouched: %v", err)
	}
}

func TestRemoveWithin_RefusesSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is privileged on Windows")
	}
	root := t.TempDir()
	target := t.TempDir()
	writeFile(t, filepath.Join(target, "keep.txt"), "x")
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := RemoveWithin(root, link, true); err == nil {
		t.Fatal("expected RemoveWithin to refuse a symlink target")
	}
	if _, err := os.Stat(filepath.Join(target, "keep.txt")); err != nil {
		t.Fatalf("symlinked tree should be untouched: %v", err)
	}
}

func TestRemoveWithin_MissingIsNoError(t *testing.T) {
	root := t.TempDir()
	if err := RemoveWithin(root, filepath.Join(root, "gone"), true); err != nil {
		t.Fatalf("RemoveWithin on a missing path: %v", err)
	}
}

func TestPruneEmptyParents(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "Author", "Series", "Book")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	// A sibling under Author keeps that level from being pruned.
	writeFile(t, filepath.Join(root, "Author", "other.txt"), "x")

	PruneEmptyParents(root, deep)

	if _, err := os.Stat(deep); !os.IsNotExist(err) {
		t.Errorf("empty Book dir should be gone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "Author", "Series")); !os.IsNotExist(err) {
		t.Errorf("empty Series dir should be gone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "Author")); err != nil {
		t.Errorf("non-empty Author dir must be kept: %v", err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Errorf("root must never be pruned: %v", err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
