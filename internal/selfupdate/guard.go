package selfupdate

import (
	"log/slog"
	"os"
	"path/filepath"
)

// CanApply reports whether an in-place self-update is possible for this install,
// and if not, the first failing check's human-readable reason. A false result
// is a hard gate on POST /api/update/apply.
//
// The result is computed once and cached: GET /api/update is unauthenticated and
// hot, and the writability probe touches the filesystem. The set of conditions
// it checks (dev build, container, package manager, read-only mount) does not
// change over a process's lifetime.
func (u *Updater) CanApply() (ok bool, reason string) {
	u.canApplyOnce.Do(func() {
		u.canApplyOK, u.canApplyWhy = u.computeCanApply()
	})
	return u.canApplyOK, u.canApplyWhy
}

func (u *Updater) computeCanApply() (ok bool, reason string) {
	if u.currentVersion == "dev" {
		return false, "development build — build from source or install a release"
	}
	if isContainer() {
		return false, "running in a container — update the image instead"
	}
	if u.execPath == "" {
		return false, "could not locate the running binary"
	}
	dir := filepath.Dir(u.execPath)
	if !dirWritable(dir) {
		slog.Debug("self-update: binary directory is not writable", "dir", dir)
		return false, "the binary's directory is not writable"
	}
	if _, err := os.Stat("/var/lib/dpkg/info/audiobookrenamer.list"); err == nil {
		return false, "installed via apt — use apt to upgrade"
	}
	return true, ""
}

// isContainer is a best-effort check for running inside a container image, where
// the right fix is to pull a new image rather than rewrite the binary.
func isContainer() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	return os.Getenv("ABR_CONTAINER") != ""
}

// dirWritable probes dir by creating and removing a temp file, which also covers
// filesystem-level read-only mounts that a mode check would miss.
func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".abr-update-probe-")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}
