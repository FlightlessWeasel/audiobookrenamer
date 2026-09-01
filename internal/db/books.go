package db

import (
	"database/sql"
	"errors"
	"strings"

	"audiobookrenamer/internal/model"

	"github.com/google/uuid"
)

const bookCols = `id, library_id, source_dir, source_file, layout, state, message, scan_fingerprint,
	matched_provider, matched_provider_id, match_score,
	title, subtitle, author, author_sort, author_sort_source, narrator, series, series_index, year, asin, isbn, cover_url,
	created_at, updated_at`

func scanBook(s interface{ Scan(...any) error }) (model.Book, error) {
	var b model.Book
	var created, updated string
	err := s.Scan(
		&b.ID, &b.LibraryID, &b.SourceDir, &b.SourceFile, &b.Layout, &b.State, &b.Message, &b.ScanFingerprint,
		&b.MatchedProvider, &b.MatchedProviderID, &b.MatchScore,
		&b.Title, &b.Subtitle, &b.Author, &b.AuthorSort, &b.AuthorSortSource, &b.Narrator, &b.Series, &b.SeriesIndex, &b.Year, &b.ASIN, &b.ISBN, &b.CoverURL,
		&created, &updated,
	)
	if err != nil {
		return model.Book{}, err
	}
	b.CreatedAt = parseTime(created)
	b.UpdatedAt = parseTime(updated)
	return b, nil
}

// BookFilter narrows a ListBooks query. Empty fields are ignored.
type BookFilter struct {
	LibraryID string
	State     model.BookState
	Query     string // matched against title, author, and source path
	Limit     int
}

// ListBooks returns books matching the filter, newest first, without files.
func (d *DB) ListBooks(f BookFilter) ([]model.Book, error) {
	var where []string
	var args []any
	if f.LibraryID != "" {
		where = append(where, "library_id = ?")
		args = append(args, f.LibraryID)
	}
	if f.State != "" {
		where = append(where, "state = ?")
		args = append(args, f.State)
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		like := "%" + escapeLike(q) + "%"
		where = append(where, "(title LIKE ? ESCAPE '\\' OR author LIKE ? ESCAPE '\\' OR source_dir LIKE ? ESCAPE '\\' OR source_file LIKE ? ESCAPE '\\')")
		args = append(args, like, like, like, like)
	}
	sqlStr := `SELECT ` + bookCols + ` FROM books`
	if len(where) > 0 {
		sqlStr += " WHERE " + strings.Join(where, " AND ")
	}
	sqlStr += " ORDER BY updated_at DESC"
	if f.Limit <= 0 || f.Limit > 5000 {
		f.Limit = 2000
	}
	sqlStr += " LIMIT ?"
	args = append(args, f.Limit)

	rows, err := d.Query(sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Book
	for rows.Next() {
		b, err := scanBook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// GetBook returns one book with its files, or ErrNotFound.
func (d *DB) GetBook(id string) (model.Book, error) {
	row := d.QueryRow(`SELECT `+bookCols+` FROM books WHERE id = ?`, id)
	b, err := scanBook(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Book{}, ErrNotFound
	}
	if err != nil {
		return model.Book{}, err
	}
	files, err := d.listBookFiles(id)
	if err != nil {
		return model.Book{}, err
	}
	b.Files = files
	return b, nil
}

func (d *DB) listBookFiles(bookID string) ([]model.BookFile, error) {
	rows, err := d.Query(
		`SELECT id, book_id, rel_path, size, mod_time, ext, track, tag_json
		 FROM book_files WHERE book_id = ? ORDER BY track, rel_path`, bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.BookFile
	for rows.Next() {
		var f model.BookFile
		if err := rows.Scan(&f.ID, &f.BookID, &f.RelPath, &f.Size, &f.ModTime, &f.Ext, &f.Track, &f.TagJSON); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// BooksIndex returns the library's books keyed by their identity
// (source_dir + "\x00" + source_file), for incremental rescans.
func (d *DB) BooksIndex(libraryID string) (map[string]model.Book, error) {
	rows, err := d.Query(`SELECT `+bookCols+` FROM books WHERE library_id = ?`, libraryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]model.Book{}
	for rows.Next() {
		b, err := scanBook(rows)
		if err != nil {
			return nil, err
		}
		out[BookIdentity(b.SourceDir, b.SourceFile)] = b
	}
	return out, rows.Err()
}

// BookIdentity builds the identity key used by BooksIndex and UpsertBook.
func BookIdentity(sourceDir, sourceFile string) string {
	return sourceDir + "\x00" + sourceFile
}

// UpsertBook inserts b or updates the existing row with the same identity,
// returning the stored book. Caller-supplied ID is ignored on update.
func (d *DB) UpsertBook(b model.Book) (model.Book, error) {
	var out model.Book
	err := d.InTx(func(tx *sql.Tx) error {
		id, err := upsertBookTx(tx, b)
		if err != nil {
			return err
		}
		out, err = getBookBareTx(tx, id)
		return err
	})
	return out, err
}

// UpsertBookWithFiles upserts the book row and replaces its file rows in one
// transaction, so a scan can never leave a book whose fingerprint says
// "unchanged" while its file list failed to persist.
func (d *DB) UpsertBookWithFiles(b model.Book, files []model.BookFile) (model.Book, error) {
	var out model.Book
	err := d.InTx(func(tx *sql.Tx) error {
		id, err := upsertBookTx(tx, b)
		if err != nil {
			return err
		}
		if err := replaceBookFilesTx(tx, id, files); err != nil {
			return err
		}
		out, err = getBookBareTx(tx, id)
		return err
	})
	return out, err
}

// upsertBookTx does the insert-or-update and returns the row id.
func upsertBookTx(tx *sql.Tx, b model.Book) (string, error) {
	var existingID string
	err := tx.QueryRow(
		`SELECT id FROM books WHERE library_id = ? AND source_dir = ? AND source_file = ?`,
		b.LibraryID, b.SourceDir, b.SourceFile).Scan(&existingID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		id := uuid.NewString()
		ts := now()
		if b.AuthorSortSource == "" {
			b.AuthorSortSource = model.AuthorSortDerived
		}
		_, err := tx.Exec(
			`INSERT INTO books (`+bookCols+`) VALUES (?,?,?,?,?,?,?,?, ?,?,?, ?,?,?,?,?,?,?,?,?,?,?,?, ?,?)`,
			id, b.LibraryID, b.SourceDir, b.SourceFile, b.Layout, b.State, b.Message, b.ScanFingerprint,
			b.MatchedProvider, b.MatchedProviderID, b.MatchScore,
			b.Title, b.Subtitle, b.Author, b.AuthorSort, b.AuthorSortSource, b.Narrator, b.Series, b.SeriesIndex, b.Year, b.ASIN, b.ISBN, b.CoverURL,
			ts, ts,
		)
		return id, err
	case err != nil:
		return "", err
	}

	if b.AuthorSortSource == "" {
		b.AuthorSortSource = model.AuthorSortDerived
	}
	_, err = tx.Exec(
		`UPDATE books SET layout=?, state=?, message=?, scan_fingerprint=?,
			matched_provider=?, matched_provider_id=?, match_score=?,
			title=?, subtitle=?, author=?, author_sort=?, author_sort_source=?, narrator=?, series=?, series_index=?, year=?, asin=?, isbn=?, cover_url=?,
			updated_at=?
		 WHERE id=?`,
		b.Layout, b.State, b.Message, b.ScanFingerprint,
		b.MatchedProvider, b.MatchedProviderID, b.MatchScore,
		b.Title, b.Subtitle, b.Author, b.AuthorSort, b.AuthorSortSource, b.Narrator, b.Series, b.SeriesIndex, b.Year, b.ASIN, b.ISBN, b.CoverURL,
		now(), existingID,
	)
	return existingID, err
}

func getBookBareTx(tx *sql.Tx, id string) (model.Book, error) {
	b, err := scanBook(tx.QueryRow(`SELECT `+bookCols+` FROM books WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Book{}, ErrNotFound
	}
	return b, err
}

// GetBookBare returns a book without its files.
func (d *DB) GetBookBare(id string) (model.Book, error) {
	row := d.QueryRow(`SELECT `+bookCols+` FROM books WHERE id = ?`, id)
	b, err := scanBook(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Book{}, ErrNotFound
	}
	return b, err
}

// ReplaceBookFiles deletes and re-inserts the file rows for a book.
func (d *DB) ReplaceBookFiles(bookID string, files []model.BookFile) error {
	return d.InTx(func(tx *sql.Tx) error {
		return replaceBookFilesTx(tx, bookID, files)
	})
}

func replaceBookFilesTx(tx *sql.Tx, bookID string, files []model.BookFile) error {
	if _, err := tx.Exec(`DELETE FROM book_files WHERE book_id = ?`, bookID); err != nil {
		return err
	}
	stmt, err := tx.Prepare(
		`INSERT INTO book_files (id, book_id, rel_path, size, mod_time, ext, track, tag_json)
		 VALUES (?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, f := range files {
		if _, err := stmt.Exec(uuid.NewString(), bookID, f.RelPath, f.Size, f.ModTime, f.Ext, f.Track, f.TagJSON); err != nil {
			return err
		}
	}
	return nil
}

// SetBookMatch writes the accepted metadata + match provenance for a book and
// sets its state (typically matched). Only metadata/match/state columns are
// touched; files, layout, and fingerprint are left as-is.
func (d *DB) SetBookMatch(id string, m model.Book) error {
	if m.AuthorSortSource == "" {
		m.AuthorSortSource = model.AuthorSortDerived
	}
	res, err := d.Exec(
		`UPDATE books SET state=?, message=?,
			matched_provider=?, matched_provider_id=?, match_score=?,
			title=?, subtitle=?, author=?, author_sort=?, author_sort_source=?, narrator=?, series=?, series_index=?, year=?, asin=?, isbn=?, cover_url=?,
			updated_at=?
		 WHERE id=?`,
		m.State, m.Message,
		m.MatchedProvider, m.MatchedProviderID, m.MatchScore,
		m.Title, m.Subtitle, m.Author, m.AuthorSort, m.AuthorSortSource, m.Narrator, m.Series, m.SeriesIndex, m.Year, m.ASIN, m.ISBN, m.CoverURL,
		now(), id,
	)
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

// SetBookAuthorSort updates only a book's author_sort value and its provenance
// (author_sort_source). Used by the manual PATCH /api/books/{id} edit.
func (d *DB) SetBookAuthorSort(id, authorSort, source string) error {
	res, err := d.Exec(
		`UPDATE books SET author_sort=?, author_sort_source=?, updated_at=? WHERE id=?`,
		authorSort, source, now(), id,
	)
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

// SetBookState updates only a book's state and message.
func (d *DB) SetBookState(id string, state model.BookState, message string) error {
	res, err := d.Exec(`UPDATE books SET state=?, message=?, updated_at=? WHERE id=?`, state, message, now(), id)
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

// escapeLike escapes the LIKE wildcards ("%", "_") and the escape character
// itself in a user-supplied term, for use with `LIKE ? ESCAPE '\'`.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// deleteBooksChunk keeps each IN (...) list under SQLite's default
// SQLITE_MAX_VARIABLE_NUMBER (999).
const deleteBooksChunk = 900

// DeleteBooks removes books by id (cascades to files and candidates). The id
// list is deleted in chunks of at most deleteBooksChunk parameters, all within
// one transaction so a large prune is still all-or-nothing.
func (d *DB) DeleteBooks(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	return d.InTx(func(tx *sql.Tx) error {
		for start := 0; start < len(ids); start += deleteBooksChunk {
			end := start + deleteBooksChunk
			if end > len(ids) {
				end = len(ids)
			}
			batch := ids[start:end]
			q := `DELETE FROM books WHERE id IN (?` + strings.Repeat(",?", len(batch)-1) + `)`
			args := make([]any, len(batch))
			for i, id := range batch {
				args[i] = id
			}
			if _, err := tx.Exec(q, args...); err != nil {
				return err
			}
		}
		return nil
	})
}

// CountBooksByState returns a state->count map for a library (or all libraries
// when libraryID is "").
func (d *DB) CountBooksByState(libraryID string) (map[string]int, error) {
	q := `SELECT state, COUNT(*) FROM books`
	var args []any
	if libraryID != "" {
		q += ` WHERE library_id = ?`
		args = append(args, libraryID)
	}
	q += ` GROUP BY state`
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{
		string(model.StateUnmatched):   0,
		string(model.StateNeedsReview): 0,
		string(model.StateMatched):     0,
		string(model.StateOrganized):   0,
		string(model.StateError):       0,
	}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[st] = n
	}
	return out, rows.Err()
}
