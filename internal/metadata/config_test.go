package metadata

import (
	"context"
	"testing"
)

// A transient/corrupt read of one provider's settings row must not silently
// degrade the resolved config to defaults: LoadProviderConfig returns the
// error, and Registry.Search propagates it instead of quietly running the
// default provider set (or a disabled provider).
func TestLoadProviderConfig_PropagatesCorruptRow(t *testing.T) {
	d := testDB(t)

	if err := d.SetSetting(KeyAudible, ToggleConfig{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	// Invalid JSON in Google's row: GetSetting returns a non-"not found" error.
	if _, err := d.Exec(
		`INSERT INTO settings (key, value) VALUES (?, ?)`,
		KeyGoogleBooks, `{"enabled":`,
	); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadProviderConfig(d); err == nil {
		t.Fatal("LoadProviderConfig should return an error for a corrupt provider row")
	}

	reg := NewRegistry(d)
	if _, err := reg.Search(context.Background(), Query{Freeform: "dune"}); err == nil {
		t.Fatal("Registry.Search should surface the config-load error, not run a default/disabled provider set")
	}
}

// A stored threshold outside (0,1] is a value judgement and falls back to the
// default without erroring; a genuine read failure is returned.
func TestLoadAutoMatchThreshold_ErrorVsOutOfRange(t *testing.T) {
	d := testDB(t)

	if err := d.SetSetting(KeyAutoMatchThreshold, 5.0); err != nil {
		t.Fatal(err)
	}
	got, err := LoadAutoMatchThreshold(d)
	if err != nil {
		t.Fatalf("out-of-range value must not be an error: %v", err)
	}
	if got != DefaultAutoMatchThreshold {
		t.Fatalf("out-of-range threshold = %v, want default %v", got, DefaultAutoMatchThreshold)
	}

	if _, err := d.Exec(
		`UPDATE settings SET value = ? WHERE key = ?`,
		`"not a number"`, KeyAutoMatchThreshold,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAutoMatchThreshold(d); err == nil {
		t.Fatal("a corrupt threshold row must return an error, not the default")
	}
}
