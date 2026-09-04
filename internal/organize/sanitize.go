// Package organize renders library naming templates, plans in-place renames
// (optionally including an embedded-tag rewrite) as a reviewable diff, and
// executes them with a journal that supports undo.
package organize

import (
	"regexp"
	"strings"
)

// Windows-reserved device names (case-insensitive), which are illegal as a
// path segment even with an extension.
var winReserved = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true,
	"com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true,
	"lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

var illegalChars = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]`)
var multiSpace = regexp.MustCompile(`\s+`)

// SanitizeSegment makes s safe to use as a single path component on any of the
// supported OSes: illegal characters become spaces, control chars are dropped,
// trailing dots/spaces are trimmed (Windows), reserved device names are
// prefixed, and the result is length-clamped. An all-empty result becomes
// "Unknown".
func SanitizeSegment(s string) string {
	s = strings.TrimSpace(s)
	s = illegalChars.ReplaceAllString(s, " ")
	s = multiSpace.ReplaceAllString(s, " ")
	s = strings.Trim(s, " .")

	if s == "" {
		return "Unknown"
	}
	if base, _, found := strings.Cut(s, "."); found {
		if winReserved[strings.ToLower(base)] {
			s = "_" + s
		}
	} else if winReserved[strings.ToLower(s)] {
		s = "_" + s
	}

	return clampRunes(s, 180)
}

// SanitizeRelPath sanitizes every segment of a "/"-separated relative path and
// rejoins them with "/". Empty segments are dropped.
func SanitizeRelPath(p string) string {
	parts := strings.Split(p, "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || part == "." {
			continue
		}
		out = append(out, SanitizeSegment(part))
	}
	return strings.Join(out, "/")
}

func clampRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return strings.TrimRight(string(r[:max]), " .")
}

// EqualFold reports whether two paths differ only by ASCII case (a case-only
// rename, which needs special handling on case-insensitive filesystems).
func EqualFold(a, b string) bool {
	return a != b && strings.EqualFold(a, b)
}
