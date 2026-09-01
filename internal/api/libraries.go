package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"audiobookrenamer/internal/db"
	"audiobookrenamer/internal/model"
	"audiobookrenamer/internal/organize"

	"github.com/go-chi/chi/v5"
)

// libraryInput is the create/update payload. The template and structure fields
// are pointers so an update can tell "field omitted" (nil => keep current) apart
// from "field explicitly cleared" ("" => reset to the built-in default).
type libraryInput struct {
	Name              string                  `json:"name"`
	RootPath          string                  `json:"root_path"`
	StructureMode     *model.StructureMode    `json:"structure_mode"`
	AuthorFolderMode  *model.AuthorFolderMode `json:"author_folder_mode"`
	FileTemplate      *string                 `json:"file_template"`
	MultiFileTemplate *string                 `json:"multi_file_template"`
	Enabled           *bool                   `json:"enabled"`
}

func (in libraryInput) validate() (string, bool) {
	if strings.TrimSpace(in.Name) == "" {
		return "name is required", false
	}
	if strings.TrimSpace(in.RootPath) == "" {
		return "root_path is required", false
	}
	if !filepath.IsAbs(in.RootPath) {
		return "root_path must be an absolute path", false
	}
	fi, err := os.Stat(in.RootPath)
	if err != nil || !fi.IsDir() {
		return "root_path does not exist or is not a directory", false
	}
	if in.StructureMode != nil {
		switch *in.StructureMode {
		case "", model.AuthorFirst, model.SeriesFirst:
		default:
			return "structure_mode must be author_first or series_first", false
		}
	}
	if in.AuthorFolderMode != nil {
		switch *in.AuthorFolderMode {
		case "", model.AuthorFolderSort, model.AuthorFolderName:
		default:
			return "author_folder_mode must be sort or name", false
		}
	}
	// A non-empty template is validated; "" is a reset request and is left to
	// the db defaulting layer.
	for _, t := range []*string{in.FileTemplate, in.MultiFileTemplate} {
		if t != nil && *t != "" {
			if err := organize.ValidateTemplate(*t); err != nil {
				return err.Error(), false
			}
		}
	}
	return "", true
}

func (in libraryInput) structureMode() model.StructureMode {
	if in.StructureMode == nil {
		return ""
	}
	return *in.StructureMode
}

func (in libraryInput) authorFolderMode() model.AuthorFolderMode {
	if in.AuthorFolderMode == nil {
		return ""
	}
	return *in.AuthorFolderMode
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (s *Server) listLibraries(w http.ResponseWriter, r *http.Request) {
	libs, err := s.DB.ListLibraries()
	if err != nil {
		writeDBErr(w, err)
		return
	}
	if libs == nil {
		libs = []model.Library{}
	}
	writeJSON(w, http.StatusOK, libs)
}

func (s *Server) createLibrary(w http.ResponseWriter, r *http.Request) {
	var in libraryInput
	if !s.decode(w, r, &in) {
		return
	}
	if msg, ok := in.validate(); !ok {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	lib, err := s.DB.CreateLibrary(model.Library{
		Name:              strings.TrimSpace(in.Name),
		RootPath:          filepath.Clean(in.RootPath),
		StructureMode:     in.structureMode(),
		AuthorFolderMode:  in.authorFolderMode(),
		FileTemplate:      deref(in.FileTemplate),
		MultiFileTemplate: deref(in.MultiFileTemplate),
		Enabled:           enabled,
	})
	if err != nil {
		if db.IsUniqueViolation(err) {
			writeErr(w, http.StatusConflict, "a library with that root path already exists")
			return
		}
		writeDBErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, lib)
}

func (s *Server) getLibrary(w http.ResponseWriter, r *http.Request) {
	lib, err := s.DB.GetLibrary(chi.URLParam(r, "id"))
	if err != nil {
		writeDBErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, lib)
}

func (s *Server) updateLibrary(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	existing, err := s.DB.GetLibrary(id)
	if err != nil {
		writeDBErr(w, err)
		return
	}
	var in libraryInput
	if !s.decode(w, r, &in) {
		return
	}
	if msg, ok := in.validate(); !ok {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	existing.Name = strings.TrimSpace(in.Name)
	existing.RootPath = filepath.Clean(in.RootPath)
	// nil => leave the stored value; non-nil => apply it, where "" flows through
	// normalizeLibrary in the db layer and resets the field to its default.
	if in.StructureMode != nil {
		existing.StructureMode = *in.StructureMode
	}
	if in.AuthorFolderMode != nil {
		existing.AuthorFolderMode = *in.AuthorFolderMode
	}
	if in.FileTemplate != nil {
		existing.FileTemplate = *in.FileTemplate
	}
	if in.MultiFileTemplate != nil {
		existing.MultiFileTemplate = *in.MultiFileTemplate
	}
	if in.Enabled != nil {
		existing.Enabled = *in.Enabled
	}
	updated, err := s.DB.UpdateLibrary(existing)
	if err != nil {
		writeDBErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) deleteLibrary(w http.ResponseWriter, r *http.Request) {
	if err := s.DB.DeleteLibrary(chi.URLParam(r, "id")); err != nil {
		writeDBErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// scanLibrary enqueues a scan job for the library and returns the queued job;
// the worker runs it and reports progress over the job event stream.
func (s *Server) scanLibrary(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.DB.GetLibrary(id); err != nil {
		writeDBErr(w, err)
		return
	}
	job, err := s.Worker.Enqueue(model.JobScan, id)
	if err != nil {
		writeDBErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}
