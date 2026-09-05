// Package selfupdate checks GitHub Releases for a newer build of the running
// binary and swaps it in place. It mirrors scripts/install.sh's
// download/verify/swap/rollback flow: fetch the release archive matching this
// OS/arch, verify checksums.txt against its detached cosign signature, verify
// the archive against checksums.txt, extract the single binary entry, then hand
// the bytes to github.com/minio/selfupdate to replace the executable (with that
// library's automatic rollback on failure).
//
// The applied update only takes effect after the process re-execs into the new
// binary. The "selfupdate" worker job performs the swap and then asks for a
// restart via RestartRequested; main orchestrates the graceful shutdown and
// calls Exec once its own cleanup has run.
package selfupdate

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// defaultRepo is the public release repository. Tests point ABR_UPDATE_REPO at a
// fake instead.
const defaultRepo = "FlightlessWeasel/audiobookrenamer"

// repoPattern is the accepted shape of ABR_UPDATE_REPO ("owner/name"). Anything
// else is ignored so a malformed value cannot be spliced into a request URL.
var repoPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`)

// redirectHostAllowed reports whether a redirect may land on host. GitHub is
// the only origin we ever start from; it bounces release downloads through
// changing CDN subdomains (objects., codeload., release-assets., ...) all under
// githubusercontent.com, so allow that whole zone plus github.com rather than
// hard-coding a list that breaks every time GitHub renames a host. The asset
// payloads are cosign- and SHA-256-verified regardless of where they are
// fetched from; this check exists mainly to keep the signature-unprotected
// release-check response from being served by a non-GitHub host.
func redirectHostAllowed(host string) bool {
	host = strings.ToLower(host)
	return host == "github.com" ||
		strings.HasSuffix(host, ".github.com") ||
		strings.HasSuffix(host, ".githubusercontent.com")
}

// checkRedirect rejects any redirect that leaves HTTPS or targets a host outside
// GitHub, while keeping net/http's default 10-hop cap.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if req.URL.Scheme != "https" {
		return fmt.Errorf("refusing redirect to non-https URL %q", req.URL.Redacted())
	}
	if !redirectHostAllowed(req.URL.Hostname()) {
		return fmt.Errorf("refusing redirect to untrusted host %q", req.URL.Host)
	}
	return nil
}

// Updater checks for and applies in-place updates of the running executable.
// Construct it with New.
type Updater struct {
	repo           string
	currentVersion string
	httpClient     *http.Client
	execPath       string

	// pubKeyPEM is the PKIX PEM public key that checksums.txt.sig is verified
	// against. It defaults to the embedded release signing key; tests override
	// it with an ephemeral key.
	pubKeyPEM []byte

	// applying serializes Apply. github.com/minio/selfupdate stages the download
	// at a fixed ".new" path with no locking, so two concurrent applies would
	// interleave into one spliced binary.
	applying atomic.Bool

	// restartCh carries a single pending restart request from the worker job to
	// main's run loop. Capacity 1 + a non-blocking send makes repeated requests
	// idempotent and never blocks the sender.
	restartCh chan struct{}

	// canApply* memoize CanApply: it probes the filesystem, and GET /api/update
	// is unauthenticated and hot.
	canApplyOnce sync.Once
	canApplyOK   bool
	canApplyWhy  string

	cache latestCache
}

// Option customizes an Updater built by New.
type Option func(*Updater)

// WithHTTPClient overrides the client used for release checks and asset
// downloads, for a caller that needs a proxy, custom transport, or (in tests) a
// transport that redirects GitHub to a fake. The redirect policy is applied if
// the client does not set its own CheckRedirect.
func WithHTTPClient(c *http.Client) Option {
	return func(u *Updater) { u.setHTTPClient(c) }
}

// New returns an Updater for the running executable. currentVersion is the build
// version injected via ldflags ("dev" for a plain `go build`). The release repo
// defaults to defaultRepo and can be redirected with ABR_UPDATE_REPO (used by
// tests to stand up a fake GitHub); a malformed value is ignored.
func New(currentVersion string, opts ...Option) *Updater {
	repo := defaultRepo
	if v := strings.TrimSpace(os.Getenv("ABR_UPDATE_REPO")); v != "" {
		if repoPattern.MatchString(v) {
			repo = v
		} else {
			slog.Warn("ignoring malformed ABR_UPDATE_REPO", "value", v)
		}
	}
	u := &Updater{
		repo:           repo,
		currentVersion: currentVersion,
		httpClient: &http.Client{
			Timeout:       assetDownloadTimeout,
			CheckRedirect: checkRedirect,
		},
		pubKeyPEM: embeddedPubKey,
		restartCh: make(chan struct{}, 1),
	}
	// Resolve the real path so a swap targets the binary itself, not a symlink
	// that points at it (e.g. /usr/local/bin/abr -> /opt/abr/bin/abr).
	if exe, err := os.Executable(); err == nil {
		if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
			u.execPath = resolved
		} else {
			u.execPath = exe
		}
	}
	for _, o := range opts {
		o(u)
	}
	return u
}

// assetDownloadTimeout bounds a single release-asset download (tens of MiB).
const assetDownloadTimeout = 30 * time.Second

// CurrentVersion is the running build's version string.
func (u *Updater) CurrentVersion() string { return u.currentVersion }

// ExecPath is the resolved path of the binary a swap/restart targets.
func (u *Updater) ExecPath() string { return u.execPath }

// RestartRequested fires once the self-update job has swapped the binary in
// place and the process should re-exec into it. main selects on this to run a
// graceful shutdown before calling Exec.
func (u *Updater) RestartRequested() <-chan struct{} { return u.restartCh }

// requestRestart records a pending restart without blocking; a second request
// before main has drained the first is a no-op.
func (u *Updater) requestRestart() {
	select {
	case u.restartCh <- struct{}{}:
	default:
	}
}

// setHTTPClient overrides the client used for release checks and asset
// downloads. In-package tests use it to redirect GitHub to a fake; the redirect
// policy is applied if the caller did not set one.
func (u *Updater) setHTTPClient(c *http.Client) {
	if c == nil {
		return
	}
	if c.CheckRedirect == nil {
		c.CheckRedirect = checkRedirect
	}
	u.httpClient = c
}
