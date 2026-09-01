package metadata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A failed cache persistence write is advisory: GetJSON still returns the
// freshly-fetched, decoded body, but the failure is logged (not swallowed) so
// an operator can see the cache is not working and every request is re-hitting
// upstream.
func TestGetJSON_CacheWriteFailureIsAdvisory(t *testing.T) {
	d := testDB(t)
	logs := captureLogs(t)

	// Make every provider_cache insert abort, standing in for a locked/full DB.
	if _, err := d.Exec(
		`CREATE TRIGGER abr_block_cache_insert BEFORE INSERT ON provider_cache
		 BEGIN SELECT RAISE(ABORT, 'cache write blocked for test'); END`,
	); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"title":"Dune"}`))
	}))
	defer srv.Close()

	c := NewClient(d)
	var out struct {
		Title string `json:"title"`
	}
	if err := c.GetJSON(context.Background(), "test", srv.URL, nil, &out); err != nil {
		t.Fatalf("GetJSON returned an error despite the cache being advisory: %v", err)
	}
	if out.Title != "Dune" {
		t.Fatalf("decoded body = %+v, want Title=Dune", out)
	}
	if !logs.contains("metadata cache write failed") {
		t.Fatalf("expected a warn log for the failed cache write; got: %v", logs.all())
	}
}
