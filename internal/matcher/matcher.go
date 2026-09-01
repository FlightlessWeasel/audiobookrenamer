// Package matcher orchestrates metadata matching: it queries providers, scores
// candidates against a scanned book, stores them, and either auto-accepts a
// clear winner or leaves the book for manual review.
package matcher

import (
	"context"
	"fmt"
	"strings"

	"audiobookrenamer/internal/db"
	"audiobookrenamer/internal/matching"
	"audiobookrenamer/internal/metadata"
	"audiobookrenamer/internal/model"
	"audiobookrenamer/internal/strutil"
	"audiobookrenamer/internal/worker"
)

// Matcher couples the provider registry with the database.
type Matcher struct {
	db  *db.DB
	reg *metadata.Registry
}

// New builds a Matcher.
func New(database *db.DB, reg *metadata.Registry) *Matcher {
	return &Matcher{db: database, reg: reg}
}

// Register wires the bulk "match a library" job.
func Register(wm *worker.Manager, m *Matcher) {
	wm.Register(model.JobMatch, m.runLibraryJob)
}

const maxStoredCandidates = 12

// MatchBook queries providers for one book, stores ranked candidates, and
// auto-accepts the top one if it clears the configured threshold. It returns
// the (possibly updated) book and the ranked candidates.
func (m *Matcher) MatchBook(ctx context.Context, bookID string) (model.Book, []model.Candidate, error) {
	book, err := m.db.GetBookBare(bookID)
	if err != nil {
		return model.Book{}, nil, err
	}

	q := metadata.Query{Title: book.Title, Author: book.Author, Year: book.Year, Narrator: book.Narrator}
	if q.Title == "" {
		q = metadata.Query{Freeform: baseName(book)}
	}

	found, err := m.reg.Search(ctx, q)
	if err != nil {
		return book, nil, err
	}

	ranked := matching.Rank(book, found)
	if len(ranked) > maxStoredCandidates {
		ranked = ranked[:maxStoredCandidates]
	}
	if err := m.db.ReplaceCandidates(bookID, ranked); err != nil {
		return book, ranked, err
	}

	threshold, err := metadata.LoadAutoMatchThreshold(m.db)
	if err != nil {
		return book, ranked, err
	}
	if pick, ok := matching.AutoPick(ranked, threshold); ok {
		updated, err := m.apply(book, pick)
		return updated, ranked, err
	}

	newState := model.StateUnmatched
	if len(ranked) > 0 {
		newState = model.StateNeedsReview
	}
	if book.State != model.StateMatched && book.State != model.StateOrganized {
		if err := m.db.SetBookState(bookID, newState, ""); err != nil {
			return book, ranked, err
		}
		book.State = newState
	}
	return book, ranked, nil
}

// AcceptOutcome reports what AcceptTopCandidates did. Considered is always
// Accepted + NoCandidates + BelowScore: every book in scope lands in exactly
// one bucket, so a caller can explain a 0-Accepted run instead of it looking
// like nothing happened.
type AcceptOutcome struct {
	Considered   int `json:"considered"`
	Accepted     int `json:"accepted"`
	NoCandidates int `json:"no_candidates"` // in scope, but never searched/matched
	BelowScore   int `json:"below_score"`   // has a top candidate, but it didn't clear minScore
}

// AcceptTopCandidates accepts the best already-stored candidate for every
// unmatched or needs-review book whose top candidate scores at least minScore.
// An empty libraryID sweeps every library.
//
// It never calls a provider: it only acts on candidates a previous match run
// stored, which is what makes it cheap enough to run synchronously and what
// makes it the way to clear a review backlog at a lower bar than the standing
// auto-match threshold.
//
// Unlike MatchBook it applies a plain score bar, without AutoPick's
// runner-up-margin guard. That guard is deliberately skipped: books held back
// by an ambiguous runner-up are exactly the ones sitting in review, and the
// caller is asking to take the top pick anyway.
func (m *Matcher) AcceptTopCandidates(libraryID string, minScore float64) (AcceptOutcome, error) {
	var out AcceptOutcome
	books, err := m.db.ListBooks(db.BookFilter{LibraryID: libraryID, Limit: 5000})
	if err != nil {
		return out, err
	}
	for _, b := range books {
		if b.State != model.StateUnmatched && b.State != model.StateNeedsReview {
			continue
		}
		out.Considered++
		cands, err := m.db.ListCandidates(b.ID)
		if err != nil {
			return out, err
		}
		switch {
		case len(cands) == 0:
			out.NoCandidates++
			continue
		case cands[0].Score < minScore:
			out.BelowScore++
			continue
		}
		if _, err := m.apply(b, cands[0]); err != nil {
			return out, err
		}
		out.Accepted++
	}
	return out, nil
}

// Search runs a provider query not tied to a specific book (manual UI search).
// An empty provider fans out to every enabled provider; a non-empty provider
// restricts the search to that one (metadata.ErrProviderNotAvailable if it is
// unknown or disabled).
func (m *Matcher) Search(ctx context.Context, q metadata.Query, provider string) ([]model.Candidate, error) {
	if provider == "" {
		return m.reg.Search(ctx, q)
	}
	return m.reg.SearchProvider(ctx, provider, q)
}

// AcceptStored applies a previously stored candidate (by provider + id) to the
// book and marks it matched.
func (m *Matcher) AcceptStored(bookID, provider, providerID string) (model.Book, error) {
	book, err := m.db.GetBookBare(bookID)
	if err != nil {
		return model.Book{}, err
	}
	c, err := m.db.GetCandidate(bookID, provider, providerID)
	if err != nil {
		return model.Book{}, err
	}
	return m.apply(book, c)
}

// AcceptCandidate applies an arbitrary candidate (e.g. from a manual search or
// hand-entered fields) to the book.
func (m *Matcher) AcceptCandidate(bookID string, c model.Candidate) (model.Book, error) {
	book, err := m.db.GetBookBare(bookID)
	if err != nil {
		return model.Book{}, err
	}
	return m.apply(book, c)
}

// apply copies candidate fields onto the book, sets provenance, marks it
// matched, and persists.
func (m *Matcher) apply(book model.Book, c model.Candidate) (model.Book, error) {
	book.Title = strutil.FirstNonEmpty(c.Title, book.Title)
	book.Subtitle = c.Subtitle
	if a := matching.PrimaryAuthor(c); a != "" {
		book.Author = a
		// Refresh the derived sort name, but never overwrite a value a user
		// edited by hand (author_sort_source == "manual").
		if book.AuthorSortSource != model.AuthorSortManual {
			book.AuthorSort = strutil.DeriveAuthorSort(a)
			book.AuthorSortSource = model.AuthorSortDerived
		}
	}
	if n := matching.JoinNarrators(c); n != "" {
		book.Narrator = n
	}
	book.Series = c.Series
	book.SeriesIndex = c.SeriesIndex
	if c.Year > 0 {
		book.Year = c.Year
	}
	book.ASIN = c.ASIN
	book.ISBN = c.ISBN
	book.CoverURL = c.CoverURL
	book.MatchedProvider = c.Provider
	book.MatchedProviderID = c.ProviderID
	book.MatchScore = c.Score
	book.State = model.StateMatched
	book.Message = ""

	if err := m.db.SetBookMatch(book.ID, book); err != nil {
		return model.Book{}, err
	}
	return book, nil
}

func (m *Matcher) runLibraryJob(ctx context.Context, job model.Job, p *worker.Progress) error {
	books, err := m.db.ListBooks(db.BookFilter{LibraryID: job.LibraryID, Limit: 5000})
	if err != nil {
		return err
	}
	var todo []model.Book
	for _, b := range books {
		if b.State == model.StateUnmatched || b.State == model.StateNeedsReview {
			todo = append(todo, b)
		}
	}

	p.Set(0, len(todo), "matching")
	var matched, review int
	for i, b := range todo {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		updated, _, err := m.MatchBook(ctx, b.ID)
		if err != nil {
			// Don't fail the whole job for one book; record and continue.
			_ = m.db.SetBookState(b.ID, model.StateError, truncate(err.Error(), 300))
		} else if updated.State == model.StateMatched {
			matched++
		} else {
			review++
		}
		p.Set(i+1, len(todo), fmt.Sprintf("%d matched, %d to review", matched, review))
	}
	return nil
}

func baseName(b model.Book) string {
	p := b.SourceDir
	if b.SourceFile != "" {
		p = b.SourceFile
	}
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		p = p[i+1:]
	}
	if dot := strings.LastIndex(p, "."); dot > 0 {
		p = p[:dot]
	}
	return p
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
