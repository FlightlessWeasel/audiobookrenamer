package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// hostRewrite redirects every request to a test server, keeping the path so the
// fake can route on it. It lets the production code keep its hardcoded
// github.com / api.github.com URLs.
type hostRewrite struct{ scheme, host string }

func (rt *hostRewrite) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme = rt.scheme
	r.URL.Host = rt.host
	r.Host = ""
	return http.DefaultTransport.RoundTrip(r)
}

func genKey(t *testing.T) (*ecdsa.PrivateKey, []byte) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		t.Fatalf("marshal pubkey: %v", err)
	}
	return k, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
}

// signBlob signs blob the way `cosign sign-blob --key k --output-signature`
// does for a P-256 key: base64 of the ASN.1 DER ECDSA signature over the
// SHA-256 of the blob.
func signBlob(t *testing.T, k *ecdsa.PrivateKey, blob []byte) []byte {
	t.Helper()
	sum := sha256.Sum256(blob)
	der, err := ecdsa.SignASN1(rand.Reader, k, sum[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return []byte(base64.StdEncoding.EncodeToString(der))
}

// assetName mirrors the name Apply computes for the running OS/arch.
func assetName(version string) string {
	ext := ".tar.gz"
	if runtime.GOOS == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("%s_%s_%s_%s%s", binaryName,
		strings.TrimPrefix(version, "v"), runtime.GOOS, runtime.GOARCH, ext)
}

// buildArchive packs binContent as the single binary entry, in the format Apply
// expects for the running OS (zip on Windows, tar.gz elsewhere).
func buildArchive(t *testing.T, binContent []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	if runtime.GOOS == "windows" {
		zw := zip.NewWriter(&buf)
		w, err := zw.Create(binaryName + ".exe")
		if err != nil {
			t.Fatalf("zip create: %v", err)
		}
		if _, err := w.Write(binContent); err != nil {
			t.Fatalf("zip write: %v", err)
		}
		if err := zw.Close(); err != nil {
			t.Fatalf("zip close: %v", err)
		}
		return buf.Bytes()
	}
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	if err := tw.WriteHeader(&tar.Header{
		Name: binaryName, Mode: 0o755, Size: int64(len(binContent)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	if _, err := tw.Write(binContent); err != nil {
		t.Fatalf("tar write: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// fakeRelease serves the GitHub API "latest" JSON plus the release asset files.
type fakeRelease struct {
	tag         string
	archive     []byte
	checksums   []byte
	sig         []byte // nil => 404 on checksums.txt.sig
	archiveHits atomic.Int64
}

func (fr *fakeRelease) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name":     fr.tag,
				"body":         "release notes",
				"html_url":     "https://example.test/releases/" + fr.tag,
				"published_at": time.Now().UTC(),
			})
		case strings.HasSuffix(r.URL.Path, "/checksums.txt.sig"):
			if fr.sig == nil {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(fr.sig)
		case strings.HasSuffix(r.URL.Path, "/checksums.txt"):
			_, _ = w.Write(fr.checksums)
		case strings.HasSuffix(r.URL.Path, assetName(fr.tag)):
			fr.archiveHits.Add(1)
			_, _ = w.Write(fr.archive)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newTestUpdater builds an Updater whose HTTP client is redirected to srv.
func newTestUpdater(t *testing.T, current, srvURL string, pubPEM []byte) *Updater {
	t.Helper()
	base, err := url.Parse(srvURL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}
	return &Updater{
		repo:           "acme/audiobookrenamer",
		currentVersion: current,
		httpClient:     &http.Client{Transport: &hostRewrite{scheme: base.Scheme, host: base.Host}},
		pubKeyPEM:      pubPEM,
	}
}

// checksumsLine formats one GoReleaser checksums.txt line.
func checksumsLine(sum [32]byte, name string) []byte {
	return []byte(fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), name))
}

// buildStubBinary compiles a tiny real executable that prints versionOut when
// run with "-version" and nothing otherwise. Apply's pre-swap sanity check
// actually executes the extracted binary, so tests that reach it need a real
// one rather than arbitrary bytes.
func buildStubBinary(t *testing.T, versionOut string) []byte {
	t.Helper()
	dir := t.TempDir()
	src := fmt.Sprintf(`package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "-version" {
		fmt.Println(%q)
	}
}
`, versionOut)
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write stub source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module abrstub\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatalf("write stub go.mod: %v", err)
	}
	out := filepath.Join(dir, "stub")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", out, ".")
	cmd.Dir = dir
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build stub binary: %v\n%s", err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read stub binary: %v", err)
	}
	return b
}
