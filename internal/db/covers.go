package db

import (
	"database/sql"
	"errors"
	"time"
)

// BookCover is the cached cover image for a matched book.
type BookCover struct {
	BookID    string
	MIME      string
	Data      []byte
	SourceURL string // the candidate cover_url this image was fetched from
	FetchedAt time.Time
}

// GetBookCover returns a book's cached cover, or ok=false if none is stored.
func (d *DB) GetBookCover(bookID string) (BookCover, bool, error) {
	var c BookCover
	var fetched string
	c.BookID = bookID
	err := d.QueryRow(
		`SELECT mime, data, source_url, fetched_at FROM book_covers WHERE book_id = ?`, bookID,
	).Scan(&c.MIME, &c.Data, &c.SourceURL, &fetched)
	if errors.Is(err, sql.ErrNoRows) {
		return BookCover{}, false, nil
	}
	if err != nil {
		return BookCover{}, false, err
	}
	c.FetchedAt = parseTime(fetched)
	return c, true, nil
}

// SetBookCover stores (replacing any existing row) the cover fetched from
// sourceURL for bookID.
func (d *DB) SetBookCover(bookID, mime string, data []byte, sourceURL string) error {
	_, err := d.Exec(
		`INSERT INTO book_covers (book_id, mime, data, source_url, fetched_at) VALUES (?,?,?,?,?)
		 ON CONFLICT(book_id) DO UPDATE SET
		   mime = excluded.mime, data = excluded.data,
		   source_url = excluded.source_url, fetched_at = excluded.fetched_at`,
		bookID, mime, data, sourceURL, now(),
	)
	return err
}

// DeleteBookCover removes a book's cached cover, if any. It is not an error
// for none to exist.
func (d *DB) DeleteBookCover(bookID string) error {
	_, err := d.Exec(`DELETE FROM book_covers WHERE book_id = ?`, bookID)
	return err
}
