package selfupdate

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func targetPath(t *testing.T) string {
	t.Helper()
	name := binaryName
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte("old binary\n"), 0o755); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	return p
}

func TestApply_HappyPath(t *testing.T) {
	key, pubPEM := genKey(t)
	newBin := buildStubBinary(t, "2.0.0")
	archive := buildArchive(t, newBin)
	const tag = "v2.0.0"
	sum := sha256.Sum256(archive)

	fr := &fakeRelease{
		tag:       tag,
		archive:   archive,
		checksums: checksumsLine(sum, assetName(tag)),
	}
	fr.sig = signBlob(t, key, fr.checksums)
	srv := fr.server(t)

	u := newTestUpdater(t, "v1.0.0", srv.URL, pubPEM)
	u.execPath = targetPath(t)

	if err := u.Apply(context.Background(), Release{Version: tag}, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	got, err := os.ReadFile(u.execPath)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(got) != string(newBin) {
		t.Fatalf("target contents = %q, want %q", got, newBin)
	}
}

func TestApply_SanityCheckRejectsWrongVersion(t *testing.T) {
	key, pubPEM := genKey(t)
	// A well-formed, correctly signed archive whose binary reports the wrong
	// version — a stand-in for a valid-but-wrong / wrong-arch artifact that
	// still matched the signed checksum.
	newBin := buildStubBinary(t, "9.9.9")
	archive := buildArchive(t, newBin)
	const tag = "v2.0.0"
	sum := sha256.Sum256(archive)

	fr := &fakeRelease{
		tag:       tag,
		archive:   archive,
		checksums: checksumsLine(sum, assetName(tag)),
	}
	fr.sig = signBlob(t, key, fr.checksums)
	srv := fr.server(t)

	u := newTestUpdater(t, "v1.0.0", srv.URL, pubPEM)
	u.execPath = targetPath(t)

	err := u.Apply(context.Background(), Release{Version: tag}, nil)
	if err == nil || !strings.Contains(err.Error(), "sanity-check") {
		t.Fatalf("Apply = %v, want a sanity-check failure", err)
	}
	got, _ := os.ReadFile(u.execPath)
	if string(got) != "old binary\n" {
		t.Fatalf("target was swapped despite a failed sanity check: %q", got)
	}
}

func TestApply_RefusesNonSemverTag(t *testing.T) {
	err := (&Updater{}).Apply(context.Background(), Release{Version: "not-a-version"}, nil)
	if err == nil || !strings.Contains(err.Error(), "non-semver") {
		t.Fatalf("Apply with non-semver tag = %v, want a refusal", err)
	}
}

func TestApply_RejectsConcurrent(t *testing.T) {
	u := &Updater{}
	u.applying.Store(true) // simulate an Apply already running
	if err := u.Apply(context.Background(), Release{Version: "v2.0.0"}, nil); !errors.Is(err, ErrUpdateInProgress) {
		t.Fatalf("second Apply = %v, want ErrUpdateInProgress", err)
	}
}

func TestApply_ChecksumMismatch(t *testing.T) {
	key, pubPEM := genKey(t)
	archive := buildArchive(t, []byte("new binary\n"))
	const tag = "v2.0.0"

	// A well-formed, correctly signed checksums.txt that lists the wrong digest.
	var wrong [32]byte
	fr := &fakeRelease{
		tag:       tag,
		archive:   archive,
		checksums: checksumsLine(wrong, assetName(tag)),
	}
	fr.sig = signBlob(t, key, fr.checksums)
	srv := fr.server(t)

	u := newTestUpdater(t, "v1.0.0", srv.URL, pubPEM)
	u.execPath = targetPath(t)

	if err := u.Apply(context.Background(), Release{Version: tag}, nil); err == nil {
		t.Fatal("Apply: want error on checksum mismatch, got nil")
	}
	got, err := os.ReadFile(u.execPath)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(got) != "old binary\n" {
		t.Fatalf("target was modified despite checksum mismatch: %q", got)
	}
}

func TestApply_BadSignature(t *testing.T) {
	_, pubPEM := genKey(t)   // key the Updater trusts
	otherKey, _ := genKey(t) // key that actually signs — not trusted
	archive := buildArchive(t, []byte("new binary\n"))
	const tag = "v2.0.0"
	sum := sha256.Sum256(archive)

	fr := &fakeRelease{
		tag:       tag,
		archive:   archive,
		checksums: checksumsLine(sum, assetName(tag)),
	}
	fr.sig = signBlob(t, otherKey, fr.checksums)
	srv := fr.server(t)

	u := newTestUpdater(t, "v1.0.0", srv.URL, pubPEM)
	u.execPath = targetPath(t)

	if err := u.Apply(context.Background(), Release{Version: tag}, nil); err == nil {
		t.Fatal("Apply: want error on bad signature, got nil")
	}
	if n := fr.archiveHits.Load(); n != 0 {
		t.Fatalf("archive was downloaded %d time(s) despite a bad signature", n)
	}
	got, _ := os.ReadFile(u.execPath)
	if string(got) != "old binary\n" {
		t.Fatalf("target was modified despite a bad signature: %q", got)
	}
}

func TestApply_MissingSignature(t *testing.T) {
	_, pubPEM := genKey(t)
	archive := buildArchive(t, []byte("new binary\n"))
	const tag = "v2.0.0"
	sum := sha256.Sum256(archive)

	fr := &fakeRelease{
		tag:       tag,
		archive:   archive,
		checksums: checksumsLine(sum, assetName(tag)),
		sig:       nil, // 404
	}
	srv := fr.server(t)

	u := newTestUpdater(t, "v1.0.0", srv.URL, pubPEM)
	u.execPath = targetPath(t)

	if err := u.Apply(context.Background(), Release{Version: tag}, nil); err == nil {
		t.Fatal("Apply: want error on missing signature, got nil")
	}
	if n := fr.archiveHits.Load(); n != 0 {
		t.Fatalf("archive was downloaded %d time(s) despite a missing signature", n)
	}
}
