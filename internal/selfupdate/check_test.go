package selfupdate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestHasUpdate(t *testing.T) {
	cases := []struct {
		name    string
		current string
		remote  string
		want    bool
	}{
		{"equal version", "v1.2.3", "v1.2.3", false},
		{"older remote", "v1.2.3", "v1.0.0", false},
		{"newer remote", "v1.2.3", "v1.3.0", true},
		{"newer remote without v prefix", "v1.2.3", "1.3.0", true},
		{"dev current, valid remote", "dev", "v0.0.1", true},
		{"garbage remote", "v1.2.3", "banana", false},
		{"garbage remote, dev current", "dev", "banana", false},
		{"empty remote", "v1.2.3", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u := &Updater{currentVersion: c.current}
			if got := u.HasUpdate(Release{Version: c.remote}); got != c.want {
				t.Fatalf("HasUpdate(current=%q, remote=%q) = %v, want %v",
					c.current, c.remote, got, c.want)
			}
		})
	}
}

func TestLatestCached_UsesCache(t *testing.T) {
	fr := &fakeRelease{tag: "v1.2.3"}
	srv := fr.server(t)
	u := newTestUpdater(t, "v1.0.0", srv.URL, nil)

	first, err := u.LatestCached(context.Background())
	if err != nil {
		t.Fatalf("LatestCached: %v", err)
	}
	if first.Version != "v1.2.3" {
		t.Fatalf("version = %q, want v1.2.3", first.Version)
	}

	// Close the upstream: a second call must still succeed from cache.
	srv.Close()
	second, err := u.LatestCached(context.Background())
	if err != nil {
		t.Fatalf("LatestCached (cached): %v", err)
	}
	if second.Version != first.Version {
		t.Fatalf("cached version = %q, want %q", second.Version, first.Version)
	}
}

func TestCheckRedirect(t *testing.T) {
	mk := func(raw string) *http.Request {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("parse %q: %v", raw, err)
		}
		return &http.Request{Method: http.MethodGet, URL: u}
	}
	cases := []struct {
		name    string
		target  string
		wantErr bool
	}{
		{"github asset host", "https://objects.githubusercontent.com/x", false},
		{"release-assets host", "https://release-assets.githubusercontent.com/x", false},
		{"api host", "https://api.github.com/repos/x", false},
		{"codeload host", "https://codeload.github.com/x", false},
		{"downgrade to http", "http://github.com/x", true},
		{"foreign https host", "https://evil.example/x", true},
		{"lookalike suffix", "https://github.com.evil.example/x", true},
		{"githubusercontent lookalike", "https://githubusercontent.com.evil.example/x", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := checkRedirect(mk(c.target), nil)
			if (err != nil) != c.wantErr {
				t.Fatalf("checkRedirect(%q) err = %v, wantErr = %v", c.target, err, c.wantErr)
			}
		})
	}
	if err := checkRedirect(mk("https://github.com/x"), make([]*http.Request, 10)); err == nil {
		t.Fatal("checkRedirect with 10 prior hops = nil, want the hop-cap error")
	}
}

func TestLatest_RejectsRedirectOffGitHub(t *testing.T) {
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("client followed the redirect to %s", r.URL)
	}))
	t.Cleanup(evil.Close)
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evil.URL+"/releases/latest", http.StatusFound)
	}))
	t.Cleanup(redir.Close)

	u := newTestUpdater(t, "v1.0.0", redir.URL, nil)
	u.httpClient.CheckRedirect = checkRedirect

	if _, err := u.Latest(context.Background()); err == nil {
		t.Fatal("Latest across an off-GitHub redirect = nil error, want it rejected")
	}
}

func TestCanApply_DevBuild(t *testing.T) {
	u := &Updater{currentVersion: "dev"}
	ok, reason := u.CanApply()
	if ok {
		t.Fatal("CanApply() = true, want false for a dev build")
	}
	if !strings.Contains(reason, "development build") {
		t.Fatalf("reason = %q, want it to mention a development build", reason)
	}
}
