package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func browse(t *testing.T, s *Server, path string) (*httptest.ResponseRecorder, dirListing) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/browse?path="+path, nil)
	rr := httptest.NewRecorder()
	s.browseDirs(rr, req)
	var got dirListing
	if rr.Code == http.StatusOK {
		if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode listing: %v (body: %s)", err, rr.Body.String())
		}
	}
	return rr, got
}

// The picker lists sub-folders only: files would not be selectable as a library
// root, and hidden folders are noise.
func TestBrowse_ListsOnlyVisibleDirectories(t *testing.T) {
	s := newTestServer(t)
	root := t.TempDir()
	for _, d := range []string{"Audiobooks", "Podcasts", ".hidden"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	rr, got := browse(t, s, root)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d (body: %s)", rr.Code, rr.Body.String())
	}
	if got.Path != filepath.Clean(root) {
		t.Errorf("path = %q, want %q", got.Path, root)
	}
	if got.Parent != filepath.Dir(filepath.Clean(root)) {
		t.Errorf("parent = %q, want %q", got.Parent, filepath.Dir(root))
	}

	var names []string
	for _, e := range got.Entries {
		names = append(names, e.Name)
	}
	if len(names) != 2 || names[0] != "Audiobooks" || names[1] != "Podcasts" {
		t.Errorf("entries = %v, want [Audiobooks Podcasts] sorted, no file and no dotfile", names)
	}
	for _, e := range got.Entries {
		if !filepath.IsAbs(e.Path) {
			t.Errorf("entry %q has a relative path %q; the API contract is absolute paths", e.Name, e.Path)
		}
	}
}

// An empty path is the starting listing: something has to be selectable before
// the user knows any path at all.
func TestBrowse_EmptyPathReturnsFilesystemRoots(t *testing.T) {
	s := newTestServer(t)
	rr, got := browse(t, s, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d (body: %s)", rr.Code, rr.Body.String())
	}
	if len(got.Entries) == 0 {
		t.Fatal("root listing is empty; the picker would have nowhere to start")
	}
	if got.Path != "" || got.Parent != "" {
		t.Errorf("root listing should have no path/parent, got %q/%q", got.Path, got.Parent)
	}
	if runtime.GOOS != "windows" {
		if got.Entries[0].Path != "/" {
			t.Errorf("first root = %q, want /", got.Entries[0].Path)
		}
	}
}

// A filesystem root has no parent; reporting itself would make "Up" a no-op the
// user cannot escape.
func TestBrowse_FilesystemRootHasNoParent(t *testing.T) {
	s := newTestServer(t)
	root := "/"
	if runtime.GOOS == "windows" {
		root = filepath.VolumeName(os.Getenv("SystemDrive")) + `\`
		if root == `\` {
			root = `C:\`
		}
	}
	rr, got := browse(t, s, root)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d (body: %s)", rr.Code, rr.Body.String())
	}
	if got.Parent != "" {
		t.Errorf("parent of %q = %q, want empty", root, got.Parent)
	}
}

func TestBrowse_RejectsRelativeAndMissingPaths(t *testing.T) {
	s := newTestServer(t)

	if rr, _ := browse(t, s, "relative/path"); rr.Code != http.StatusBadRequest {
		t.Errorf("relative path: status = %d, want 400", rr.Code)
	}
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if rr, _ := browse(t, s, missing); rr.Code != http.StatusNotFound {
		t.Errorf("missing path: status = %d, want 404", rr.Code)
	}
}
