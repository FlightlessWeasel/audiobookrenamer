package organize

import (
	"fmt"
	"strconv"
	"strings"

	"audiobookrenamer/internal/model"
	"audiobookrenamer/internal/strutil"
)

// TokenValues are the substitutions available to a naming template.
type TokenValues struct {
	Author      string
	AuthorSort  string
	Series      string
	SeriesIndex string
	Title       string
	Subtitle    string
	Year        string // "" when unknown
	Narrator    string
	ASIN        string
	ISBN        string
	Ext         string // includes the leading dot, lowercase
	Track       int    // effective track number for multi-file books
}

// knownTokens is the authoritative set of template tokens Render understands.
// It must stay in sync with TokenValues.lookup (guarded by a test).
var knownTokens = map[string]bool{
	"author": true, "author_sort": true, "series": true, "series_index": true,
	"title": true, "subtitle": true, "year": true, "narrator": true,
	"asin": true, "isbn": true, "ext": true,
	"track": true, "track2": true, "track3": true,
}

// ValidateTemplate reports a problem with a user-entered naming template before
// it is stored: an unknown {token}, or an unbalanced '[' / ']' optional group,
// or an unterminated '{'. It intentionally does NOT check path safety — every
// rendered segment is run through SanitizeSegment at plan time — its job is to
// stop a typo like "{tilte}" from being baked verbatim into every filename.
func ValidateTemplate(tmpl string) error {
	depth := 0
	for i := 0; i < len(tmpl); i++ {
		switch tmpl[i] {
		case '[':
			depth++
		case ']':
			depth--
			if depth < 0 {
				return fmt.Errorf("template: unbalanced ']'")
			}
		}
	}
	if depth != 0 {
		return fmt.Errorf("template: unbalanced '['")
	}
	for i := 0; i < len(tmpl); {
		if tmpl[i] != '{' {
			i++
			continue
		}
		rel := strings.IndexByte(tmpl[i:], '}')
		if rel < 0 {
			return fmt.Errorf("template: unterminated '{'")
		}
		name := tmpl[i+1 : i+rel]
		if !knownTokens[name] {
			return fmt.Errorf("template: unknown token %q", name)
		}
		i += rel + 1
	}
	return nil
}

func (tv TokenValues) lookup(name string) (string, bool) {
	switch name {
	case "author":
		return tv.Author, true
	case "author_sort":
		return strutil.FirstNonEmpty(tv.AuthorSort, tv.Author), true
	case "series":
		return tv.Series, true
	case "series_index":
		return tv.SeriesIndex, true
	case "title":
		return tv.Title, true
	case "subtitle":
		return tv.Subtitle, true
	case "year":
		return tv.Year, true
	case "narrator":
		return tv.Narrator, true
	case "asin":
		return tv.ASIN, true
	case "isbn":
		return tv.ISBN, true
	case "ext":
		return tv.Ext, true
	case "track":
		if tv.Track <= 0 {
			return "", true
		}
		return strconv.Itoa(tv.Track), true
	case "track2":
		if tv.Track <= 0 {
			return "", true
		}
		return fmt.Sprintf("%02d", tv.Track), true
	case "track3":
		if tv.Track <= 0 {
			return "", true
		}
		return fmt.Sprintf("%03d", tv.Track), true
	default:
		return "", false
	}
}

// Render expands a template string. It supports {token} substitutions and
// [ ... ] optional groups: a group whose text contains a {token} that resolves
// to empty is removed entirely. Unknown tokens are left verbatim so typos are
// visible in the preview.
func Render(tmpl string, tv TokenValues) string {
	var b strings.Builder
	i := 0
	for i < len(tmpl) {
		switch tmpl[i] {
		case '[':
			end := findClose(tmpl, i)
			if end < 0 {
				b.WriteByte(tmpl[i])
				i++
				continue
			}
			inner := tmpl[i+1 : end]
			if rendered, ok := renderGroup(inner, tv); ok {
				b.WriteString(rendered)
			}
			i = end + 1
		case '{':
			end := strings.IndexByte(tmpl[i:], '}')
			if end < 0 {
				b.WriteString(tmpl[i:])
				i = len(tmpl)
				continue
			}
			end += i
			name := tmpl[i+1 : end]
			if v, ok := tv.lookup(name); ok {
				b.WriteString(v)
			} else {
				b.WriteString(tmpl[i : end+1]) // keep unknown token literally
			}
			i = end + 1
		default:
			b.WriteByte(tmpl[i])
			i++
		}
	}
	return collapseSpaces(b.String())
}

// renderGroup returns (text, true) when every known token inside resolves
// non-empty, otherwise ("", false).
func renderGroup(inner string, tv TokenValues) (string, bool) {
	var b strings.Builder
	i := 0
	for i < len(inner) {
		if inner[i] == '{' {
			end := strings.IndexByte(inner[i:], '}')
			if end < 0 {
				b.WriteString(inner[i:])
				break
			}
			end += i
			name := inner[i+1 : end]
			v, known := tv.lookup(name)
			if known {
				if v == "" {
					return "", false
				}
				b.WriteString(v)
			} else {
				b.WriteString(inner[i : end+1])
			}
			i = end + 1
			continue
		}
		b.WriteByte(inner[i])
		i++
	}
	return b.String(), true
}

func findClose(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func collapseSpaces(s string) string {
	s = multiSpace.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// bookDirTemplate is the (currently fixed) template for a book's own folder.
const bookDirTemplate = "{title}[ ({year})]"

// tokenValues builds the substitution set for a book file.
func tokenValues(b model.Book, ext string, track int) TokenValues {
	year := ""
	if b.Year > 0 {
		year = strconv.Itoa(b.Year)
	}
	return TokenValues{
		Author:      b.Author,
		AuthorSort:  b.AuthorSort,
		Series:      b.Series,
		SeriesIndex: b.SeriesIndex,
		Title:       b.Title,
		Subtitle:    b.Subtitle,
		Year:        year,
		Narrator:    b.Narrator,
		ASIN:        b.ASIN,
		ISBN:        b.ISBN,
		Ext:         strings.ToLower(ext),
		Track:       track,
	}
}
