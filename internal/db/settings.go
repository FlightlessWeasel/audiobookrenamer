package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
)

// GetSetting unmarshals the JSON value stored under key into dst. It returns
// (false, nil) when the key is absent.
func (d *DB) GetSetting(key string, dst any) (bool, error) {
	var raw string
	err := d.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal([]byte(raw), dst); err != nil {
		return false, err
	}
	return true, nil
}

// Mutator is the single read-modify-write mechanism for settings. When used as a
// SetSettings (or SetSetting) value it is applied inside a transaction: it is
// handed the raw JSON currently stored under the key (raw is nil when the key is
// absent, and loaded reports the same) and returns the value to marshal and
// write back. Returning a nil value leaves the key untouched; returning an error
// aborts the transaction and is propagated to the caller unwrapped. Via
// SetSetting a Mutator runs in its own transaction; via SetSettings it shares
// the batch transaction with the other keys. Either way this lets a caller do a
// safe read-modify-write without a lost-update race and without dropping a
// read/unmarshal error.
type Mutator func(raw json.RawMessage, loaded bool) (any, error)

// SetSetting marshals val to JSON and upserts it under key. val may be a
// Mutator, in which case it is applied as a read-modify-write in its own
// transaction.
func (d *DB) SetSetting(key string, val any) error {
	if _, ok := val.(Mutator); ok {
		return d.InTx(func(tx *sql.Tx) error { return setSetting(tx, key, val) })
	}
	return setSetting(d.DB, key, val)
}

// SetSettings upserts several settings in a single transaction, so a failure
// partway through can't leave a group of related keys (e.g. the auth config)
// half-written.
func (d *DB) SetSettings(values map[string]any) error {
	if len(values) == 0 {
		return nil
	}
	// Deterministic order keeps behaviour predictable under test.
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return d.InTx(func(tx *sql.Tx) error {
		for _, k := range keys {
			if err := setSetting(tx, k, values[k]); err != nil {
				return err
			}
		}
		return nil
	})
}

func setSetting(x dbtx, key string, val any) error {
	if m, ok := val.(Mutator); ok {
		var cur string
		err := x.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&cur)
		loaded := err == nil
		switch {
		case errors.Is(err, sql.ErrNoRows):
			// key absent: pass nil / loaded == false
		case err != nil:
			return err
		}
		var curRaw json.RawMessage
		if loaded {
			curRaw = json.RawMessage(cur)
		}
		next, err := m(curRaw, loaded)
		if err != nil {
			return err
		}
		if next == nil {
			return nil
		}
		val = next
	}
	raw, err := json.Marshal(val)
	if err != nil {
		return err
	}
	_, err = x.Exec(
		`INSERT INTO settings (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, string(raw),
	)
	return err
}

// AllSettings returns every setting as a map of key to raw JSON message.
func (d *DB) AllSettings() (map[string]json.RawMessage, error) {
	rows, err := d.Query(`SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]json.RawMessage{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = json.RawMessage(v)
	}
	return out, rows.Err()
}
