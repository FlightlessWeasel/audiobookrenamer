// Package model holds the core domain types shared across the scanner,
// metadata, matching, organize, and api packages.
package model

import "time"

// StructureMode selects the top-level library folder hierarchy.
type StructureMode string

const (
	// AuthorFirst: {author}/{series}/{book}/<files> (default).
	AuthorFirst StructureMode = "author_first"
	// SeriesFirst: {series}/{author}/{book}/<files> — for multi-author
	// anthologies and shared-world series.
	SeriesFirst StructureMode = "series_first"
)

// Layout describes how a book's audio is stored on disk.
type Layout string

const (
	// LayoutSingle: one audio file is the whole book (e.g. a .m4b).
	LayoutSingle Layout = "single"
	// LayoutMulti: a folder of numbered tracks makes up one book.
	LayoutMulti Layout = "multi"
)

// BookState is the workflow state of a scanned book.
type BookState string

const (
	StateUnmatched   BookState = "unmatched"    // scanned, no metadata match yet
	StateNeedsReview BookState = "needs_review" // candidates found, below auto threshold
	StateMatched     BookState = "matched"      // metadata accepted, not yet organized
	StateOrganized   BookState = "organized"    // on-disk layout matches the template
	StateError       BookState = "error"        // last operation failed; see message
)

// Default naming templates. Path separators are always "/" in templates and are
// translated per-OS by the organize package.
const (
	DefaultFileTemplate      = "{title}[ ({year})] - {author}{ext}"
	DefaultMultiFileTemplate = "{title} ({year}) - {track2}{ext}"
)

// AuthorSort provenance. A "derived" value is recomputed from the author name
// whenever a match sets the author; a "manual" value is hand-edited and never
// overwritten by a match.
const (
	AuthorSortDerived = "derived"
	AuthorSortManual  = "manual"
)

// AuthorFolderMode selects which form of the author's name names the author
// folder.
type AuthorFolderMode string

const (
	// AuthorFolderSort files a book under the sort name, "Campbell, Jack".
	AuthorFolderSort AuthorFolderMode = "sort"
	// AuthorFolderName files a book under the display name, "Jack Campbell".
	AuthorFolderName AuthorFolderMode = "name"
)

// Library is a configured root folder that the app manages.
type Library struct {
	ID                string           `json:"id"`
	Name              string           `json:"name"`
	RootPath          string           `json:"root_path"`
	StructureMode     StructureMode    `json:"structure_mode"`
	AuthorFolderMode  AuthorFolderMode `json:"author_folder_mode"`
	FileTemplate      string           `json:"file_template"`
	MultiFileTemplate string           `json:"multi_file_template"`
	Enabled           bool             `json:"enabled"`

	// WriteTags rewrites the embedded metadata tags of a book's audio files as
	// part of organizing it. EmbedCover additionally embeds the cover image;
	// it has no effect unless WriteTags is set. Both default off — every other
	// organize step leaves file contents alone.
	WriteTags  bool `json:"write_tags"`
	EmbedCover bool `json:"embed_cover"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Book is one audiobook discovered in a library, plus any accepted metadata.
type Book struct {
	ID         string    `json:"id"`
	LibraryID  string    `json:"library_id"`
	SourceDir  string    `json:"source_dir"`            // absolute path to the book's folder (or the file's parent for a loose single file)
	SourceFile string    `json:"source_file,omitempty"` // absolute path, set only for a standalone file that shares its folder with siblings
	Layout     Layout    `json:"layout"`
	State      BookState `json:"state"`
	Message    string    `json:"message,omitempty"`

	// ScanFingerprint is a hash of the book's files (path/size/mtime); an
	// unchanged fingerprint lets a rescan skip the book.
	ScanFingerprint string `json:"-"`

	MatchedProvider   string  `json:"matched_provider,omitempty"`
	MatchedProviderID string  `json:"matched_provider_id,omitempty"`
	MatchScore        float64 `json:"match_score,omitempty"`

	// Metadata — populated from tags on scan, overwritten on match.
	Title            string `json:"title,omitempty"`
	Subtitle         string `json:"subtitle,omitempty"`
	Author           string `json:"author,omitempty"`
	AuthorSort       string `json:"author_sort,omitempty"`
	AuthorSortSource string `json:"author_sort_source,omitempty"` // "derived" | "manual"
	Narrator         string `json:"narrator,omitempty"`
	Series           string `json:"series,omitempty"`
	SeriesIndex      string `json:"series_index,omitempty"`
	Year             int    `json:"year,omitempty"`
	ASIN             string `json:"asin,omitempty"`
	ISBN             string `json:"isbn,omitempty"`
	CoverURL         string `json:"cover_url,omitempty"`

	Files     []BookFile `json:"files,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// BookFile is one audio file belonging to a Book.
type BookFile struct {
	ID      string `json:"id"`
	BookID  string `json:"book_id"`
	RelPath string `json:"rel_path"` // path relative to Book.SourceDir
	Size    int64  `json:"size"`
	ModTime int64  `json:"mod_time"` // unix seconds
	Ext     string `json:"ext"`      // lowercase, with leading dot
	Track   int    `json:"track,omitempty"`
	TagJSON string `json:"tag_json,omitempty"` // raw parsed tags, for debugging/UI
}

// Candidate is a metadata match returned by a Provider.
type Candidate struct {
	Provider    string   `json:"provider"`
	ProviderID  string   `json:"provider_id"`
	Title       string   `json:"title"`
	Subtitle    string   `json:"subtitle,omitempty"`
	Authors     []string `json:"authors,omitempty"`
	Narrators   []string `json:"narrators,omitempty"`
	Series      string   `json:"series,omitempty"`
	SeriesIndex string   `json:"series_index,omitempty"`
	Year        int      `json:"year,omitempty"`
	ASIN        string   `json:"asin,omitempty"`
	ISBN        string   `json:"isbn,omitempty"`
	CoverURL    string   `json:"cover_url,omitempty"`
	Score       float64  `json:"score,omitempty"` // set by the matching package
}

// JobType identifies a background job.
type JobType string

const (
	JobScan       JobType = "scan"
	JobMatch      JobType = "match"
	JobOrganize   JobType = "organize"
	JobUndo       JobType = "undo"
	JobSelfUpdate JobType = "selfupdate"
)

// JobStatus is the lifecycle state of a background job.
type JobStatus string

const (
	JobQueued   JobStatus = "queued"
	JobRunning  JobStatus = "running"
	JobDone     JobStatus = "done"
	JobFailed   JobStatus = "failed"
	JobCanceled JobStatus = "canceled"
)

// Job is a unit of background work reported to the UI.
type Job struct {
	ID         string     `json:"id"`
	Type       JobType    `json:"type"`
	Status     JobStatus  `json:"status"`
	LibraryID  string     `json:"library_id,omitempty"`
	Total      int        `json:"total"`
	Done       int        `json:"done"`
	Message    string     `json:"message,omitempty"`
	Error      string     `json:"error,omitempty"`
	Payload    string     `json:"-"` // job-type-specific JSON (e.g. organize book ids)
	CreatedAt  time.Time  `json:"created_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// AudioExts is the set of file extensions the scanner treats as audiobook
// audio. Keys are lowercase and include the leading dot.
var AudioExts = map[string]bool{
	".m4b":  true,
	".m4a":  true,
	".mp3":  true,
	".flac": true,
	".ogg":  true,
	".opus": true,
	".aac":  true,
	".wav":  true,
}
