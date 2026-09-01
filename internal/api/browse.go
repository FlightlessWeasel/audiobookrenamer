package api

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// maxBrowseEntries caps one directory listing. A folder with tens of thousands
// of children would otherwise build a response no one can use and that costs
// real memory to marshal; the UI reports the truncation instead.
const maxBrowseEntries = 2000

// dirEntry is one selectable folder.
type dirEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// dirListing is the response of GET /api/browse.
type dirListing struct {
	// Path is the folder that was listed, "" for the root listing (drives on
	// Windows, "/" elsewhere).
	Path string `json:"path"`
	// Parent is the folder to go up to, "" when Path is already a filesystem
	// root and the caller should ask for the root listing instead.
	Parent    string     `json:"parent"`
	Entries   []dirEntry `json:"entries"`
	Truncated bool       `json:"truncated,omitempty"`
}

// browseDirs lists the folders directly inside ?path= so the UI can offer a
// folder picker for a library root.
//
// This has to be a server-side listing. A browser file input only ever yields
// file names and an opaque handle, never a path the server can open, and the
// server frequently runs somewhere else entirely — another host, or a container
// where the library lives at /audiobooks and no such path exists on the machine
// running the browser. The picker must therefore walk the server's filesystem.
//
// It is mounted inside the authenticated group. With auth disabled (the
// default) it lets any caller enumerate directory names on the host, which is
// the same exposure the existing "create a library at any path and scan it"
// flow already carries — see the deployment warning in the README.
func (s *Server) browseDirs(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.URL.Query().Get("path"))
	if raw == "" {
		writeJSON(w, http.StatusOK, dirListing{Entries: filesystemRoots()})
		return
	}

	p := filepath.Clean(raw)
	if !filepath.IsAbs(p) {
		writeErr(w, http.StatusBadRequest, "path must be absolute")
		return
	}

	entries, err := os.ReadDir(p)
	if err != nil {
		switch {
		case errors.Is(err, fs.ErrNotExist):
			writeErr(w, http.StatusNotFound, "no such folder")
		case errors.Is(err, fs.ErrPermission):
			writeErr(w, http.StatusForbidden, "permission denied reading that folder")
		default:
			writeErr(w, http.StatusBadRequest, "cannot list that folder: "+err.Error())
		}
		return
	}

	out := make([]dirEntry, 0, len(entries))
	truncated := false
	for _, e := range entries {
		if len(out) == maxBrowseEntries {
			truncated = true
			break
		}
		// Hidden and system-ish folders are noise in a library picker. A user
		// who really wants one can still type the path.
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if !isBrowsableDir(p, e) {
			continue
		}
		out = append(out, dirEntry{Name: e.Name(), Path: filepath.Join(p, e.Name())})
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})

	writeJSON(w, http.StatusOK, dirListing{
		Path:      p,
		Parent:    parentDir(p),
		Entries:   out,
		Truncated: truncated,
	})
}

// isBrowsableDir reports whether e is a directory the picker should offer.
// A symlink is followed, because a library root reached through one is
// perfectly ordinary; a link that cannot be resolved is not a directory anyone
// can select, so it is left out of the listing rather than failing it.
func isBrowsableDir(parent string, e fs.DirEntry) bool {
	if e.IsDir() {
		return true
	}
	if e.Type()&fs.ModeSymlink == 0 {
		return false
	}
	fi, err := os.Stat(filepath.Join(parent, e.Name()))
	return err == nil && fi.IsDir()
}

// parentDir is the folder above p, or "" when p is already a filesystem root
// (filepath.Dir is its own fixed point there).
func parentDir(p string) string {
	up := filepath.Dir(p)
	if up == p {
		return ""
	}
	return up
}

// filesystemRoots is the starting listing: every drive that exists on Windows,
// "/" elsewhere, plus the account's home directory as a shortcut.
func filesystemRoots() []dirEntry {
	var out []dirEntry
	if runtime.GOOS == "windows" {
		for d := 'A'; d <= 'Z'; d++ {
			p := string(d) + `:\`
			if _, err := os.Stat(p); err == nil {
				out = append(out, dirEntry{Name: p, Path: p})
			}
		}
	} else {
		out = append(out, dirEntry{Name: "/", Path: "/"})
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		out = append(out, dirEntry{Name: "Home — " + home, Path: home})
	}
	return out
}
