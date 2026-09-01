package metadata

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"audiobookrenamer/internal/db"
)

// Client is a small JSON HTTP client with a SQLite-backed response cache and
// per-host rate limiting, shared by all providers.
type Client struct {
	db  *db.DB
	hc  *http.Client
	ttl time.Duration

	mu        sync.Mutex
	lastHit   map[string]time.Time
	minGap    time.Duration
	userAgent string
}

// NewClient returns a Client with a 7-day cache TTL and a 1100ms per-host gap.
func NewClient(database *db.DB) *Client {
	return &Client{
		db:        database,
		hc:        &http.Client{Timeout: 15 * time.Second},
		ttl:       7 * 24 * time.Hour,
		lastHit:   map[string]time.Time{},
		minGap:    1100 * time.Millisecond,
		userAgent: "audiobookrenamer/0.1 (+https://github.com/; library manager)",
	}
}

type cacheRow struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    json.RawMessage   `json:"body"`
}

// GetJSON fetches reqURL (GET) and decodes the JSON body into out. Responses
// are cached per (provider,url); a fresh cache entry skips the network.
func (c *Client) GetJSON(ctx context.Context, provider, reqURL string, headers map[string]string, out any) error {
	key := provider + "|" + reqURL

	if raw, fetchedAt, ok := c.readCache(key); ok && time.Since(fetchedAt) < c.ttl {
		var cr cacheRow
		if err := json.Unmarshal(raw, &cr); err == nil && cr.Status == http.StatusOK {
			return json.Unmarshal(cr.Body, out)
		}
	}

	body, err := c.fetch(ctx, reqURL, headers)
	if err != nil {
		return err
	}
	// The response was fetched successfully; the cache is advisory (it only
	// saves a future upstream round-trip), so a marshal or write failure is
	// logged and the fresh body is still returned. This matches the readCache
	// precedent above.
	if enc, err := json.Marshal(cacheRow{Status: http.StatusOK, Body: json.RawMessage(body)}); err != nil {
		slog.Warn("metadata cache write failed", "key", key, "provider", provider, "err", err)
	} else if err := c.writeCache(key, provider, enc); err != nil {
		slog.Warn("metadata cache write failed", "key", key, "provider", provider, "err", err)
	}
	return json.Unmarshal(body, out)
}

func (c *Client) fetch(ctx context.Context, reqURL string, headers map[string]string) ([]byte, error) {
	u, err := url.Parse(reqURL)
	if err != nil {
		return nil, err
	}
	if err := c.throttle(ctx, u.Host); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	// Drain any remainder so the keep-alive connection can be reused.
	_, _ = io.Copy(io.Discard, resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		snippet := data
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		return nil, fmt.Errorf("%s -> %d: %s", u.Host, resp.StatusCode, snippet)
	}
	return data, nil
}

// throttle enforces a minimum gap between requests to the same host. It computes
// the wait while holding the lock, releases the lock, then sleeps — so a slow
// host never blocks requests to other hosts — and aborts the wait if ctx is
// canceled.
func (c *Client) throttle(ctx context.Context, host string) error {
	c.mu.Lock()
	now := time.Now()
	var wait time.Duration
	if last, ok := c.lastHit[host]; ok {
		if gap := c.minGap - now.Sub(last); gap > 0 {
			wait = gap
		}
	}
	// Reserve this host's next slot at now+wait so other callers for the same
	// host queue behind us instead of racing on the same stale timestamp.
	c.lastHit[host] = now.Add(wait)
	c.mu.Unlock()

	if wait <= 0 {
		return nil
	}
	select {
	case <-time.After(wait):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) readCache(key string) (json.RawMessage, time.Time, bool) {
	var body, fetched string
	err := c.db.QueryRow(`SELECT body_json, fetched_at FROM provider_cache WHERE key = ?`, key).Scan(&body, &fetched)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, time.Time{}, false
	}
	if err != nil {
		// A genuine read failure (I/O, locked DB) is still treated as a miss so
		// the caller falls back to the network, but it must not pass silently.
		slog.Warn("metadata cache read failed", "key", key, "err", err)
		return nil, time.Time{}, false
	}
	t, _ := time.Parse(time.RFC3339Nano, fetched)
	return json.RawMessage(body), t, true
}

// writeCache upserts the already-marshalled cache row body. The error is
// returned (not swallowed) so the single call site can log a persistence
// failure; the cache is advisory, so that call site does not treat it as fatal.
func (c *Client) writeCache(key, provider string, body []byte) error {
	_, err := c.db.Exec(
		`INSERT INTO provider_cache (key, provider, body_json, fetched_at) VALUES (?,?,?,?)
		 ON CONFLICT(key) DO UPDATE SET body_json = excluded.body_json, fetched_at = excluded.fetched_at`,
		key, provider, string(body), time.Now().UTC().Format(time.RFC3339Nano),
	)
	return err
}
