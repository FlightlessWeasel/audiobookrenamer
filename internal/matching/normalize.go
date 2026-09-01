// Package matching scores metadata candidates against a scanned book and picks
// an automatic match when one candidate is a clear winner.
package matching

import (
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

var diacriticStripper = transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)

// Normalize lowercases, strips diacritics and punctuation, expands "&", drops a
// leading article, and collapses whitespace.
func Normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if out, _, err := transform.String(diacriticStripper, s); err == nil {
		s = out
	}
	s = strings.ReplaceAll(s, "&", " and ")

	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		case unicode.IsSpace(r) || r == '-' || r == '_' || r == '/' || r == ':':
			b.WriteRune(' ')
		}
	}
	fields := strings.Fields(b.String())
	if len(fields) > 1 {
		switch fields[0] {
		case "the", "a", "an":
			fields = fields[1:]
		}
	}
	return strings.Join(fields, " ")
}

// NormalizeTitle is Normalize applied to the part of a title before a subtitle
// separator, with common audiobook edition suffixes removed.
func NormalizeTitle(s string) string {
	if i := strings.IndexAny(s, ":("); i > 0 {
		s = s[:i]
	}
	s = Normalize(s)
	for _, suffix := range []string{" unabridged", " abridged", " a novel", " audiobook"} {
		s = strings.TrimSuffix(s, suffix)
	}
	return strings.TrimSpace(s)
}

// tokens returns the unique word set of a normalized string.
func tokens(s string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, f := range strings.Fields(s) {
		out[f] = struct{}{}
	}
	return out
}

// diceCoefficient is the token-set Sørensen–Dice similarity of two strings
// (already normalized), in [0,1].
func diceCoefficient(a, b string) float64 {
	ta, tb := tokens(a), tokens(b)
	if len(ta) == 0 || len(tb) == 0 {
		return 0
	}
	inter := 0
	for t := range ta {
		if _, ok := tb[t]; ok {
			inter++
		}
	}
	return 2 * float64(inter) / float64(len(ta)+len(tb))
}

var romanValues = map[byte]int{'i': 1, 'v': 5, 'x': 10, 'l': 50, 'c': 100, 'd': 500, 'm': 1000}

// romanToInt converts a lowercase roman numeral to its value, or 0 if it is not
// a clean roman numeral.
func romanToInt(s string) int {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return 0
	}
	total, prev := 0, 0
	for i := len(s) - 1; i >= 0; i-- {
		v, ok := romanValues[s[i]]
		if !ok {
			return 0
		}
		if v < prev {
			total -= v
		} else {
			total += v
			prev = v
		}
	}
	return total
}

// SeriesIndexEqual reports whether two series indexes refer to the same volume,
// treating "3", "03", "III", and "3.0" as equal.
func SeriesIndexEqual(a, b string) bool {
	na, nb := canonSeriesIndex(a), canonSeriesIndex(b)
	return na != "" && na == nb
}

func canonSeriesIndex(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	if r := romanToInt(s); r > 0 {
		return strconv.Itoa(r)
	}
	s = strings.TrimLeft(s, "0")
	s = strings.TrimSuffix(s, ".0")
	return s
}
