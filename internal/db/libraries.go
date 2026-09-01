package db

import (
	"database/sql"
	"errors"
	"fmt"

	"audiobookrenamer/internal/model"

	"github.com/google/uuid"
)

// ErrNotFound is returned when a lookup by id matches no row.
var ErrNotFound = errors.New("not found")

const libraryCols = `id, name, root_path, structure_mode, author_folder_mode, file_template, multi_file_template, enabled, created_at, updated_at`

func scanLibrary(s interface{ Scan(...any) error }) (model.Library, error) {
	var l model.Library
	var enabled int
	var created, updated string
	err := s.Scan(&l.ID, &l.Name, &l.RootPath, &l.StructureMode, &l.AuthorFolderMode, &l.FileTemplate, &l.MultiFileTemplate, &enabled, &created, &updated)
	if err != nil {
		return model.Library{}, err
	}
	l.Enabled = enabled != 0
	l.CreatedAt = parseTime(created)
	l.UpdatedAt = parseTime(updated)
	return l, nil
}

// ListLibraries returns all libraries ordered by name.
func (d *DB) ListLibraries() ([]model.Library, error) {
	rows, err := d.Query(`SELECT ` + libraryCols + ` FROM libraries ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Library
	for rows.Next() {
		l, err := scanLibrary(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// GetLibrary returns one library by id, or ErrNotFound.
func (d *DB) GetLibrary(id string) (model.Library, error) {
	row := d.QueryRow(`SELECT `+libraryCols+` FROM libraries WHERE id = ?`, id)
	l, err := scanLibrary(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Library{}, ErrNotFound
	}
	return l, err
}

// normalizeLibrary fills in defaults for an empty StructureMode, author
// folder mode, or template.
// Both CreateLibrary and UpdateLibrary run it, so a persisted library always has
// usable templates: an empty file_template would otherwise render every filename
// to "Unknown"+ext (SanitizeSegment("") => "Unknown"). This is also what lets a
// PATCH send file_template:"" to mean "reset to the default".
func normalizeLibrary(l model.Library) model.Library {
	if l.StructureMode == "" {
		l.StructureMode = model.AuthorFirst
	}
	if l.AuthorFolderMode == "" {
		l.AuthorFolderMode = model.AuthorFolderSort
	}
	if l.FileTemplate == "" {
		l.FileTemplate = model.DefaultFileTemplate
	}
	if l.MultiFileTemplate == "" {
		l.MultiFileTemplate = model.DefaultMultiFileTemplate
	}
	return l
}

// CreateLibrary inserts l, assigning an id and timestamps. Defaults are applied
// for empty StructureMode and templates.
func (d *DB) CreateLibrary(l model.Library) (model.Library, error) {
	l.ID = uuid.NewString()
	l = normalizeLibrary(l)
	ts := now()
	_, err := d.Exec(
		`INSERT INTO libraries (`+libraryCols+`) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		l.ID, l.Name, l.RootPath, l.StructureMode, l.AuthorFolderMode, l.FileTemplate, l.MultiFileTemplate, boolToInt(l.Enabled), ts, ts,
	)
	if err != nil {
		return model.Library{}, fmt.Errorf("insert library: %w", err)
	}
	l.CreatedAt = parseTime(ts)
	l.UpdatedAt = parseTime(ts)
	return l, nil
}

// UpdateLibrary writes the mutable fields of l (matched by l.ID) and bumps
// updated_at.
func (d *DB) UpdateLibrary(l model.Library) (model.Library, error) {
	l = normalizeLibrary(l)
	ts := now()
	res, err := d.Exec(
		`UPDATE libraries SET name=?, root_path=?, structure_mode=?, author_folder_mode=?, file_template=?, multi_file_template=?, enabled=?, updated_at=? WHERE id=?`,
		l.Name, l.RootPath, l.StructureMode, l.AuthorFolderMode, l.FileTemplate, l.MultiFileTemplate, boolToInt(l.Enabled), ts, l.ID,
	)
	if err != nil {
		return model.Library{}, err
	}
	n, err := affected(res)
	if err != nil {
		return model.Library{}, err
	}
	if n == 0 {
		return model.Library{}, ErrNotFound
	}
	return d.GetLibrary(l.ID)
}

// DeleteLibrary removes a library and (via cascade) its books.
func (d *DB) DeleteLibrary(id string) error {
	res, err := d.Exec(`DELETE FROM libraries WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, err := affected(res)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
