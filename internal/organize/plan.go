package organize

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"audiobookrenamer/internal/db"
	"audiobookrenamer/internal/model"
	"audiobookrenamer/internal/strutil"
)

// ErrBookNotInLibrary is returned when an organize request references a book
// that belongs to a different library than the one named in the request.
var ErrBookNotInLibrary = errors.New("book does not belong to the requested library")

// ValidateBooks checks that every id belongs to libraryID before any planning
// or filesystem work happens. It returns db.ErrNotFound for unknown ids and
// ErrBookNotInLibrary for cross-library ids.
func ValidateBooks(database *db.DB, libraryID string, bookIDs []string) error {
	for _, id := range bookIDs {
		b, err := database.GetBookBare(id)
		if err != nil {
			return err
		}
		if b.LibraryID != libraryID {
			return fmt.Errorf("book %s: %w", id, ErrBookNotInLibrary)
		}
	}
	return nil
}

// OpKind identifies a filesystem step in a plan.
type OpKind string

const (
	OpMkdir   OpKind = "mkdir"   // create Dst
	OpMove    OpKind = "move"    // rename Src -> Dst
	OpCaseFix OpKind = "casefix" // rename Src -> Dst differing only by case (two-step)
	OpRmdir   OpKind = "rmdir"   // remove Dst (an emptied source directory)
)

// Op is one journaled filesystem step. Paths are absolute, native separators.
type Op struct {
	Kind OpKind `json:"kind"`
	Src  string `json:"src,omitempty"`
	Dst  string `json:"dst"`
}

// FileMove is the UI-facing before/after for a single file.
type FileMove struct {
	FromRel string `json:"from_rel"` // relative to library root, "/" separators
	ToRel   string `json:"to_rel"`
	NoOp    bool   `json:"no_op"`
}

// BookPlan is the planned change for one book. Fields tagged json:"-" are
// internal bookkeeping used by the executor.
type BookPlan struct {
	BookID string     `json:"book_id"`
	Title  string     `json:"title"`
	Moves  []FileMove `json:"moves"`
	Skip   bool       `json:"skip"`
	Reason string     `json:"reason,omitempty"`

	OldSourceDir  string            `json:"-"`
	OldSourceFile string            `json:"-"`
	OldState      model.BookState   `json:"-"` // book state at plan time, restored verbatim on undo
	OldFileRel    map[string]string `json:"-"` // fileID -> old rel_path
	NewSourceDir  string            `json:"-"`
	NewFileRel    map[string]string `json:"-"` // fileID -> new rel_path
}

// Plan is the full set of changes for an organize run.
type Plan struct {
	LibraryID string     `json:"library_id"`
	RootPath  string     `json:"root_path"`
	Books     []BookPlan `json:"books"`
	Conflicts []string   `json:"conflicts,omitempty"`
}

// Changed reports whether the plan has any non-skipped file move.
func (p *Plan) Changed() bool {
	for _, b := range p.Books {
		if b.Skip {
			continue
		}
		for _, m := range b.Moves {
			if !m.NoOp {
				return true
			}
		}
	}
	return false
}

// HasWork reports whether Execute has anything at all to do: at least one
// non-skipped book. Unlike Changed, this is true even when every move in the
// plan is a NoOp — a book already sitting at its target path still needs
// Execute to run so its state can be finalized to organized. Changed alone is
// the wrong gate for "should Execute run": a batch of only no-op books would
// never clear it, and Execute would never run to flip their state.
func (p *Plan) HasWork() bool {
	for _, b := range p.Books {
		if !b.Skip {
			return true
		}
	}
	return false
}

// BuildPlan computes the rename plan for the given books (or every matched book
// in the library when bookIDs is empty). It reads the filesystem but changes
// nothing.
func BuildPlan(database *db.DB, libraryID string, bookIDs []string) (*Plan, error) {
	lib, err := database.GetLibrary(libraryID)
	if err != nil {
		return nil, err
	}

	var books []model.Book
	if len(bookIDs) == 0 {
		list, err := database.ListBooks(db.BookFilter{LibraryID: libraryID, State: model.StateMatched, Limit: 5000})
		if err != nil {
			return nil, err
		}
		books = list
	} else {
		for _, id := range bookIDs {
			b, err := database.GetBook(id)
			if err != nil {
				return nil, err
			}
			if b.LibraryID != libraryID {
				return nil, fmt.Errorf("book %s: %w", id, ErrBookNotInLibrary)
			}
			books = append(books, b)
		}
	}

	// Books is non-nil from the start: an empty preview (no matched books, or
	// none selected) must serialise "books": [] for the UI's validator.
	plan := &Plan{LibraryID: libraryID, RootPath: lib.RootPath, Books: []BookPlan{}}
	// target-abs (case-folded) -> bookID that owns it, to detect cross-book
	// collisions.
	claimed := map[string]string{}
	// folded target folder -> bookID. Every organized book's DB identity is
	// (library_id, source_dir, source_file="") — see FinalizeOrganize — so two
	// books that land in the same folder collide on that UNIQUE index even when
	// their file names differ and `claimed` never trips (e.g. a file template
	// keyed on {narrator} for two editions of one title). Catch it here and skip
	// the later book with a visible reason, exactly as for a shared file target.
	claimedDirs := map[string]string{}

	for _, b := range books {
		if len(b.Files) == 0 {
			var err error
			b, err = database.GetBook(b.ID)
			if err != nil {
				return nil, err
			}
		}
		bp := planBook(lib, b)

		// Compute this book's target claims first, then commit them only if the
		// book survives. A book that ends up skipped must leave `claimed`
		// untouched, or a later book could collide with a claim nobody holds.
		var keys []string
		for i := range bp.Moves {
			if bp.Skip {
				break
			}
			if bp.Moves[i].NoOp {
				continue
			}
			key := strings.ToLower(filepath.ToSlash(filepath.Join(lib.RootPath, filepath.FromSlash(bp.Moves[i].ToRel))))
			if prev, taken := claimed[key]; taken && prev != b.ID {
				bp.Skip = true
				bp.Reason = "target path collides with another book in this run"
				plan.Conflicts = append(plan.Conflicts, bp.Moves[i].ToRel)
				break
			}
			keys = append(keys, key)
		}
		// A surviving book also claims its destination folder. Unlike the file
		// keys this is checked even when every move is a NoOp: a book already
		// sitting at its target still gets finalized to (source_dir, "") and
		// would collide with a second book targeting the same folder.
		dirKey := ""
		if !bp.Skip && bp.NewSourceDir != "" {
			dirKey = strings.ToLower(filepath.ToSlash(bp.NewSourceDir))
			if prev, taken := claimedDirs[dirKey]; taken && prev != b.ID {
				bp.Skip = true
				bp.Reason = "target folder collides with another book in this run"
				plan.Conflicts = append(plan.Conflicts, mustRel(lib.RootPath, bp.NewSourceDir))
			}
		}
		if dirKey != "" && !bp.Skip {
			// Another book already parked at this exact identity
			// (source_dir, source_file="") means finalizing this book to the same
			// identity would trip the UNIQUE index. This covers a duplicate that
			// isn't in the run at all (it plainly isn't moving) and one that is
			// (claimedDirs only sees books already planned this pass, so it can't
			// rely on ordering). If that incumbent is itself relocating this run
			// the skip is a harmless false positive that clears on the next run
			// once the folder is empty. Skip now with a visible reason rather
			// than aborting mid-run.
			occ, err := database.BookIDAt(libraryID, bp.NewSourceDir, "")
			if err != nil {
				return nil, err
			}
			if occ != "" && occ != b.ID {
				bp.Skip = true
				bp.Reason = "target folder already holds another book in this library"
				plan.Conflicts = append(plan.Conflicts, mustRel(lib.RootPath, bp.NewSourceDir))
			}
		}
		if !bp.Skip {
			for _, k := range keys {
				claimed[k] = b.ID
			}
			if dirKey != "" {
				claimedDirs[dirKey] = b.ID
			}
		}
		plan.Books = append(plan.Books, bp)
	}
	return plan, nil
}

func planBook(lib model.Library, b model.Book) BookPlan {
	bp := BookPlan{
		BookID: b.ID,
		Title:  b.Title,
		// Non-nil so a skipped book (which never reaches the move loop) still
		// serialises "moves": [] rather than "moves": null — the UI's response
		// validator requires an array.
		Moves:         []FileMove{},
		OldSourceDir:  b.SourceDir,
		OldSourceFile: b.SourceFile,
		OldState:      b.State,
		OldFileRel:    map[string]string{},
		NewFileRel:    map[string]string{},
	}

	if b.State != model.StateMatched && b.State != model.StateOrganized {
		bp.Skip, bp.Reason = true, "book is not matched"
		return bp
	}
	if strings.TrimSpace(b.Title) == "" {
		bp.Skip, bp.Reason = true, "missing title"
		return bp
	}
	// "Has an author" means either name is set: the folder segment falls back
	// from the sort name to the display name, so a book with only a sort name is
	// perfectly placeable and must not be skipped as authorless.
	hasAuthor := strings.TrimSpace(authorFolderName(lib, b)) != ""
	if lib.StructureMode == model.AuthorFirst && !hasAuthor {
		bp.Skip, bp.Reason = true, "missing author (required for author-first layout)"
		return bp
	}
	if lib.StructureMode == model.SeriesFirst && b.Series == "" && !hasAuthor {
		bp.Skip, bp.Reason = true, "missing author and series"
		return bp
	}

	tmpl := lib.FileTemplate
	if b.Layout == model.LayoutMulti {
		tmpl = lib.MultiFileTemplate
	}
	tracks := effectiveTracks(b)

	// Render every file name first: the folder budget below depends on the
	// longest of them.
	names := make([]string, len(b.Files))
	longestName := 0
	for idx, f := range b.Files {
		ext := f.Ext
		if ext == "" {
			ext = strings.ToLower(filepath.Ext(f.RelPath))
		}
		name := Render(tmpl, tokenValues(b, ext, tracks[idx]))
		name = SanitizeSegment(strings.TrimSuffix(name, "/"))
		if !strings.HasSuffix(strings.ToLower(name), strings.ToLower(ext)) {
			name += ext
		}
		names[idx] = name
		if n := utf8.RuneCountInString(name); n > longestName {
			longestName = n
		}
	}

	// Keep the deepest target inside the platform's path limit. Clamping each
	// segment to 180 runes (SanitizeSegment) bounds the segments but not their
	// sum: root + author + series + book folder + file name runs past MAX_PATH
	// on Windows well before any single segment is oversized, and the failure
	// would otherwise land mid-run as a failed mkdir or rename.
	// The two separators are the ones around the book folder.
	dirBudget := MaxPathLen - utf8.RuneCountInString(lib.RootPath) - 2 - longestName
	segs, fits := fitDirSegments(bookDirSegments(lib, b), dirBudget)
	if !fits {
		bp.Skip, bp.Reason = true, "target path would exceed this platform's maximum path length"
		return bp
	}
	bookDirRel := strings.Join(segs, "/")
	bp.NewSourceDir = filepath.Join(lib.RootPath, filepath.FromSlash(bookDirRel))

	seenTarget := map[string]bool{}
	for idx, f := range b.Files {
		toRel := bookDirRel + "/" + names[idx]
		if seenTarget[strings.ToLower(toRel)] {
			bp.Skip, bp.Reason = true, "two files would be renamed to the same name (missing track numbers?)"
			return bp
		}
		seenTarget[strings.ToLower(toRel)] = true

		fromAbs := fileAbs(b, f)
		fromRel := mustRel(lib.RootPath, fromAbs)
		toAbs := filepath.Join(lib.RootPath, filepath.FromSlash(toRel))
		noOp := filepath.Clean(fromAbs) == filepath.Clean(toAbs)

		if !resolvedWithinRoot(lib.RootPath, fromAbs) || !resolvedWithinRoot(lib.RootPath, toAbs) {
			bp.Skip, bp.Reason = true, "resolved path escapes the library root"
			return bp
		}
		if fi, err := os.Lstat(fromAbs); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			bp.Skip, bp.Reason = true, "source file is a symlink"
			return bp
		}
		// Refuse to plan a move onto a path that already holds something else,
		// or the executor would abort partway through. The one allowed
		// collision is THIS file's own current location differing only by case
		// (a case-fix). A sibling file's source path is NOT exempt: moving
		// onto it would clobber a file this same run still needs to move.
		if !noOp {
			if _, err := os.Lstat(toAbs); err == nil &&
				!EqualFold(filepath.Clean(toAbs), filepath.Clean(fromAbs)) {
				bp.Skip, bp.Reason = true, "a file already exists at the target path"
				return bp
			}
		}

		bp.OldFileRel[f.ID] = fromRel
		bp.NewFileRel[f.ID] = toRel

		bp.Moves = append(bp.Moves, FileMove{
			FromRel: fromRel,
			ToRel:   toRel,
			NoOp:    noOp,
		})
	}

	// A book whose files are all already at their target path (every move is
	// NoOp) is deliberately NOT skipped here. Skip means "cannot be
	// processed" (missing title, path collision, ...); this book CAN be
	// processed, it just has no file to move — most commonly a book that was
	// organized before, then rematched to metadata that renders the same
	// path. Execute still needs to run for it so FinalizeOrganize can flip its
	// state back to organized; a hard skip would exclude it from Execute's
	// active set and leave it stuck at "matched" forever even though its files
	// are already exactly where they belong.
	return bp
}

// bookDirSegments is the library-root-relative folder chain a book should live
// in, deepest segment last. Only the Series segment is optional (it is dropped
// for a standalone book); the author segment is always present, named by
// authorFolderName.
// authorFolderName is the author segment for a book's path: the sort name
// ("Campbell, Jack") or the display name ("Jack Campbell"), per the library.
// Either way the other name is the fallback, so a book that carries only one of
// the two is still placeable rather than being skipped as authorless. Both the
// skip check and the segment builder go through here, so they cannot disagree
// about whether a book has an author.
func authorFolderName(lib model.Library, b model.Book) string {
	if lib.AuthorFolderMode == model.AuthorFolderName {
		return strutil.FirstNonEmpty(b.Author, b.AuthorSort)
	}
	return strutil.FirstNonEmpty(b.AuthorSort, b.Author)
}

func bookDirSegments(lib model.Library, b model.Book) []string {
	var segs []string
	// Emptiness is tested on the resolved name, not on b.Author: a book with a
	// sort name but no display name still has a known author and must still get
	// an author folder.
	authorName := authorFolderName(lib, b)
	author := ""
	if strings.TrimSpace(authorName) != "" {
		author = SanitizeSegment(authorName)
	}
	series := ""
	if b.Series != "" {
		series = SanitizeSegment(b.Series)
	}
	switch lib.StructureMode {
	case model.SeriesFirst:
		if series != "" {
			segs = append(segs, series)
		}
		if author != "" {
			segs = append(segs, author)
		}
	default: // author first
		segs = append(segs, author)
		if series != "" {
			segs = append(segs, series)
		}
	}
	bookDir := SanitizeSegment(Render(bookDirTemplate, tokenValues(b, "", 0)))
	return append(segs, bookDir)
}

// minSegment is the shortest a folder segment is clamped to when a target is
// being shortened to fit the platform path limit. Below this a folder name
// stops being recognisable, and skipping the book with a reason the user can
// see in the preview beats filing it under an unreadable stub.
const minSegment = 24

// fitDirSegments shortens segs, deepest first, until the folder chain fits in
// budget runes (separators included), and reports whether it succeeded.
//
// Only folders are shortened. File names are left alone deliberately: both
// default templates end in the track number, so trimming a name from the right
// would strip exactly the part that keeps two tracks distinct.
func fitDirSegments(segs []string, budget int) ([]string, bool) {
	for i := len(segs) - 1; i >= 0 && segChainLen(segs) > budget; i-- {
		over := segChainLen(segs) - budget
		r := []rune(segs[i])
		keep := len(r) - over
		if keep < minSegment {
			keep = minSegment
		}
		if keep >= len(r) {
			continue
		}
		trimmed := strings.TrimRight(string(r[:keep]), " .")
		if trimmed == "" {
			trimmed = "Unknown"
		}
		segs[i] = trimmed
	}
	return segs, segChainLen(segs) <= budget
}

// segChainLen is the rune length of segs joined with "/".
func segChainLen(segs []string) int {
	n := len(segs) - 1 // separators
	for _, s := range segs {
		n += utf8.RuneCountInString(s)
	}
	return n
}

// effectiveTracks assigns a track number per file: the stored track when every
// file has a distinct positive one, otherwise the 1-based ordinal.
func effectiveTracks(b model.Book) []int {
	out := make([]int, len(b.Files))
	if b.Layout != model.LayoutMulti {
		return out
	}
	distinct := map[int]bool{}
	ok := true
	for _, f := range b.Files {
		if f.Track <= 0 || distinct[f.Track] {
			ok = false
			break
		}
		distinct[f.Track] = true
	}
	for i, f := range b.Files {
		if ok {
			out[i] = f.Track
		} else {
			out[i] = i + 1
		}
	}
	return out
}

func fileAbs(b model.Book, f model.BookFile) string {
	if b.SourceFile != "" && len(b.Files) == 1 {
		return b.SourceFile
	}
	return filepath.Join(b.SourceDir, filepath.FromSlash(f.RelPath))
}

func mustRel(base, target string) string {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return filepath.ToSlash(target)
	}
	return filepath.ToSlash(rel)
}

// ancestorDirs lists dir and its parents up to (excluding) root, shallowest
// first, so they can be created in order.
func ancestorDirs(root, dir string) []string {
	root = filepath.Clean(root)
	dir = filepath.Clean(dir)
	var chain []string
	for dir != root && len(dir) > len(root) {
		chain = append(chain, dir)
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	sort.Slice(chain, func(i, j int) bool { return len(chain[i]) < len(chain[j]) })
	return chain
}

// emptyingDirs lists the old book dir and its now-childless parents (up to but
// excluding root and excluding any ancestor of keep), deepest first.
func emptyingDirs(root, oldDir, keep string) []string {
	root = filepath.Clean(root)
	keep = filepath.Clean(keep)
	d := filepath.Clean(oldDir)
	var out []string
	for d != root && len(d) > len(root) {
		if keep == d || strings.HasPrefix(keep+string(filepath.Separator), d+string(filepath.Separator)) {
			break
		}
		out = append(out, d)
		d = filepath.Dir(d)
	}
	return out
}
