package metadata

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"audiobookrenamer/internal/db"
)

// seedAudibleCache primes the response cache with body so audibleProvider.Search
// decodes it instead of hitting the network. The cache key must match the one
// GetJSON computes, so this exercises the real URL the provider builds.
func seedAudibleCache(t *testing.T, d *db.DB, p *audibleProvider, q Query, body []byte) {
	t.Helper()
	reqURL := p.searchURL(q)
	enc, err := json.Marshal(cacheRow{Status: http.StatusOK, Body: json.RawMessage(body)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(
		`INSERT INTO provider_cache (key, provider, body_json, fetched_at) VALUES (?,?,?,?)`,
		p.Name()+"|"+reqURL, p.Name(), string(enc), time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		t.Fatal(err)
	}
}

// A real Audible catalog response carries the series under products[].series,
// with "title" and "sequence". Search must surface both on the candidate.
func TestAudibleSearch_PopulatesSeries(t *testing.T) {
	body, err := os.ReadFile("testdata/audible_series.json")
	if err != nil {
		t.Fatal(err)
	}
	d := testDB(t)
	p := &audibleProvider{http: NewClient(d), region: "us"}
	q := Query{Title: "Dauntless", Author: "Jack Campbell"}
	seedAudibleCache(t, d, p, q, body)

	cands, err := p.Search(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) == 0 {
		t.Fatal("no candidates decoded from the captured Audible response")
	}
	c := cands[0]
	if c.Series != "Lost Fleet" {
		t.Errorf("Series = %q, want %q", c.Series, "Lost Fleet")
	}
	if c.SeriesIndex != "1" {
		t.Errorf("SeriesIndex = %q, want %q", c.SeriesIndex, "1")
	}
}
