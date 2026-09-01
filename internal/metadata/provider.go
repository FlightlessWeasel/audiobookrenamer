// Package metadata queries external book catalogues (Audible, Google Books,
// Open Library) and returns normalized candidates. It performs no scoring or
// persistence beyond an HTTP response cache.
package metadata

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"audiobookrenamer/internal/db"
	"audiobookrenamer/internal/model"
)

// ErrProviderNotAvailable is returned by SearchProvider when the requested
// provider name is unknown or is not currently enabled. It is a client error
// (the caller asked for something that doesn't exist), not an upstream failure.
var ErrProviderNotAvailable = errors.New("metadata provider not available")

// Query describes what to look for. For a scanned book, Title/Author/Year are
// set; for a manual UI search, Freeform carries the raw string.
type Query struct {
	Title    string
	Author   string
	Year     int
	Narrator string
	Freeform string
}

func (q Query) keywords() string {
	if s := strings.TrimSpace(q.Freeform); s != "" {
		return s
	}
	parts := make([]string, 0, 2)
	if q.Title != "" {
		parts = append(parts, q.Title)
	}
	if q.Author != "" {
		parts = append(parts, q.Author)
	}
	return strings.Join(parts, " ")
}

func (q Query) empty() bool { return q.keywords() == "" }

// Provider is one external catalogue.
type Provider interface {
	Name() string
	Search(ctx context.Context, q Query) ([]model.Candidate, error)
}

// Registry fans a query out to every enabled provider. Provider enablement and
// config are read from the settings table on each call so UI changes take
// effect immediately.
type Registry struct {
	db   *db.DB
	http *Client

	// providers, when non-nil, is used verbatim instead of building the list
	// from provider config. Set by NewRegistryWithProviders for deterministic
	// wiring and tests.
	providers []Provider
}

// NewRegistry wires a Registry to the database (for settings + HTTP cache).
func NewRegistry(database *db.DB) *Registry {
	return &Registry{db: database, http: NewClient(database)}
}

// NewRegistryWithProviders builds a Registry whose enabled-provider set is the
// given fixed list, bypassing the settings-backed config. Intended for tests
// that need a deterministic provider set without reaching the network.
func NewRegistryWithProviders(database *db.DB, providers ...Provider) *Registry {
	r := NewRegistry(database)
	r.providers = providers
	return r
}

func (r *Registry) enabledProviders() ([]Provider, error) {
	if r.providers != nil {
		return r.providers, nil
	}
	cfg, err := LoadProviderConfig(r.db)
	if err != nil {
		return nil, err
	}
	var ps []Provider
	if cfg.Audible.Enabled {
		ps = append(ps, &audibleProvider{http: r.http, region: cfg.Audible.Region})
	}
	if cfg.GoogleBooks.Enabled {
		ps = append(ps, &googleBooksProvider{http: r.http, apiKey: cfg.GoogleBooks.APIKey})
	}
	if cfg.OpenLibrary.Enabled {
		ps = append(ps, &openLibraryProvider{http: r.http})
	}
	return ps, nil
}

// Search queries all enabled providers concurrently and returns the combined,
// de-duplicated candidate list (unscored). Individual provider failures are
// logged and skipped.
func (r *Registry) Search(ctx context.Context, q Query) ([]model.Candidate, error) {
	if q.empty() {
		return nil, nil
	}
	providers, err := r.enabledProviders()
	if err != nil {
		return nil, err
	}
	if len(providers) == 0 {
		return nil, nil
	}

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		all      []model.Candidate
		failures int
	)
	for _, p := range providers {
		wg.Add(1)
		go func(p Provider) {
			defer wg.Done()
			res, err := p.Search(ctx, q)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failures++
				slog.Warn("provider search failed", "provider", p.Name(), "err", err)
				return
			}
			all = append(all, res...)
		}(p)
	}
	wg.Wait()

	// Distinguish "every provider errored" from "providers ran and found
	// nothing" — the caller should surface the former as a failure, not as an
	// empty result set.
	if failures == len(providers) && len(all) == 0 {
		return nil, fmt.Errorf("all %d metadata providers failed", failures)
	}

	return dedupe(all), nil
}

// SearchProvider queries a single named provider, chosen from the enabled set by
// Name(). An unknown or disabled name yields ErrProviderNotAvailable (a client
// error); a provider that runs but fails yields that provider's error.
func (r *Registry) SearchProvider(ctx context.Context, name string, q Query) ([]model.Candidate, error) {
	if q.empty() {
		return nil, nil
	}
	providers, err := r.enabledProviders()
	if err != nil {
		return nil, err
	}
	for _, p := range providers {
		if p.Name() == name {
			res, err := p.Search(ctx, q)
			if err != nil {
				return nil, err
			}
			return dedupe(res), nil
		}
	}
	return nil, fmt.Errorf("%q: %w", name, ErrProviderNotAvailable)
}

// collectCandidates maps each item of a provider's decoded search response to a
// Candidate and keeps the ones that named a book.
//
// Every provider ends its Search the same way, and the untitled-result filter
// in particular has to stay identical across all of them: a candidate with no
// title cannot be scored by internal/matching or shown in the review UI, so one
// provider quietly keeping them would put unpickable rows in front of the user.
// Only the per-item field mapping differs, which is what build supplies.
func collectCandidates[T any](items []T, build func(T) model.Candidate) []model.Candidate {
	out := make([]model.Candidate, 0, len(items))
	for _, it := range items {
		if c := build(it); c.Title != "" {
			out = append(out, c)
		}
	}
	return out
}

// dedupe collapses candidates that are obviously the same edition (same ASIN,
// same ISBN, or same provider+id), keeping the first seen.
// It allocates its own slice rather than filtering into in[:0], which would
// overwrite the caller's backing array.
func dedupe(in []model.Candidate) []model.Candidate {
	seen := map[string]bool{}
	out := make([]model.Candidate, 0, len(in))
	for _, c := range in {
		keys := []string{c.Provider + ":" + c.ProviderID}
		if c.ASIN != "" {
			keys = append(keys, "asin:"+strings.ToLower(c.ASIN))
		}
		if c.ISBN != "" {
			keys = append(keys, "isbn:"+c.ISBN)
		}
		dup := false
		for _, k := range keys {
			if seen[k] {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		for _, k := range keys {
			seen[k] = true
		}
		out = append(out, c)
	}
	// Stable order: Audible first (richest audiobook data), then others.
	sort.SliceStable(out, func(i, j int) bool {
		return providerRank(out[i].Provider) < providerRank(out[j].Provider)
	})
	return out
}

func providerRank(name string) int {
	switch name {
	case "audible":
		return 0
	case "googlebooks":
		return 1
	case "openlibrary":
		return 2
	default:
		return 3
	}
}
