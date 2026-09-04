package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"audiobookrenamer/internal/model"

	"github.com/go-chi/chi/v5"
)

func seedLibrary(t *testing.T, s *Server, fileTemplate string) model.Library {
	t.Helper()
	lib, err := s.DB.CreateLibrary(model.Library{
		Name: "L", RootPath: t.TempDir(), StructureMode: model.AuthorFirst,
		FileTemplate: fileTemplate,
	})
	if err != nil {
		t.Fatal(err)
	}
	return lib
}

func patchLibrary(t *testing.T, s *Server, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	req := httptest.NewRequest(http.MethodPatch, "/api/libraries/"+id, strings.NewReader(body)).
		WithContext(context.WithValue(context.Background(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()
	s.updateLibrary(rr, req)
	return rr
}

// PATCH file_template:"" is an explicit reset: the stored value goes back to the
// built-in default, not "" (which would render every filename to "Unknown").
func TestUpdateLibrary_EmptyTemplateResetsToDefault(t *testing.T) {
	s := newTestServer(t)
	lib := seedLibrary(t, s, "{title}{ext}")

	body := fmt.Sprintf(`{"name":"L","root_path":%q,"file_template":""}`, lib.RootPath)
	rr := patchLibrary(t, s, lib.ID, body)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	var got model.Library
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.FileTemplate != model.DefaultFileTemplate {
		t.Fatalf("file_template = %q, want default %q", got.FileTemplate, model.DefaultFileTemplate)
	}
}

// PATCH that omits file_template leaves the stored value untouched.
func TestUpdateLibrary_OmittedTemplateKept(t *testing.T) {
	s := newTestServer(t)
	lib := seedLibrary(t, s, "{title}{ext}")

	body := fmt.Sprintf(`{"name":"L2","root_path":%q}`, lib.RootPath)
	rr := patchLibrary(t, s, lib.ID, body)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	var got model.Library
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.FileTemplate != "{title}{ext}" {
		t.Fatalf("file_template = %q, want it unchanged", got.FileTemplate)
	}
}

// write_tags / embed_cover default off, round-trip through PATCH, and an
// omitted key leaves the stored value untouched.
func TestLibrary_TagWritingToggles(t *testing.T) {
	s := newTestServer(t)
	lib := seedLibrary(t, s, "")
	if lib.WriteTags || lib.EmbedCover {
		t.Fatalf("new library defaults: write_tags=%v embed_cover=%v, want both false", lib.WriteTags, lib.EmbedCover)
	}

	body := fmt.Sprintf(`{"name":"L","root_path":%q,"write_tags":true,"embed_cover":true}`, lib.RootPath)
	rr := patchLibrary(t, s, lib.ID, body)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	var got model.Library
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.WriteTags || !got.EmbedCover {
		t.Fatalf("after PATCH: write_tags=%v embed_cover=%v, want both true", got.WriteTags, got.EmbedCover)
	}

	// Persisted, not just echoed.
	if reloaded, err := s.DB.GetLibrary(lib.ID); err != nil || !reloaded.WriteTags || !reloaded.EmbedCover {
		t.Fatalf("reload: %+v err=%v", reloaded, err)
	}

	// A PATCH that omits the keys keeps them.
	rr = patchLibrary(t, s, lib.ID, fmt.Sprintf(`{"name":"L","root_path":%q}`, lib.RootPath))
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.WriteTags || !got.EmbedCover {
		t.Fatalf("omitted keys were cleared: %+v", got)
	}

	// And write_tags:false turns it back off.
	rr = patchLibrary(t, s, lib.ID, fmt.Sprintf(`{"name":"L","root_path":%q,"write_tags":false}`, lib.RootPath))
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.WriteTags {
		t.Fatalf("write_tags still true after PATCH write_tags:false")
	}
}

// PATCH with a template referencing an unknown token is rejected (400) so a typo
// is never baked into filenames.
func TestUpdateLibrary_BogusTemplateIs400(t *testing.T) {
	s := newTestServer(t)
	lib := seedLibrary(t, s, "{title}{ext}")

	body := fmt.Sprintf(`{"name":"L","root_path":%q,"file_template":"{bogus}{ext}"}`, lib.RootPath)
	rr := patchLibrary(t, s, lib.ID, body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", rr.Code, rr.Body.String())
	}
}
