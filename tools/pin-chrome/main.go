// Command pin-chrome rewrites internal/browser/chrome.json for a Chrome for
// Testing Headless Shell version.
//
// Chrome for Testing publishes download URLs but no checksums, so this command
// computes them for the committed manifest. This is a maintainer chore:
//
//	go run ./tools/pin-chrome            # pin the current Stable release
//	go run ./tools/pin-chrome 152.0.7977.64
//
// Every platform archive is downloaded, hashed, and checked for the executable
// the manifest claims it contains. Platforms whose download fails are reported
// and left out of the manifest, so a partial pin is still usable.
package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"slices"

	"github.com/corneliusio/shots/internal/browser"
)

// binaries is the browser executable inside each platform's archive.
var binaries = map[string]string{
	"linux64":   "chrome-headless-shell-linux64/chrome-headless-shell",
	"mac-arm64": "chrome-headless-shell-mac-arm64/chrome-headless-shell",
	"mac-x64":   "chrome-headless-shell-mac-x64/chrome-headless-shell",
	"win32":     "chrome-headless-shell-win32/chrome-headless-shell.exe",
	"win64":     "chrome-headless-shell-win64/chrome-headless-shell.exe",
}

const (
	stableURL   = "https://googlechromelabs.github.io/chrome-for-testing/last-known-good-versions-with-downloads.json"
	downloadURL = "https://storage.googleapis.com/chrome-for-testing-public/%s/%s/chrome-headless-shell-%s.zip"
	manifest    = "internal/browser/chrome.json"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "pin-chrome:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) > 1 {
		return fmt.Errorf("usage: pin-chrome [VERSION]")
	}
	version := ""
	if len(args) == 1 {
		version = args[0]
	} else {
		var err error
		if version, err = stableVersion(); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "current Stable is %s\n", version)
	}

	pinned := browser.Manifest{Version: version, Platforms: map[string]browser.Build{}}
	var failed []string
	for _, platform := range slices.Sorted(maps.Keys(binaries)) {
		url := fmt.Sprintf(downloadURL, version, platform, platform)
		sum, err := hashArchive(url, binaries[platform])
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", platform, err)
			failed = append(failed, platform)
			continue
		}
		fmt.Fprintf(os.Stderr, "%s: %s\n", platform, sum)
		pinned.Platforms[platform] = browser.Build{URL: url, SHA256: sum, Binary: binaries[platform]}
	}
	if len(pinned.Platforms) == 0 {
		return fmt.Errorf("no platform archives could be downloaded")
	}
	if len(failed) > 0 {
		fmt.Fprintf(os.Stderr, "warning: no checksum for %v; those platforms cannot be installed\n", failed)
	}

	data, err := json.MarshalIndent(pinned, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(manifest, append(data, '\n'), 0o644)
}

func stableVersion() (string, error) {
	resp, err := http.Get(stableURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: %s", stableURL, resp.Status)
	}
	var known struct {
		Channels map[string]struct {
			Version string `json:"version"`
		} `json:"channels"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&known); err != nil {
		return "", err
	}
	if v := known.Channels["Stable"].Version; v != "" {
		return v, nil
	}
	return "", fmt.Errorf("%s: no Stable channel version", stableURL)
}

// hashArchive downloads url, returns its SHA-256, and fails if binary is not
// one of its entries.
func hashArchive(url, binary string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s", resp.Status)
	}
	f, err := os.CreateTemp("", "pin-chrome-*.zip")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	sum := sha256.New()
	size, err := io.Copy(io.MultiWriter(f, sum), resp.Body)
	if err != nil {
		return "", err
	}
	zr, err := zip.NewReader(f, size)
	if err != nil {
		return "", err
	}
	for _, entry := range zr.File {
		if entry.Name == binary {
			return hex.EncodeToString(sum.Sum(nil)), nil
		}
	}
	return "", fmt.Errorf("archive does not contain %q", binary)
}
