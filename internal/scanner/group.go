// Package scanner walks a library root, groups audio files into books, reads
// their embedded tags, and persists the result. It has no knowledge of
// metadata providers or renaming.
package scanner

import (
	"context"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"audiobookrenamer/internal/db"
	"audiobookrenamer/internal/model"
)

// ScannedFile is one audio file found on disk, relative to its unit.
type ScannedFile struct {
	AbsPath string
	RelPath string // path relative to Unit.SourceDir, always using "/"
	Size    int64
	ModTime int64 // unix seconds
	Ext     string
	// Tags holds the file's embedded metadata, read once during the walk. The
	// book's title/author/year and each file's track number are derived from
	// these, so a digest of the relevant subset feeds fingerprint(): a tag
	// rewrite that preserves size and restores mtime would otherwise be
	// invisible to a rescan.
	Tags fileTags
	// SeqIndex is the file's 1-based position in its book's numbered set, as
	// found in the filenames by sequenceIndex. Zero when grouping found no
	// sequence (single-file books, merged disc folders). It is the track
	// fallback when a file carries no track tag.
	SeqIndex int
}

// Unit is one book: the folder it lives in, and every audio file in it.
//
// There is no per-file variant. A folder holds exactly one book, so a folder
// and a book are the same thing and the files inside are never split apart.
// (model.Book still carries a SourceFile, because books organized before this
// rule existed have one recorded and undo restores it.)
type Unit struct {
	SourceDir string // absolute
	Layout    model.Layout
	Files     []ScannedFile
}

var discDirRe = regexp.MustCompile(`(?i)^(?:cd|dis[ck]|part|vol(?:ume)?)[ ._-]*(\d+)[a-z]?$`)
var numRunRe = regexp.MustCompile(`\d+`)

// Group walks root and returns the book units beneath it. Every directory that
// directly contains audio is exactly one book, holding all of that directory's
// audio; disc-style subfolders (CD1, Disc 2, …) are merged into one unit at
// their parent.
func Group(ctx context.Context, root string) ([]Unit, error) {
	byDir := map[string][]ScannedFile{}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		// Abort promptly on shutdown/cancel rather than walking the whole tree.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			// Don't swallow this: a directory we can't read means we can't see
			// the books under it, and silently continuing would make the
			// scanner treat them as deleted and prune them. Fail the scan
			// instead.
			return fmt.Errorf("read %s: %w", path, err)
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && path != root {
				return fs.SkipDir
			}
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "._") {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(name))
		if !model.AudioExts[ext] {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			// Same reasoning as the walk error above: dropping this file would
			// leave its book unseen, and scanner.Scan prunes books it did not
			// see in the walk. Fail the scan rather than delete a book because
			// one stat call failed.
			return fmt.Errorf("stat %s: %w", path, statErr)
		}
		dir := filepath.Dir(path)
		byDir[dir] = append(byDir[dir], ScannedFile{
			AbsPath: path,
			Size:    info.Size(),
			ModTime: info.ModTime().Unix(),
			Ext:     ext,
			Tags:    readTags(path),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	unitsByDir := map[string]*Unit{}
	for dir, files := range byDir {
		sortFiles(files)
		u := unitForDir(dir, files)
		unitsByDir[keyForDir(dir)] = &u
	}

	mergeDiscFolders(byDir, unitsByDir)

	out := make([]Unit, 0, len(unitsByDir))
	for _, u := range unitsByDir {
		u.Files = relativize(u.SourceDir, u.Files)
		sortScanned(u.Files)
		out = append(out, *u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SourceDir < out[j].SourceDir })
	return out, nil
}

// keyForDir is the identity a scanned unit is filed under while grouping. It is
// db.BookIdentity: the two must agree, or scanner.Scan cannot line a scanned
// unit up with the book row it already has and would insert a duplicate. Every
// unit is folder-rooted, so the source-file half of the identity is always
// empty.
func keyForDir(dir string) string { return db.BookIdentity(dir, "") }

// unitForDir builds the one book that the audio directly inside dir makes up.
// The files are never split: the folder is the book. Layout follows from the
// file count alone, and sequenceIndex supplies track numbers where the
// filenames carry them.
func unitForDir(dir string, files []ScannedFile) Unit {
	layout := model.LayoutMulti
	if len(files) == 1 {
		layout = model.LayoutSingle
	}
	numbered := make([]ScannedFile, len(files))
	copy(numbered, files)
	if idx, ok := sequenceIndex(files); ok {
		for i := range numbered {
			numbered[i].SeqIndex = idx[i]
		}
	}
	return Unit{SourceDir: dir, Layout: layout, Files: numbered}
}

// sequenceIndex reports whether the filenames carry track numbers and, if so,
// returns each file's 1-based track in the same order as files.
//
// It no longer decides anything about grouping - a folder is one book either
// way - so its only question is which digit run in the names is the track.
// That run is FOUND, not guessed. Filenames carry digit runs that have nothing
// to do with track order: a year in "Tarnished Knight (2012) (07)", the codec
// in "- MP3 -", a bitrate in "64kbps". Any fixed position (the first run, the
// last run) is wrong for some real naming scheme, so every aligned position is
// tested and the first one shaped like a track numbering wins. A year or codec
// cannot win: it is identical in every name, and trackRunStart rejects repeats.
//
// When nothing qualifies the caller falls back to sorted file order.
func sequenceIndex(files []ScannedFile) ([]int, bool) {
	n := len(files)
	if n < 2 {
		return nil, false
	}
	runs := make([][]int, n)
	minRuns := 0
	for i, f := range files {
		runs[i] = numberRuns(f.AbsPath)
		if len(runs[i]) == 0 {
			return nil, false
		}
		if i == 0 || len(runs[i]) < minRuns {
			minRuns = len(runs[i])
		}
	}
	// Only positions every file has can be compared; a name carrying extra runs
	// ("02 - Chapter 1.mp3" beside "01 - Intro.mp3") is still indexed by the
	// positions its siblings share.
	for pos := 0; pos < minRuns; pos++ {
		vals := runsAt(runs, pos)
		start, ok := trackRunStart(vals)
		if !ok {
			continue
		}
		idx := make([]int, n)
		for i, v := range vals {
			idx[i] = v + 1 - start // a 0-based run on disk is still track 1..N
		}
		return idx, true
	}
	return nil, false
}

// runsAt collects digit run number pos from every file. Callers must keep pos
// below the shortest run list.
func runsAt(runs [][]int, pos int) []int {
	vals := make([]int, len(runs))
	for i, r := range runs {
		vals[i] = r[pos]
	}
	return vals
}

// nonIndexRun stands in for a digit run too long to be an int. Keeping a
// placeholder rather than dropping the run leaves that file's remaining runs
// aligned with its siblings', and trackRunStart refuses it outright.
const nonIndexRun = math.MaxInt

// numberRuns returns the digit runs in absPath's base name, as integers.
func numberRuns(absPath string) []int {
	base := strings.TrimSuffix(filepath.Base(absPath), filepath.Ext(absPath))
	matches := numRunRe.FindAllString(base, -1)
	out := make([]int, 0, len(matches))
	for _, m := range matches {
		v, err := strconv.Atoi(m)
		if err != nil {
			v = nonIndexRun
		}
		out = append(out, v)
	}
	return out
}

// trackRunStart reports whether vals is shaped like a track numbering - values
// distinct from one another, starting at 0 or 1 - and returns the value it
// starts at.
//
// Gaps are fine: a folder is one book even when a file is missing, and tracks
// 01, 02, 04 should keep the numbers they have on disk. Repeats are not: a run
// identical in every name is a year, a codec, or a bitrate, never an index.
func trackRunStart(vals []int) (int, bool) {
	if len(vals) == 0 {
		return 0, false
	}
	sorted := append([]int(nil), vals...)
	sort.Ints(sorted)
	if sorted[0] > 1 || sorted[len(sorted)-1] == nonIndexRun {
		return 0, false
	}
	for i := 1; i < len(sorted); i++ {
		if sorted[i] == sorted[i-1] {
			return 0, false
		}
	}
	return sorted[0], true
}

// mergeDiscFolders folds CD1/CD2/… sibling units into a single multi-file unit
// rooted at their shared parent.
func mergeDiscFolders(byDir map[string][]ScannedFile, unitsByDir map[string]*Unit) {
	childDirs := map[string][]string{}
	for dir := range byDir {
		childDirs[filepath.Dir(dir)] = append(childDirs[filepath.Dir(dir)], dir)
	}
	for parent, kids := range childDirs {
		if len(kids) < 2 || len(byDir[parent]) != 0 {
			continue
		}
		if _, taken := unitsByDir[keyForDir(parent)]; taken {
			continue
		}
		allDisc := true
		for _, k := range kids {
			if _, ok := discNumber(k); !ok {
				allDisc = false
				break
			}
			if _, ok := unitsByDir[keyForDir(k)]; !ok {
				allDisc = false
				break
			}
		}
		if !allDisc {
			continue
		}
		sortDiscs(kids)
		merged := Unit{SourceDir: parent, Layout: model.LayoutMulti}
		track := 0
		for _, k := range kids {
			for _, f := range unitsByDir[keyForDir(k)].Files {
				// Each disc numbered its own tracks 1..N, so the per-disc
				// indexes collide once the discs are one book. Renumber across
				// discs in DISC-NUMBER order; falling back to the merged path
				// ordering would not do, because sorting paths as strings puts
				// CD10 ahead of CD2.
				track++
				f.SeqIndex = track
				merged.Files = append(merged.Files, f)
			}
			delete(unitsByDir, keyForDir(k))
		}
		unitsByDir[keyForDir(parent)] = &merged
	}
}

// discNumber returns the ordinal in a disc folder name ("CD2", "Disc 10") and
// whether the name is a disc folder at all.
func discNumber(dir string) (int, bool) {
	m := discDirRe.FindStringSubmatch(filepath.Base(dir))
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	return n, true
}

// sortDiscs orders disc folders by their ordinal, so disc 2 precedes disc 10.
func sortDiscs(dirs []string) {
	sort.Slice(dirs, func(i, j int) bool {
		ni, _ := discNumber(dirs[i])
		nj, _ := discNumber(dirs[j])
		if ni != nj {
			return ni < nj
		}
		return dirs[i] < dirs[j]
	})
}

func relativize(base string, files []ScannedFile) []ScannedFile {
	out := make([]ScannedFile, len(files))
	for i, f := range files {
		rel, err := filepath.Rel(base, f.AbsPath)
		if err != nil {
			rel = filepath.Base(f.AbsPath)
		}
		f.RelPath = filepath.ToSlash(rel)
		out[i] = f
	}
	return out
}

func sortFiles(f []ScannedFile) {
	sort.Slice(f, func(i, j int) bool { return f[i].AbsPath < f[j].AbsPath })
}
func sortScanned(f []ScannedFile) {
	sort.Slice(f, func(i, j int) bool { return f[i].RelPath < f[j].RelPath })
}

// DirExists is a small helper used by callers to validate a library root.
func DirExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}
