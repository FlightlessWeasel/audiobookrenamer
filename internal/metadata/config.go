package metadata

import (
	"fmt"

	"audiobookrenamer/internal/db"
)

// Settings keys and stored config structs for the metadata providers. The
// settings API (internal/api/settings.go) imports these rather than redefining
// them.
const (
	KeyAudible            = "audible"
	KeyGoogleBooks        = "google_books"
	KeyOpenLibrary        = "open_library"
	KeyAutoMatchThreshold = "auto_match_threshold"
)

// DefaultAutoMatchThreshold is used when the setting is unset.
const DefaultAutoMatchThreshold = 0.85

// AudibleConfig is the stored config for the Audible provider.
type AudibleConfig struct {
	Enabled bool   `json:"enabled"`
	Region  string `json:"region"`
}

// GoogleConfig is the stored config for Google Books. APIKey is optional.
type GoogleConfig struct {
	Enabled bool   `json:"enabled"`
	APIKey  string `json:"api_key"`
}

// ToggleConfig is a bare enable/disable provider config.
type ToggleConfig struct {
	Enabled bool `json:"enabled"`
}

// ProviderConfig is the resolved config for all providers.
type ProviderConfig struct {
	Audible     AudibleConfig
	GoogleBooks GoogleConfig
	OpenLibrary ToggleConfig
}

// LoadProviderConfig reads provider settings, applying defaults (all providers
// enabled, Audible region "us") for anything unset. A read failure (I/O, corrupt
// JSON) is returned rather than swallowed: callers must not silently query a
// disabled provider or drop an API key because a row could not be read.
func LoadProviderConfig(d *db.DB) (ProviderConfig, error) {
	c := ProviderConfig{
		Audible:     AudibleConfig{Enabled: true, Region: "us"},
		GoogleBooks: GoogleConfig{Enabled: true},
		OpenLibrary: ToggleConfig{Enabled: true},
	}
	if _, err := d.GetSetting(KeyAudible, &c.Audible); err != nil {
		return ProviderConfig{}, fmt.Errorf("load %s config: %w", KeyAudible, err)
	}
	if c.Audible.Region == "" {
		c.Audible.Region = "us"
	}
	if _, err := d.GetSetting(KeyGoogleBooks, &c.GoogleBooks); err != nil {
		return ProviderConfig{}, fmt.Errorf("load %s config: %w", KeyGoogleBooks, err)
	}
	if _, err := d.GetSetting(KeyOpenLibrary, &c.OpenLibrary); err != nil {
		return ProviderConfig{}, fmt.Errorf("load %s config: %w", KeyOpenLibrary, err)
	}
	return c, nil
}

// LoadAutoMatchThreshold reads the auto-match threshold. A value stored outside
// (0,1] is a value judgement, not a failure, and falls back to the default; a
// genuine read error (I/O, corrupt JSON) is returned so the caller can abort.
func LoadAutoMatchThreshold(d *db.DB) (float64, error) {
	t := DefaultAutoMatchThreshold
	if _, err := d.GetSetting(KeyAutoMatchThreshold, &t); err != nil {
		return 0, fmt.Errorf("load %s: %w", KeyAutoMatchThreshold, err)
	}
	if t <= 0 || t > 1 {
		return DefaultAutoMatchThreshold, nil
	}
	return t, nil
}
