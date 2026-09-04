package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"audiobookrenamer/internal/matcher"
	"audiobookrenamer/internal/metadata"
	"audiobookrenamer/internal/model"
)

type fakeProvider struct {
	name string
	out  []model.Candidate
}

func (p *fakeProvider) Name() string { return p.name }
func (p *fakeProvider) Search(context.Context, metadata.Query) ([]model.Candidate, error) {
	return p.out, nil
}

func serverWithProviders(t *testing.T, providers ...metadata.Provider) *Server {
	t.Helper()
	s := newTestServer(t)
	s.Matcher = matcher.New(s.DB, metadata.NewRegistryWithProviders(s.DB, providers...), metadata.NewClient(s.DB))
	return s
}

func postSearch(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/search", strings.NewReader(body))
	rr := httptest.NewRecorder()
	s.searchMetadata(rr, req)
	return rr
}

// POST /api/search with a "provider" restricts the query to that one provider.
func TestSearchMetadata_PerProvider(t *testing.T) {
	p1 := &fakeProvider{name: "openlibrary", out: []model.Candidate{{Provider: "openlibrary", ProviderID: "a", Title: "From OL"}}}
	p2 := &fakeProvider{name: "audible", out: []model.Candidate{{Provider: "audible", ProviderID: "b", Title: "From Audible"}}}
	s := serverWithProviders(t, p1, p2)

	rr := postSearch(t, s, `{"provider":"openlibrary","title":"dune"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	var got []model.Candidate
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Provider != "openlibrary" {
		t.Fatalf("per-provider search returned %+v, want only the openlibrary candidate", got)
	}
}

// An empty "provider" keeps the fan-out behaviour across every enabled provider.
func TestSearchMetadata_NoProviderFansOut(t *testing.T) {
	p1 := &fakeProvider{name: "openlibrary", out: []model.Candidate{{Provider: "openlibrary", ProviderID: "a", Title: "From OL"}}}
	p2 := &fakeProvider{name: "audible", out: []model.Candidate{{Provider: "audible", ProviderID: "b", Title: "From Audible"}}}
	s := serverWithProviders(t, p1, p2)

	rr := postSearch(t, s, `{"title":"dune"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var got []model.Candidate
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("fan-out search returned %d candidates, want 2", len(got))
	}
}

// An unknown or disabled provider name is a client error (400), never a 502.
func TestSearchMetadata_UnknownProviderIs400(t *testing.T) {
	p1 := &fakeProvider{name: "openlibrary", out: nil}
	s := serverWithProviders(t, p1)

	rr := postSearch(t, s, `{"provider":"nope","title":"dune"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", rr.Code, rr.Body.String())
	}
}
