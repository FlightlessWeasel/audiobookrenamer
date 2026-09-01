// Package config resolves runtime configuration from defaults, an optional
// JSON config file, and environment variable overrides (prefix ABR_).
//
// Only process-level settings live here (bind address, data directory, log
// level). User-facing settings that change at runtime — provider keys, auth
// toggle, match threshold — are stored in the database via internal/db and the
// settings API, not here.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config holds process-level configuration.
type Config struct {
	// Addr is the TCP address the HTTP server binds to, e.g. ":8674".
	Addr string `json:"addr"`
	// ConfigDir is where the SQLite database and any local state are kept.
	ConfigDir string `json:"config_dir"`
	// LogLevel is one of debug, info, warn, error.
	LogLevel string `json:"log_level"`
}

// DBPath returns the absolute path to the SQLite database file.
func (c Config) DBPath() string {
	return filepath.Join(c.ConfigDir, "audiobookrenamer.db")
}

func defaults() Config {
	return Config{
		Addr:      ":8674",
		ConfigDir: defaultConfigDir(),
		LogLevel:  "info",
	}
}

func defaultConfigDir() string {
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, "audiobookrenamer")
	}
	return "config"
}

// Load builds a Config from defaults, then the JSON file at path (if it exists
// and path is non-empty), then ABR_-prefixed environment variables. It also
// ensures ConfigDir exists.
func Load(path string) (Config, error) {
	cfg := defaults()

	if path != "" {
		b, err := os.ReadFile(path)
		switch {
		case err == nil:
			if err := json.Unmarshal(b, &cfg); err != nil {
				return Config{}, fmt.Errorf("parse config file %s: %w", path, err)
			}
		case !os.IsNotExist(err):
			return Config{}, fmt.Errorf("read config file %s: %w", path, err)
		}
	}

	if err := applyEnv(&cfg); err != nil {
		return Config{}, err
	}

	if cfg.ConfigDir == "" {
		cfg.ConfigDir = defaultConfigDir()
	}
	// 0700: this directory holds the SQLite database, session-signing secret,
	// and provider API keys — nothing here should be world-readable. Chmod as
	// well as MkdirAll, since MkdirAll leaves an already-existing directory's
	// mode untouched (and umask can loosen the create mode).
	if err := os.MkdirAll(cfg.ConfigDir, 0o700); err != nil {
		return Config{}, fmt.Errorf("create config dir %s: %w", cfg.ConfigDir, err)
	}
	if err := os.Chmod(cfg.ConfigDir, 0o700); err != nil {
		return Config{}, fmt.Errorf("secure config dir %s: %w", cfg.ConfigDir, err)
	}
	return cfg, nil
}

func applyEnv(cfg *Config) error {
	if v := os.Getenv("ABR_ADDR"); v != "" {
		cfg.Addr = v
	}
	if v := os.Getenv("ABR_PORT"); v != "" {
		port := strings.TrimPrefix(v, ":")
		if _, err := strconv.Atoi(port); err != nil {
			return fmt.Errorf("invalid ABR_PORT %q: must be a number", v)
		}
		cfg.Addr = ":" + port
	}
	if v := os.Getenv("ABR_CONFIG_DIR"); v != "" {
		cfg.ConfigDir = v
	}
	if v := os.Getenv("ABR_LOG_LEVEL"); v != "" {
		cfg.LogLevel = v
	}
	return nil
}
