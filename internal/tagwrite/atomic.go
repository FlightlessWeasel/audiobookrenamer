package tagwrite

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// tmpPrefix names the in-directory scratch file every writer renames into
// place. It matches the ".abr*" family the organize executor already uses for
// its own temp files.
const tmpPrefix = ".abrtag-"

// replaceFile atomically replaces the file at path with the bytes emit writes.
// emit must produce the file's complete new contents; if it returns an error,
// or any later step fails, the original file is left exactly as it was and the
// scratch file is removed.
//
// The new file is fsynced before the rename, so a crash leaves either the intact
// original or the fully-written replacement, never a truncated file. The
// original's permission bits are carried over; owner and mtime are not (a tag
// rewrite is meant to look like a content change to a rescan).
func replaceFile(path string, emit func(w io.Writer) error) (retErr error) {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), tmpPrefix+"*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if retErr != nil {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()

	if err := emit(tmp); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("flush %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	if err := os.Chmod(tmpName, fi.Mode().Perm()); err != nil {
		return fmt.Errorf("chmod %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}
