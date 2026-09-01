package db

import (
	"database/sql"
	"encoding/json"
	"errors"

	"audiobookrenamer/internal/model"

	"github.com/google/uuid"
)

// ReplaceCandidates swaps the stored candidate set for a book. Candidates are
// expected pre-ranked (index 0 = best); rank is stored as the slice position.
func (d *DB) ReplaceCandidates(bookID string, cands []model.Candidate) error {
	return d.InTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM candidates WHERE book_id = ?`, bookID); err != nil {
			return err
		}
		stmt, err := tx.Prepare(
			`INSERT INTO candidates (id, book_id, provider, provider_id, payload_json, score, rank)
			 VALUES (?,?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for i, c := range cands {
			payload, err := json.Marshal(c)
			if err != nil {
				return err
			}
			if _, err := stmt.Exec(uuid.NewString(), bookID, c.Provider, c.ProviderID, string(payload), c.Score, i); err != nil {
				return err
			}
		}
		return nil
	})
}

// ListCandidates returns a book's stored candidates in rank order.
func (d *DB) ListCandidates(bookID string) ([]model.Candidate, error) {
	rows, err := d.Query(
		`SELECT payload_json, score FROM candidates WHERE book_id = ? ORDER BY rank`, bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Candidate
	for rows.Next() {
		var payload string
		var score float64
		if err := rows.Scan(&payload, &score); err != nil {
			return nil, err
		}
		var c model.Candidate
		if err := json.Unmarshal([]byte(payload), &c); err != nil {
			return nil, err
		}
		c.Score = score
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetCandidate returns one stored candidate by provider + provider id.
func (d *DB) GetCandidate(bookID, provider, providerID string) (model.Candidate, error) {
	row := d.QueryRow(
		`SELECT payload_json, score FROM candidates WHERE book_id = ? AND provider = ? AND provider_id = ?`,
		bookID, provider, providerID)
	var payload string
	var score float64
	err := row.Scan(&payload, &score)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Candidate{}, ErrNotFound
	}
	if err != nil {
		return model.Candidate{}, err
	}
	var c model.Candidate
	if err := json.Unmarshal([]byte(payload), &c); err != nil {
		return model.Candidate{}, err
	}
	c.Score = score
	return c, nil
}
