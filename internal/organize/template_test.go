package organize

import (
	"testing"

	"audiobookrenamer/internal/model"
)

// The default single-file template must not leave a bare "()" when the year is
// unknown: the year sits in an optional [ ... ] group that vanishes when empty.
func TestDefaultFileTemplate_DropsEmptyYear(t *testing.T) {
	noYear := Render(model.DefaultFileTemplate, TokenValues{
		Title: "Elantris", Author: "Brandon Sanderson", Ext: ".m4b",
	})
	if noYear != "Elantris - Brandon Sanderson.m4b" {
		t.Fatalf("no-year render = %q, want no empty parens", noYear)
	}

	withYear := Render(model.DefaultFileTemplate, TokenValues{
		Title: "Elantris", Author: "Brandon Sanderson", Year: "2005", Ext: ".m4b",
	})
	if withYear != "Elantris (2005) - Brandon Sanderson.m4b" {
		t.Fatalf("with-year render = %q", withYear)
	}
}

func TestRender_Basic(t *testing.T) {
	tv := TokenValues{Title: "Mistborn", Year: "2006", Author: "Brandon Sanderson", Ext: ".m4b"}
	got := Render("{title} ({year}) - {author}{ext}", tv)
	want := "Mistborn (2006) - Brandon Sanderson.m4b"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRender_OptionalGroupDropped(t *testing.T) {
	// No year -> the "[ (...)]" group disappears, spaces collapse.
	tv := TokenValues{Title: "Elantris", Ext: ".mp3"}
	got := Render("{title}[ ({year})]{ext}", tv)
	if got != "Elantris.mp3" {
		t.Fatalf("got %q", got)
	}

	tv.Year = "2005"
	got = Render("{title}[ ({year})]{ext}", tv)
	if got != "Elantris (2005).mp3" {
		t.Fatalf("got %q", got)
	}
}

func TestRender_TrackPadding(t *testing.T) {
	tv := TokenValues{Title: "The Stand", Track: 7, Ext: ".mp3"}
	if got := Render("{title} - {track2}{ext}", tv); got != "The Stand - 07.mp3" {
		t.Fatalf("got %q", got)
	}
	if got := Render("{title} - {track3}{ext}", tv); got != "The Stand - 007.mp3" {
		t.Fatalf("got %q", got)
	}
}

func TestRender_UnknownTokenKeptLiteral(t *testing.T) {
	if got := Render("{title} {bogus}", TokenValues{Title: "X"}); got != "X {bogus}" {
		t.Fatalf("got %q", got)
	}
}

func TestSanitizeSegment(t *testing.T) {
	cases := map[string]string{
		`A: B / C`:         "A B C",
		`trailing dots...`: "trailing dots",
		`  spaced  `:       "spaced",
		`con`:              "_con",
		`NUL.txt`:          "_NUL.txt",
		``:                 "Unknown",
		`normal name`:      "normal name",
	}
	for in, want := range cases {
		if got := SanitizeSegment(in); got != want {
			t.Errorf("SanitizeSegment(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidateTemplate(t *testing.T) {
	ok := []string{
		"{title}[ ({year})] - {author}{ext}",
		"{title} ({year}) - {track2}{ext}",
		"{author_sort}/{series}/{title}{ext}",
		"plain text no tokens",
		"",
	}
	for _, tmpl := range ok {
		if err := ValidateTemplate(tmpl); err != nil {
			t.Errorf("ValidateTemplate(%q) = %v, want nil", tmpl, err)
		}
	}

	bad := []string{
		"{tilte}{ext}",       // typo'd token
		"{title}{bogus}",     // unknown token
		"{title} ({year}]",   // unbalanced group
		"{title} [oops{ext}", // unbalanced group
		"{title",             // unterminated placeholder
	}
	for _, tmpl := range bad {
		if err := ValidateTemplate(tmpl); err == nil {
			t.Errorf("ValidateTemplate(%q) = nil, want error", tmpl)
		}
	}
}

// knownTokens (used by ValidateTemplate) must not drift from what lookup can
// actually resolve, or a valid template would be rejected — or an invalid one
// accepted.
func TestKnownTokensMatchLookup(t *testing.T) {
	var tv TokenValues
	for tok := range knownTokens {
		if _, ok := tv.lookup(tok); !ok {
			t.Errorf("knownTokens has %q but TokenValues.lookup does not resolve it", tok)
		}
	}
}

func TestEqualFold(t *testing.T) {
	if !EqualFold("/a/Book", "/a/book") {
		t.Error("case-only difference should be EqualFold")
	}
	if EqualFold("/a/book", "/a/book") {
		t.Error("identical paths are not a case-fix")
	}
	if EqualFold("/a/one", "/a/two") {
		t.Error("different paths are not EqualFold")
	}
}
