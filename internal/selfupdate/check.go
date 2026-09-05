package selfupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/semver"
)

// Release is a published release of the application.
type Release struct {
	Version     string    `json:"version"`
	Notes       string    `json:"notes"`
	URL         string    `json:"url"`
	PublishedAt time.Time `json:"published_at"`
}

// latestCacheTTL bounds how long a successful Latest result is reused.
// Unauthenticated GitHub API calls are capped at 60/hour per IP; caching keeps a
// busy UI well under that. It is a var so tests can shrink it.
var latestCacheTTL = 15 * time.Minute

// latestNegTTL is how long a failed Latest is remembered so a hung or failing
// GitHub does not make every unauthenticated GET /api/update caller wait out the
// network timeout under the router's request timeout.
var latestNegTTL = 30 * time.Second

// releaseCheckTimeout caps a single release-metadata fetch. It is much shorter
// than assetDownloadTimeout: the payload is a few KiB and the call sits on the
// synchronous /api/update path.
var releaseCheckTimeout = 10 * time.Second

// latestCache memoizes the last Latest outcome, positive and negative. The mutex
// is not held across the network call.
type latestCache struct {
	mu        sync.Mutex
	release   Release
	fetchedAt time.Time
	ok        bool
	failErr   error
	failedAt  time.Time
}

// Latest fetches the newest published release from the GitHub API.
func (u *Updater) Latest(ctx context.Context) (Release, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", u.repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Release{}, fmt.Errorf("build release request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := u.httpClient.Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("fetch latest release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("fetch latest release: github returned %s", resp.Status)
	}

	var body struct {
		TagName     string    `json:"tag_name"`
		Body        string    `json:"body"`
		HTMLURL     string    `json:"html_url"`
		PublishedAt time.Time `json:"published_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return Release{}, fmt.Errorf("decode latest release: %w", err)
	}
	if body.TagName == "" {
		return Release{}, fmt.Errorf("latest release has no tag_name")
	}
	return Release{
		Version:     body.TagName,
		Notes:       body.Body,
		URL:         body.HTMLURL,
		PublishedAt: body.PublishedAt,
	}, nil
}

// LatestCached is Latest with a process-wide cache in front of it: a successful
// result for latestCacheTTL, a failure for latestNegTTL. The network call runs
// without the cache lock held, and each call is bounded by releaseCheckTimeout.
func (u *Updater) LatestCached(ctx context.Context) (Release, error) {
	u.cache.mu.Lock()
	if u.cache.ok && time.Since(u.cache.fetchedAt) < latestCacheTTL {
		rel := u.cache.release
		u.cache.mu.Unlock()
		return rel, nil
	}
	if u.cache.failErr != nil && time.Since(u.cache.failedAt) < latestNegTTL {
		err := u.cache.failErr
		u.cache.mu.Unlock()
		return Release{}, err
	}
	u.cache.mu.Unlock()

	cctx, cancel := context.WithTimeout(ctx, releaseCheckTimeout)
	defer cancel()
	rel, err := u.Latest(cctx)

	u.cache.mu.Lock()
	defer u.cache.mu.Unlock()
	if err != nil {
		u.cache.failErr = err
		u.cache.failedAt = time.Now()
		return Release{}, err
	}
	u.cache.release = rel
	u.cache.fetchedAt = time.Now()
	u.cache.ok = true
	u.cache.failErr = nil
	return rel, nil
}

// HasUpdate reports whether r is a valid semver release strictly newer than the
// running build. A non-semver or "dev" current version is treated as older than
// anything, so any valid remote is an update; an invalid remote is never an
// update, and an equal-or-lower remote is never an update (no downgrades).
func (u *Updater) HasUpdate(r Release) bool {
	remote := NormalizeVersion(r.Version)
	if remote == "" {
		return false
	}
	current := NormalizeVersion(u.currentVersion)
	if current == "" {
		return true
	}
	return semver.Compare(remote, current) > 0
}

// NormalizeVersion ensures a leading "v" and returns "" when the result is not
// valid semver. Callers use it to compare a user-supplied or release-tag
// version string without worrying about the "v" prefix.
func NormalizeVersion(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	if !semver.IsValid(v) {
		return ""
	}
	return v
}
