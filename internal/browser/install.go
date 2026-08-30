package browser

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

// chromeManifest pins one Chrome for Testing build per platform. Chrome for
// Testing publishes download URLs but no hashes, so the checksums are computed
// once by tools/pin-chrome and committed here; see PLAN.md Section 5.
//
//go:embed chrome.json
var chromeManifest []byte

// Manifest is the pinned browser manifest, in the shape of chrome.json.
type Manifest struct {
	Version   string           `json:"version"`
	Platforms map[string]Build `json:"platforms"`
}

// Build is one platform's archive.
type Build struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	// Binary is the slash-separated path of the browser executable inside the
	// archive.
	Binary string `json:"binary"`

	platform string
}

var pinned = func() Manifest {
	var m Manifest
	if err := json.Unmarshal(chromeManifest, &m); err != nil {
		panic("browser: chrome.json: " + err.Error())
	}
	return m
}()

// Pinned returns the pinned browser manifest.
func Pinned() Manifest { return pinned }

// Platform is the Chrome for Testing platform name for this machine.
func Platform() string {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "darwin/arm64":
		return "mac-arm64"
	case "darwin/amd64":
		return "mac-x64"
	case "linux/amd64":
		return "linux64"
	case "windows/amd64":
		return "win64"
	case "windows/386":
		return "win32"
	}
	return ""
}

func pinnedBuild() (Build, error) {
	platform := Platform()
	build, ok := pinned.Platforms[platform]
	if !ok || build.URL == "" {
		return Build{}, fmt.Errorf("no pinned Chrome for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	build.platform = platform
	return build, nil
}

// Install downloads and extracts the pinned Chrome for Testing build into the
// user cache directory and returns the path of its executable. An existing
// install is reused unless force is set. Progress is written to progress, which
// may be nil.
func Install(ctx context.Context, force bool, progress io.Writer) (string, error) {
	build, err := pinnedBuild()
	if err != nil {
		return "", err
	}
	dir, err := installDir()
	if err != nil {
		return "", err
	}
	exe := filepath.Join(dir, filepath.FromSlash(build.Binary))
	if !force {
		if _, err := os.Stat(exe); err == nil {
			logf(progress, "Chrome %s already installed at %s", pinned.Version, exe)
			return exe, nil
		} else if !missing(err) {
			return "", err
		}
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}

	logf(progress, "Downloading Chrome %s for %s", pinned.Version, build.platform)
	archive, err := download(ctx, build)
	if err != nil {
		return "", err
	}
	defer os.Remove(archive.Name())
	defer archive.Close()

	// Extract beside the destination and rename, so an interrupted install
	// never leaves a half-extracted browser where discovery would find it.
	staging, err := os.MkdirTemp(filepath.Dir(dir), ".shots-chrome-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(staging)
	size, err := archive.Seek(0, io.SeekEnd)
	if err != nil {
		return "", err
	}
	if err := unzip(archive, size, staging); err != nil {
		return "", fmt.Errorf("extracting %s: %w", build.URL, err)
	}
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	if err := os.Rename(staging, dir); err != nil {
		return "", err
	}
	if _, err := os.Stat(exe); err != nil {
		return "", fmt.Errorf("archive did not contain %s: %w", build.Binary, err)
	}
	logf(progress, "Installed Chrome %s to %s", pinned.Version, exe)
	return exe, nil
}

func logf(w io.Writer, format string, args ...any) {
	if w != nil {
		fmt.Fprintf(w, format+"\n", args...)
	}
}

// download fetches build into a temporary file and verifies its checksum. The
// caller owns the returned file, whose read offset is undefined.
func download(ctx context.Context, build Build) (*os.File, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, build.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", build.URL, resp.Status)
	}
	f, err := os.CreateTemp("", "shots-chrome-*.zip")
	if err != nil {
		return nil, err
	}
	sum := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, sum), resp.Body); err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, err
	}
	if got := hex.EncodeToString(sum.Sum(nil)); got != build.SHA256 {
		f.Close()
		os.Remove(f.Name())
		return nil, fmt.Errorf("%s: checksum mismatch: manifest has %s, download is %s", build.URL, build.SHA256, got)
	}
	return f, nil
}

// unzip extracts a zip archive into dir, preserving executable bits and
// symbolic links (macOS application bundles are full of them) and rejecting
// entries that would escape dir.
func unzip(r io.ReaderAt, size int64, dir string) error {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return err
	}
	for _, f := range zr.File {
		target, err := safeJoin(dir, f.Name)
		if err != nil {
			return err
		}
		mode := f.Mode()
		switch {
		case mode.IsDir():
			err = os.MkdirAll(target, 0o755)
		case mode&fs.ModeSymlink != 0:
			err = extractSymlink(f, dir, target)
		case mode.IsRegular():
			err = extractFile(f, target)
		default:
			err = fmt.Errorf("%s: unsupported file type %s", f.Name, mode.Type())
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// safeJoin resolves name against dir, rejecting absolute paths and traversal.
func safeJoin(dir, name string) (string, error) {
	if path.IsAbs(name) || filepath.IsAbs(name) || strings.HasPrefix(name, `\`) {
		return "", fmt.Errorf("%s: absolute path in archive", name)
	}
	target := filepath.Join(dir, filepath.FromSlash(name))
	if target != dir && !strings.HasPrefix(target, dir+string(filepath.Separator)) {
		return "", fmt.Errorf("%s: path escapes the destination directory", name)
	}
	return target, nil
}

func extractFile(f *zip.File, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	src, err := f.Open()
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return err
	}
	return dst.Close()
}

func extractSymlink(f *zip.File, dir, target string) error {
	src, err := f.Open()
	if err != nil {
		return err
	}
	defer src.Close()
	dest, err := io.ReadAll(io.LimitReader(src, 4096))
	if err != nil {
		return err
	}
	// A link is only safe if it still points inside dir once resolved.
	if path.IsAbs(string(dest)) || filepath.IsAbs(string(dest)) {
		return fmt.Errorf("%s: symlink %s: absolute target", f.Name, dest)
	}
	if _, err := safeJoin(dir, path.Join(path.Dir(f.Name), string(dest))); err != nil {
		return fmt.Errorf("%s: symlink %s: %w", f.Name, dest, err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return os.Symlink(string(dest), target)
}
