package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"runtime"
	"strings"
	"time"

	"github.com/minio/selfupdate"
)

// binaryName is the executable's name inside a release archive (a ".exe" suffix
// is also accepted, for the Windows zip).
const binaryName = "audiobookrenamer"

// maxAssetBytes caps how many bytes we read from any single downloaded file or
// archive entry — a guard against a decompression bomb in a tampered asset. The
// real binary is tens of MiB.
const maxAssetBytes = 512 << 20

// sanityCheckTimeout bounds the "run the new binary with -version" probe.
const sanityCheckTimeout = 10 * time.Second

// ErrUpdateInProgress is returned by Apply when another Apply is already running
// on this Updater. github.com/minio/selfupdate stages the download at a fixed
// path with no locking, so concurrent applies would corrupt the binary.
var ErrUpdateInProgress = errors.New("an update is already in progress")

// Apply downloads the release archive for r that matches the running OS/arch,
// verifies checksums.txt against its detached signature and the archive against
// checksums.txt, extracts the single binary entry, sanity-checks it, and swaps
// it over the running executable (with automatic rollback on failure). It does
// not restart the process. progress, if non-nil, is called with a 0..100
// percentage and a short status message.
func (u *Updater) Apply(ctx context.Context, r Release, progress func(pct int, msg string)) error {
	if !u.applying.CompareAndSwap(false, true) {
		return ErrUpdateInProgress
	}
	defer u.applying.Store(false)

	if progress == nil {
		progress = func(int, string) {}
	}
	tag := strings.TrimSpace(r.Version)
	if NormalizeVersion(tag) == "" {
		return fmt.Errorf("refusing to fetch release with non-semver tag %q", tag)
	}
	ver := strings.TrimPrefix(tag, "v")
	ext := ".tar.gz"
	if runtime.GOOS == "windows" {
		ext = ".zip"
	}
	asset := fmt.Sprintf("%s_%s_%s_%s%s", binaryName, ver, runtime.GOOS, runtime.GOARCH, ext)
	base := fmt.Sprintf("https://github.com/%s/releases/download/%s", u.repo, tag)

	progress(0, "downloading checksums")
	sums, err := u.downloadBytes(ctx, base+"/checksums.txt")
	if err != nil {
		return fmt.Errorf("download checksums.txt: %w", err)
	}
	sig, err := u.downloadBytes(ctx, base+"/checksums.txt.sig")
	if err != nil {
		return fmt.Errorf("download checksums.txt.sig: %w", err)
	}

	progress(10, "verifying signature")
	if err := verifyChecksums(sig, sums, u.pubKeyPEM); err != nil {
		return fmt.Errorf("verify checksums.txt: %w", err)
	}
	wantSum, ok := checksumFor(sums, asset)
	if !ok {
		return fmt.Errorf("checksums.txt has no entry for %s", asset)
	}

	progress(20, "downloading "+asset)
	archivePath, err := u.downloadToTemp(ctx, base+"/"+asset)
	if err != nil {
		return fmt.Errorf("download %s: %w", asset, err)
	}
	defer os.Remove(archivePath)

	progress(60, "verifying download")
	gotSum, err := sha256File(archivePath)
	if err != nil {
		return fmt.Errorf("hash %s: %w", asset, err)
	}
	if gotSum != wantSum {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s", asset, gotSum, wantSum)
	}

	progress(70, "extracting")
	binPath, err := extractBinary(archivePath, ext)
	if err != nil {
		return fmt.Errorf("extract binary from %s: %w", asset, err)
	}
	defer os.Remove(binPath)

	progress(80, "verifying new binary")
	if err := sanityCheckBinary(ctx, binPath, ver); err != nil {
		return fmt.Errorf("sanity-check new binary: %w", err)
	}

	progress(85, "installing")
	f, err := os.Open(binPath)
	if err != nil {
		return fmt.Errorf("open extracted binary: %w", err)
	}
	defer f.Close()

	if err := selfupdate.Apply(f, selfupdate.Options{TargetPath: u.execPath}); err != nil {
		if rerr := selfupdate.RollbackError(err); rerr != nil {
			return fmt.Errorf("apply update: %w; rollback also failed, binary may be broken: %v", err, rerr)
		}
		return fmt.Errorf("apply update (rolled back): %w", err)
	}

	progress(100, "installed "+tag)
	return nil
}

// sanityCheckBinary runs the freshly extracted binary with -version and requires
// it to exit 0 and print a string containing wantVer. This catches a
// wrong-arch, truncated, or otherwise broken download that still matched the
// signed checksum (e.g. the checksum of a valid-but-wrong artifact) before it is
// swapped into place.
func sanityCheckBinary(ctx context.Context, binPath, wantVer string) error {
	// tar/zip extraction does not always preserve the executable bit.
	if err := os.Chmod(binPath, 0o755); err != nil {
		return fmt.Errorf("chmod extracted binary: %w", err)
	}
	cctx, cancel := context.WithTimeout(ctx, sanityCheckTimeout)
	defer cancel()

	out, err := exec.CommandContext(cctx, binPath, "-version").Output()
	if err != nil {
		return fmt.Errorf("run %s -version: %w", filepathBase(binPath), err)
	}
	got := strings.TrimSpace(string(out))
	if !strings.Contains(got, wantVer) {
		return fmt.Errorf("new binary reported version %q, expected it to contain %q", got, wantVer)
	}
	return nil
}

func filepathBase(p string) string {
	return path.Base(strings.ReplaceAll(p, `\`, "/"))
}

// downloadBytes GETs url fully into memory.
func (u *Updater) downloadBytes(ctx context.Context, url string) ([]byte, error) {
	resp, err := u.get(ctx, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, maxAssetBytes))
}

// downloadToTemp streams url to a temp file and returns its path. The caller
// removes it.
func (u *Updater) downloadToTemp(ctx context.Context, url string) (string, error) {
	resp, err := u.get(ctx, url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	return spoolToTemp("abr-update-*.archive", resp.Body)
}

// spoolToTemp streams r (capped at maxAssetBytes) into a fresh temp file named
// from pattern and returns its path. The caller removes it.
func spoolToTemp(pattern string, r io.Reader) (string, error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", err
	}
	_, copyErr := io.Copy(f, io.LimitReader(r, maxAssetBytes))
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil {
		os.Remove(f.Name())
		if copyErr != nil {
			return "", copyErr
		}
		return "", closeErr
	}
	return f.Name(), nil
}

func (u *Updater) get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := u.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return resp, nil
}

// checksumFor returns the lowercase hex digest listed for name in a GoReleaser
// checksums.txt body ("<hex>  <filename>" per line, optionally "*<filename>").
func checksumFor(sums []byte, name string) (string, bool) {
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if strings.TrimPrefix(fields[1], "*") == name {
			return strings.ToLower(fields[0]), true
		}
	}
	return "", false
}

func sha256File(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// extractBinary writes the single binaryName(.exe) entry from the release
// archive to a temp file and returns its path. ext selects the archive format.
func extractBinary(archivePath, ext string) (string, error) {
	if ext == ".zip" {
		return extractBinaryZip(archivePath)
	}
	return extractBinaryTarGz(archivePath)
}

func wantBinaryEntry(name string) bool {
	b := path.Base(strings.ReplaceAll(name, `\`, "/"))
	return b == binaryName || b == binaryName+".exe"
}

// writeTempBinary copies r to a fresh temp file and returns its path. On Windows
// the file keeps a .exe suffix so the sanity check can execute it. The caller
// removes it.
func writeTempBinary(r io.Reader) (string, error) {
	pattern := "abr-update-bin-*"
	if runtime.GOOS == "windows" {
		pattern = "abr-update-bin-*.exe"
	}
	return spoolToTemp(pattern, r)
}

func extractBinaryTarGz(archivePath string) (string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		if hdr.Typeflag != tar.TypeReg || !wantBinaryEntry(hdr.Name) {
			continue
		}
		return writeTempBinary(tr)
	}
	return "", fmt.Errorf("archive contains no %s entry", binaryName)
}

func extractBinaryZip(archivePath string) (string, error) {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return "", err
	}
	defer zr.Close()

	for _, zf := range zr.File {
		if zf.FileInfo().IsDir() || !wantBinaryEntry(zf.Name) {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return "", err
		}
		p, err := writeTempBinary(rc)
		rc.Close()
		return p, err
	}
	return "", fmt.Errorf("archive contains no %s entry", binaryName)
}
