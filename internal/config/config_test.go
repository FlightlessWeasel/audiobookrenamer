package config

import (
	"strings"
	"testing"
)

// An unparseable ABR_PORT is a misconfiguration and must surface as an error,
// not be silently dropped.
func TestLoad_InvalidPortErrors(t *testing.T) {
	t.Setenv("ABR_CONFIG_DIR", t.TempDir())
	t.Setenv("ABR_PORT", "not-a-number")

	_, err := Load("")
	if err == nil {
		t.Fatal("expected Load to fail on a non-numeric ABR_PORT")
	}
	if !strings.Contains(err.Error(), "ABR_PORT") {
		t.Fatalf("error should name ABR_PORT, got: %v", err)
	}
}

func TestLoad_ValidPortApplied(t *testing.T) {
	t.Setenv("ABR_CONFIG_DIR", t.TempDir())
	t.Setenv("ABR_PORT", "9191")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != ":9191" {
		t.Fatalf("Addr = %q, want :9191", cfg.Addr)
	}
}
