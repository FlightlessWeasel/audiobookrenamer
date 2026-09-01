// Package db opens the SQLite database, runs embedded migrations, and provides
// the query layer used by the rest of the app.
//
// It uses modernc.org/sqlite (pure Go, no CGO) so the binary cross-compiles
// cleanly for Linux, macOS, and Windows.
package db

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// DB wraps *sql.DB with the app's queries.
type DB struct {
	*sql.DB
}

// Open opens (creating if needed) the SQLite database at path, applies pragmas,
// and runs any pending migrations.
func Open(path string) (*DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	dsn := sqliteURI(abs) +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	sqlDB.SetMaxOpenConns(1) // serialize writers; WAL still allows concurrent reads via this pool
	if err := sqlDB.Ping(); err != nil {
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	d := &DB{sqlDB}
	if err := d.migrate(); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return d, nil
}

// sqliteURI turns an absolute filesystem path into a SQLite "file:" URI.
//
// modernc.org/sqlite always opens with SQLITE_OPEN_URI, so the path portion is
// percent-decoded by SQLite's URI parser: a literal "%", "#", "?" or space in
// the path must be encoded or the filename is mis-parsed (or, per the SQLite
// spec, undefined). net/url does exactly that encoding for us in path mode
// while leaving "/" and ":" intact. A leading "/" is added for drive-letter
// paths ("C:/x" -> "/C:/x") so the result is always the well-formed
// three-slash form "file:///...".
func sqliteURI(path string) string {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	u := url.URL{Scheme: "file", Path: p}
	return u.String()
}

func (d *DB) migrate() error {
	if _, err := d.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (name TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		var exists int
		if err := d.QueryRow(`SELECT COUNT(1) FROM schema_migrations WHERE name = ?`, name).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := d.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)`, name, now()); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// dbtx is the subset of *sql.DB / *sql.Tx used by the query helpers, so the
// same helper can run standalone (autocommit) or inside a transaction.
// Query is deliberately absent: no helper taking a dbtx runs a multi-row query,
// and an unused method on the interface is a method every future implementation
// has to satisfy for nothing.
type dbtx interface {
	Exec(query string, args ...any) (sql.Result, error)
	QueryRow(query string, args ...any) *sql.Row
}

// IsUniqueViolation reports whether err is a SQLite uniqueness-constraint
// failure, so a caller can turn a duplicate insert into a 409 without matching
// on driver message text (which changes between driver versions and would also
// fire on an unrelated error that happens to mention "UNIQUE").
func IsUniqueViolation(err error) bool {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return false
	}
	switch se.Code() {
	case sqliteConstraintUnique, sqliteConstraintPrimaryKey:
		return true
	}
	return false
}

// Extended SQLite result codes; see https://sqlite.org/rescode.html.
const (
	sqliteConstraintUnique     = 2067 // SQLITE_CONSTRAINT_UNIQUE
	sqliteConstraintPrimaryKey = 1555 // SQLITE_CONSTRAINT_PRIMARYKEY
)

// affected returns the number of rows a statement changed, surfacing the
// driver's error instead of discarding it. `n, _ := res.RowsAffected()` reports
// 0 on a driver failure, which callers then translate into a spurious
// ErrNotFound — a real failure disguised as a missing row.
func affected(res sql.Result) (int64, error) {
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read rows affected: %w", err)
	}
	return n, nil
}

// InTx runs fn inside a transaction, committing on success and rolling back on
// any error or panic.
func (d *DB) InTx(fn func(*sql.Tx) error) error {
	tx, err := d.DB.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// now returns the current time in the RFC3339 form stored in TEXT columns.
func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// parseTime parses a stored RFC3339 timestamp, returning the zero time on error.
func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
