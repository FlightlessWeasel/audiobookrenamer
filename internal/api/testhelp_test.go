package api

import (
	"path/filepath"
	"testing"

	"audiobookrenamer/internal/config"
	"audiobookrenamer/internal/db"
)

// testVersion is the build version newTestServer / serverWithWorker pass to
// api.New. It is valid semver so the self-update comparisons behave normally.
const testVersion = "v1.0.0"

// newTestServer returns a Server backed by a fresh temp SQLite database. Worker
// and Matcher are nil; tests that need them should set them explicitly.
func newTestServer(t *testing.T) *Server {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	s, err := New(config.Config{}, d, nil, nil, testVersion)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}
