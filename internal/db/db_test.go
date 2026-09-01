package db

import (
	"path/filepath"
	"runtime"
	"testing"
)

// Open must build a working DSN for paths containing URI-significant
// characters. modernc.org/sqlite always opens with SQLITE_OPEN_URI, so the
// path portion is percent-decoded — a literal space, "&", "%" or "#" that
// isn't encoded corrupts the filename.
func TestOpen_PathsWithSpecialChars(t *testing.T) {
	names := []string{
		"plain.db",
		"with space.db",
		"amp & sand.db",
		"pct%20literal.db",
		"hash#tag.db",
		"all the &%# things.db",
	}
	for _, name := range names {
		name := name
		t.Run(name, func(t *testing.T) {
			d, err := Open(filepath.Join(t.TempDir(), name))
			if err != nil {
				t.Fatalf("Open(%q): %v", name, err)
			}
			defer d.Close()
			if _, err := d.Exec(`CREATE TABLE t (x TEXT)`); err != nil {
				t.Fatalf("CREATE: %v", err)
			}
			if _, err := d.Exec(`INSERT INTO t (x) VALUES ('hi')`); err != nil {
				t.Fatalf("INSERT: %v", err)
			}
			var got string
			if err := d.QueryRow(`SELECT x FROM t`).Scan(&got); err != nil {
				t.Fatalf("SELECT: %v", err)
			}
			if got != "hi" {
				t.Fatalf("round-trip: got %q", got)
			}
		})
	}
}

func TestSqliteURI(t *testing.T) {
	// POSIX-shaped paths behave the same on every platform.
	posix := map[string]string{
		"/home/u/a.db":     "file:///home/u/a.db",
		"/home/u/a b.db":   "file:///home/u/a%20b.db",
		"/home/u/a#b%c.db": "file:///home/u/a%23b%25c.db",
		"/home/u/a & b.db": "file:///home/u/a%20&%20b.db",
	}
	for in, want := range posix {
		if got := sqliteURI(in); got != want {
			t.Errorf("sqliteURI(%q) = %q, want %q", in, got, want)
		}
	}

	if runtime.GOOS == "windows" {
		win := map[string]string{
			`C:\u\a.db`:     "file:///C:/u/a.db",
			`C:\u\a#b%c.db`: "file:///C:/u/a%23b%25c.db",
		}
		for in, want := range win {
			if got := sqliteURI(in); got != want {
				t.Errorf("sqliteURI(%q) = %q, want %q", in, got, want)
			}
		}
	}
}
