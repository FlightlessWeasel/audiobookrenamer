// Package strutil holds small string helpers shared across packages.
package strutil

import "strings"

// FirstNonEmpty returns the first argument that is non-empty after trimming
// surrounding whitespace, already trimmed. It returns "" when every argument is
// empty or blank.
func FirstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// DeriveAuthorSort converts a natural-order personal name ("First Middle Last")
// into library sort order ("Last, First Middle").
//
// This is a FROZEN heuristic. It is consumed by the organize templates to build
// the author folder segment, so changing it silently mass-renames author
// folders on the next organize run — keep it stable. It is deliberately simple
// and is knowingly WRONG for, among others:
//   - generational suffixes: "Isaac Asimov Jr." -> "Jr., Isaac Asimov"
//   - multi-word surname particles: "Ursula van der Leun" -> "Leun, Ursula van der"
//   - non-Western name order, where many names are already in sort order
//
// Those cases are expected to be corrected with a manual override rather than by
// extending this function.
//
// Rules: input is trimmed; a value that already contains a comma is assumed to
// be sort order and returned as-is; a single token is returned unchanged;
// otherwise the final whitespace-separated token becomes the sort key and the
// remainder follows the comma.
func DeriveAuthorSort(name string) string {
	s := strings.TrimSpace(name)
	if s == "" || strings.Contains(s, ",") {
		return s
	}
	fields := strings.Fields(s)
	if len(fields) < 2 {
		return s
	}
	last := fields[len(fields)-1]
	rest := strings.Join(fields[:len(fields)-1], " ")
	return last + ", " + rest
}
