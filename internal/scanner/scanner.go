package scanner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"audiobookrenamer/internal/db"
	"audiobookrenamer/internal/model"
	"audiobookrenamer/internal/worker"
)

// Scanner persists library scans. Construct it with Register so its job handler
// is wired into the worker Manager.
type Scanner struct {
	db *db.DB
}

// Register creates a Scanner and binds it to the JobScan job type.
func Register(wm *worker.Manager, database *db.DB) *Scanner {
	s := &Scanner{db: database}
	wm.Register(model.JobScan, s.run)
	return s
}

func (s *Scanner) run(ctx context.Context, job model.Job, p *worker.Progress) error {
	lib, err := s.db.GetLibrary(job.LibraryID)
	if err != nil {
		return fmt.Errorf("load library: %w", err)
	}
	if !DirExists(lib.RootPath) {
		return fmt.Errorf("library root not found: %s", lib.RootPath)
	}

	units, err := Group(ctx, lib.RootPath)
	if err != nil {
		return fmt.Errorf("walk %s: %w", lib.RootPath, err)
	}

	index, err := s.db.BooksIndex(lib.ID)
	if err != nil {
		return err
	}
	seen := make(map[string]bool, len(units))

	p.Set(0, len(units), "scanning")
	for i, u := range units {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		id := db.BookIdentity(u.SourceDir, "")
		seen[id] = true
		fp := fingerprint(u.Files)

		existing, has := index[id]
		if has && existing.ScanFingerprint == fp {
			p.Set(i+1, len(units), "")
			continue
		}

		// The book row (which carries the new fingerprint) and its file rows
		// are written in one transaction, so a rescan can never see an
		// "unchanged" fingerprint next to a file list that failed to persist.
		book := s.buildBook(lib, u, fp, existing, has)
		if _, err := s.db.UpsertBookWithFiles(book, fileRows(u)); err != nil {
			return fmt.Errorf("save book %s: %w", u.SourceDir, err)
		}
		p.Set(i+1, len(units), filepath.Base(displayPath(u)))
	}

	var stale []string
	for id, b := range index {
		if !seen[id] {
			stale = append(stale, b.ID)
		}
	}
	sort.Strings(stale)
	if err := s.db.DeleteBooks(stale); err != nil {
		return fmt.Errorf("prune %d stale books: %w", len(stale), err)
	}

	p.Set(len(units), len(units), fmt.Sprintf("%d books, %d removed", len(units), len(stale)))
	return nil
}

// buildBook produces the Book row for a unit. A book that is already matched or
// organized keeps its metadata and state; otherwise fields are (re)derived from
// the first file's tags.
func (s *Scanner) buildBook(lib model.Library, u Unit, fp string, existing model.Book, has bool) model.Book {
	b := model.Book{
		LibraryID:        lib.ID,
		SourceDir:        u.SourceDir,
		Layout:           u.Layout,
		ScanFingerprint:  fp,
		State:            model.StateUnmatched,
		AuthorSortSource: model.AuthorSortDerived,
	}
	if has {
		b.ID = existing.ID
		if existing.State == model.StateMatched || existing.State == model.StateOrganized {
			// Preserve accepted metadata; only files/fingerprint changed.
			b.State = existing.State
			b.MatchedProvider = existing.MatchedProvider
			b.MatchedProviderID = existing.MatchedProviderID
			b.MatchScore = existing.MatchScore
			b.Title, b.Subtitle = existing.Title, existing.Subtitle
			b.Author, b.AuthorSort = existing.Author, existing.AuthorSort
			b.AuthorSortSource = existing.AuthorSortSource
			b.Narrator = existing.Narrator
			b.Series, b.SeriesIndex = existing.Series, existing.SeriesIndex
			b.Year = existing.Year
			b.ASIN, b.ISBN, b.CoverURL = existing.ASIN, existing.ISBN, existing.CoverURL
			return b
		}
	}

	first := u.Files[0].AbsPath
	tags := u.Files[0].Tags
	// For a single-file book the file's own name is usually the best title
	// hint (e.g. "Author/The Hobbit.m4b"); for a multi-file book the tracks
	// are numbered, so the folder name is the better hint.
	nameHint := filepath.Base(u.SourceDir)
	if u.Layout == model.LayoutSingle {
		base := filepath.Base(first)
		nameHint = strings.TrimSuffix(base, filepath.Ext(base))
	}
	title, author, year := bookMetaFromTags(tags, nameHint)
	b.Title, b.Author, b.Year = title, author, year
	return b
}

func fileRows(u Unit) []model.BookFile {
	rows := make([]model.BookFile, 0, len(u.Files))
	for i, f := range u.Files {
		var track int
		if u.Layout == model.LayoutMulti {
			track = trackNumber(f.Tags, f.SeqIndex, i+1)
		}
		rows = append(rows, model.BookFile{
			RelPath: f.RelPath,
			Size:    f.Size,
			ModTime: f.ModTime,
			Ext:     f.Ext,
			Track:   track,
			TagJSON: "",
		})
	}
	return rows
}

// fingerprint hashes the file set so an unchanged book is skipped on rescan. It
// covers both filesystem identity (path/size/mtime) and a digest of the tag
// subset that book metadata is derived from, so a tag-only rewrite (same size,
// mtime restored) still forces the book to be reprocessed.
func fingerprint(files []ScannedFile) string {
	type fp struct {
		P string `json:"p"`
		S int64  `json:"s"`
		M int64  `json:"m"`
		T string `json:"t"`
	}
	items := make([]fp, len(files))
	for i, f := range files {
		items[i] = fp{P: f.RelPath, S: f.Size, M: f.ModTime, T: f.Tags.digest()}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].P < items[j].P })
	b, _ := json.Marshal(items)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

func displayPath(u Unit) string { return u.SourceDir }
