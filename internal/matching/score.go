package matching

import (
	"sort"
	"strings"

	"audiobookrenamer/internal/model"
)

// Weights for the components of a match score. Title dominates; year is a light
// tie-breaker and only counts when both sides have one.
const (
	wTitle  = 0.60
	wAuthor = 0.30
	wYear   = 0.10
)

// Score rates how well candidate c matches book b, in [0,1].
func Score(b model.Book, c model.Candidate) float64 {
	bt := NormalizeTitle(b.Title)
	ct := NormalizeTitle(c.Title)
	titleScore := diceCoefficient(bt, ct)
	// A full subtitle-inclusive comparison can rescue truncated titles.
	if alt := diceCoefficient(Normalize(b.Title), Normalize(c.Title+" "+c.Subtitle)); alt > titleScore {
		titleScore = alt
	}

	weight := wTitle
	sum := wTitle * titleScore

	if b.Author != "" && len(c.Authors) > 0 {
		sum += wAuthor * bestAuthorMatch(b.Author, c.Authors)
		weight += wAuthor
	}

	if b.Year > 0 && c.Year > 0 {
		sum += wYear * yearScore(b.Year, c.Year)
		weight += wYear
	}

	if weight == 0 {
		return 0
	}
	score := sum / weight

	// Small bonus when the series matches what little we know.
	if b.Series != "" && c.Series != "" && diceCoefficient(Normalize(b.Series), Normalize(c.Series)) > 0.6 {
		score += 0.03
	}
	if b.SeriesIndex != "" && c.SeriesIndex != "" && SeriesIndexEqual(b.SeriesIndex, c.SeriesIndex) {
		score += 0.02
	}
	if score > 1 {
		score = 1
	}
	return score
}

func bestAuthorMatch(bookAuthor string, candAuthors []string) float64 {
	na := Normalize(bookAuthor)
	best := 0.0
	for _, ca := range candAuthors {
		if s := diceCoefficient(na, Normalize(ca)); s > best {
			best = s
		}
	}
	return best
}

func yearScore(a, b int) float64 {
	d := a - b
	if d < 0 {
		d = -d
	}
	switch {
	case d == 0:
		return 1
	case d <= 1:
		return 0.7
	case d <= 3:
		return 0.3
	default:
		return 0
	}
}

// Rank scores every candidate against the book, sorts best-first, and returns
// the slice (candidates are copied with Score set).
func Rank(b model.Book, candidates []model.Candidate) []model.Candidate {
	out := make([]model.Candidate, len(candidates))
	copy(out, candidates)
	for i := range out {
		out[i].Score = Score(b, out[i])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

// AutoPick returns the top candidate when it clears the threshold and either
// leads the runner-up by a clear margin or the runner-up is the same work
// (two providers agreeing is consensus, not ambiguity).
func AutoPick(ranked []model.Candidate, threshold float64) (model.Candidate, bool) {
	if len(ranked) == 0 || ranked[0].Score < threshold {
		return model.Candidate{}, false
	}
	if len(ranked) > 1 && ranked[0].Score-ranked[1].Score < 0.05 && !sameWork(ranked[0], ranked[1]) {
		return model.Candidate{}, false
	}
	return ranked[0], true
}

// sameWork reports whether two candidates plainly describe the same book:
// a shared ISBN or ASIN, or matching normalized title with overlapping authors.
func sameWork(a, b model.Candidate) bool {
	if a.ISBN != "" && a.ISBN == b.ISBN {
		return true
	}
	if a.ASIN != "" && a.ASIN == b.ASIN {
		return true
	}
	if NormalizeTitle(a.Title) != NormalizeTitle(b.Title) || NormalizeTitle(a.Title) == "" {
		return false
	}
	if len(a.Authors) == 0 || len(b.Authors) == 0 {
		return true // same title, no author info to contradict it
	}
	return bestAuthorMatch(a.Authors[0], b.Authors) > 0.5
}

// PrimaryAuthor picks a single author string from a candidate (first listed).
func PrimaryAuthor(c model.Candidate) string {
	if len(c.Authors) == 0 {
		return ""
	}
	return strings.TrimSpace(c.Authors[0])
}

// JoinNarrators renders a candidate's narrators as a comma-separated string.
func JoinNarrators(c model.Candidate) string {
	return strings.Join(c.Narrators, ", ")
}
