// Package pathguard holds the shared containment checks that keep every
// filesystem mutation this app performs inside the library root it belongs to.
// The organize executor uses WithinRoot/ResolvedWithinRoot to police its move
// endpoints; RemoveWithin is the same guard wrapped around a delete.
package pathguard

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ErrRefused wraps every RemoveWithin failure that is a containment refusal
// rather than an I/O error: the target is the root, escapes the root, or is a
// symlink. Callers map it to a client error (a bad request), not a 500.
var ErrRefused = errors.New("delete refused by containment guard")

// caseInsensitiveFS reports whether path comparison on this OS must ignore
// case. Windows and (by default) macOS fold case; Linux and the BSDs do not.
func caseInsensitiveFS() bool {
	return runtime.GOOS == "windows" || runtime.GOOS == "darwin"
}

// WithinRoot reports whether p is root itself or a path nested under it (no
// ".." escape). Both paths are cleaned lexically; it does not resolve symlinks.
func WithinRoot(root, p string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(p))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// ResolvedWithinRoot is WithinRoot after resolving symlinks: the library root
// and the deepest existing ancestor of p are both passed through
// filepath.EvalSymlinks, so a symlinked parent directory cannot be used to
// escape the root. p itself need not exist yet.
func ResolvedWithinRoot(root, p string) bool {
	realRoot, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		return false
	}
	probe := filepath.Clean(p)
	for {
		if resolved, err := filepath.EvalSymlinks(probe); err == nil {
			rest, err := filepath.Rel(probe, filepath.Clean(p))
			if err != nil {
				return false
			}
			return WithinRoot(realRoot, filepath.Join(resolved, rest))
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return false
		}
		probe = parent
	}
}

// SamePath reports whether a and b name the same location, resolving symlinks
// when both sides exist and falling back to a cleaned lexical compare when they
// do not. It is used to refuse a delete whose target is the library root.
func SamePath(a, b string) bool {
	ca, cb := filepath.Clean(a), filepath.Clean(b)
	if ra, err := filepath.EvalSymlinks(ca); err == nil {
		if rb, err := filepath.EvalSymlinks(cb); err == nil {
			ca, cb = ra, rb
		}
	}
	if ca == cb {
		return true
	}
	// On a case-insensitive filesystem (Windows, default macOS) paths that
	// differ only by case name the same location. On Linux they do not, so the
	// fold there would wrongly equate a real subdirectory with the root.
	return caseInsensitiveFS() && strings.EqualFold(ca, cb)
}

// RemoveWithin deletes target, refusing unless it resolves to a location
// strictly inside root: the root itself is refused, a target that escapes via a
// ".." component or a symlinked parent is refused, and target being a symlink
// is refused (the link is never followed for deletion). A target that is
// already gone is not an error. recursive selects os.RemoveAll over os.Remove.
func RemoveWithin(root, target string, recursive bool) error {
	clean := filepath.Clean(target)

	if SamePath(root, clean) {
		return fmt.Errorf("%w: %s is the library root", ErrRefused, target)
	}
	if !WithinRoot(root, clean) || !ResolvedWithinRoot(root, clean) {
		return fmt.Errorf("%w: %s resolves outside the library root", ErrRefused, target)
	}

	fi, err := os.Lstat(clean)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s is a symlink", ErrRefused, target)
	}

	if recursive {
		return os.RemoveAll(clean)
	}
	return os.Remove(clean)
}

// PruneEmptyParents removes now-empty directories walking up from startDir. It
// is best-effort tidy-up run after a delete: it stops at root (never removed),
// at any path outside root, and at the first directory os.Remove will not take
// — a non-empty one (an untracked cover image, a sibling book's loose file), or
// one it lacks permission to remove. It never reports an error; the caller's
// real work is the delete that already happened.
func PruneEmptyParents(root, startDir string) {
	dir := filepath.Clean(startDir)
	root = filepath.Clean(root)
	for dir != root && WithinRoot(root, dir) && !SamePath(root, dir) {
		if err := os.Remove(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return
		}
		dir = filepath.Dir(dir)
	}
}
