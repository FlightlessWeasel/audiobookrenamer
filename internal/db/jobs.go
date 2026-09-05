package db

import (
	"database/sql"
	"encoding/json"
	"errors"

	"audiobookrenamer/internal/model"

	"github.com/google/uuid"
)

const jobCols = `id, type, status, library_id, total, done, message, error, payload, created_at, finished_at`

func scanJob(s interface{ Scan(...any) error }) (model.Job, error) {
	var j model.Job
	var created string
	var finished sql.NullString
	err := s.Scan(&j.ID, &j.Type, &j.Status, &j.LibraryID, &j.Total, &j.Done, &j.Message, &j.Error, &j.Payload, &created, &finished)
	if err != nil {
		return model.Job{}, err
	}
	j.CreatedAt = parseTime(created)
	if finished.Valid && finished.String != "" {
		t := parseTime(finished.String)
		j.FinishedAt = &t
	}
	return j, nil
}

// CreateJob inserts a queued job of the given type/library and returns it.
func (d *DB) CreateJob(t model.JobType, libraryID string) (model.Job, error) {
	return d.CreateJobPayload(t, libraryID, "")
}

// CreateJobPayload inserts a queued job carrying a type-specific JSON payload.
func (d *DB) CreateJobPayload(t model.JobType, libraryID, payload string) (model.Job, error) {
	j := model.Job{
		ID:        uuid.NewString(),
		Type:      t,
		Status:    model.JobQueued,
		LibraryID: libraryID,
		Payload:   payload,
	}
	ts := now()
	_, err := d.Exec(
		`INSERT INTO jobs (`+jobCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,NULL)`,
		j.ID, j.Type, j.Status, j.LibraryID, 0, 0, "", "", payload, ts,
	)
	if err != nil {
		return model.Job{}, err
	}
	j.CreatedAt = parseTime(ts)
	return j, nil
}

// ActiveUndoExists reports whether an undo job targeting targetJobID is already
// queued or running, so the API can refuse to enqueue a duplicate.
func (d *DB) ActiveUndoExists(targetJobID string) (bool, error) {
	rows, err := d.Query(
		`SELECT payload FROM jobs WHERE type = ? AND status IN (?, ?)`,
		model.JobUndo, model.JobQueued, model.JobRunning,
	)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return false, err
		}
		var p struct {
			TargetJobID string `json:"target_job_id"`
		}
		if json.Unmarshal([]byte(payload), &p) == nil && p.TargetJobID == targetJobID {
			return true, nil
		}
	}
	return false, rows.Err()
}

// ActiveJobExists reports whether any job of type t is currently queued or
// running, so a handler can refuse to enqueue a duplicate.
func (d *DB) ActiveJobExists(t model.JobType) (bool, error) {
	var n int
	err := d.QueryRow(
		`SELECT COUNT(1) FROM jobs WHERE type = ? AND status IN (?, ?)`,
		t, model.JobQueued, model.JobRunning,
	).Scan(&n)
	return n > 0, err
}

// GetJob returns one job by id, or ErrNotFound.
func (d *DB) GetJob(id string) (model.Job, error) {
	row := d.QueryRow(`SELECT `+jobCols+` FROM jobs WHERE id = ?`, id)
	j, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Job{}, ErrNotFound
	}
	return j, err
}

// ListJobs returns the most recent jobs, newest first, capped at limit.
func (d *DB) ListJobs(limit int) ([]model.Job, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := d.Query(`SELECT `+jobCols+` FROM jobs ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// UpdateJobProgress sets status/total/done/message for a running job.
func (d *DB) UpdateJobProgress(id string, status model.JobStatus, total, done int, message string) error {
	_, err := d.Exec(
		`UPDATE jobs SET status=?, total=?, done=?, message=? WHERE id=?`,
		status, total, done, message, id,
	)
	return err
}

// FinishJob marks a job terminal (done/failed/canceled) with an optional error
// and stamps finished_at.
func (d *DB) FinishJob(id string, status model.JobStatus, errMsg string) error {
	_, err := d.Exec(
		`UPDATE jobs SET status=?, error=?, finished_at=? WHERE id=?`,
		status, errMsg, now(), id,
	)
	return err
}
