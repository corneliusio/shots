package browser

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// NotFoundError reports that no Chrome executable could be found, and lists
// every location that was tried.
type NotFoundError struct {
	Checked []string
	Headed  bool
}

func (e *NotFoundError) Error() string {
	if e.Headed {
		return "no headed Chrome or Chromium found; shots install provides Chrome Headless Shell only; checked:\n  " + strings.Join(e.Checked, "\n  ")
	}
	return "no Chrome or Chromium found; checked:\n  " + strings.Join(e.Checked, "\n  ")
}

// Discover resolves the Chrome executable to use, in this order: the given
// path, $SHOTS_BROWSER_PATH, the browser installed by "shots install",
// Chrome Headless Shell, Chrome or Chromium on $PATH, then the well-known
// locations for this platform. It returns a [*NotFoundError] listing
// everything it tried when none of them exists.
func Discover(path string) (string, error) {
	return discover(path, false)
}

// discover skips the installed headless-only browser when headed is true.
// Explicit paths and SHOTS_BROWSER_PATH still win because the user chose them.
func discover(path string, headed bool) (string, error) {
	var checked []string
	try := func(candidate, why string) (string, bool) {
		if candidate == "" {
			return "", false
		}
		checked = append(checked, fmt.Sprintf("%s (%s)", candidate, why))
		if executable(candidate) {
			return candidate, true
		}
		return "", false
	}

	if path != "" {
		// An explicit path is a request, not a guess: report why it failed
		// rather than silently falling through to a different browser.
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("--browser-path: %w", err)
		}
		if !executable(path) {
			return "", fmt.Errorf("--browser-path: %s: not executable", path)
		}
		return path, nil
	}
	if found, ok := try(os.Getenv("SHOTS_BROWSER_PATH"), "SHOTS_BROWSER_PATH"); ok {
		return found, nil
	}
	if !headed {
		if installed, err := InstalledPath(); err == nil {
			if found, ok := try(installed, "shots install"); ok {
				return found, nil
			}
		}
	}
	for _, name := range pathNames(headed) {
		checked = append(checked, name+" (on PATH)")
		if found, err := exec.LookPath(name); err == nil {
			return found, nil
		}
	}
	for _, candidate := range knownLocations() {
		if found, ok := try(candidate, "known location"); ok {
			return found, nil
		}
	}
	return "", &NotFoundError{Checked: checked, Headed: headed}
}

// MissingLibraries lists the shared libraries the browser at path needs but
// that this machine does not have. Chrome for Testing archives carry no system
// dependencies, so a bare Linux box can hold an installed browser that cannot
// start; naming the libraries is more useful than the CDP timeout that follows.
// shots never installs them (PLAN.md Section 11).
//
// It reports nothing on other platforms, or where ldd is unavailable.
func MissingLibraries(ctx context.Context, path string) []string {
	if runtime.GOOS != "linux" {
		return nil
	}
	// ldd exits non-zero for a file it cannot inspect, but reports unresolved
	// libraries on stdout with status 0, so output decides, not the status.
	out, _ := exec.CommandContext(ctx, "ldd", path).Output()
	return unresolved(string(out))
}

// unresolved reads ldd output, whose missing entries read "libgbm.so.1 => not
// found".
func unresolved(ldd string) []string {
	var missing []string
	for line := range strings.Lines(ldd) {
		if name, rest, ok := strings.Cut(line, "=>"); ok && strings.TrimSpace(rest) == "not found" {
			missing = append(missing, strings.TrimSpace(name))
		}
	}
	return missing
}

// executable reports whether path is a regular file this process may execute.
func executable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return info.Mode().Perm()&0o111 != 0
}

// pathNames are the executable names looked up on $PATH. Chrome Headless Shell
// is preferred for headless work and excluded when a visible window is needed.
func pathNames(headed bool) []string {
	if headed {
		switch runtime.GOOS {
		case "windows":
			return []string{"chrome.exe", "chromium.exe"}
		case "darwin":
			return []string{"chrome", "chromium"}
		default:
			return []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome"}
		}
	}
	switch runtime.GOOS {
	case "windows":
		return []string{"chrome-headless-shell.exe", "headless_shell.exe", "chrome.exe", "chromium.exe"}
	case "darwin":
		return []string{"chrome-headless-shell", "headless_shell", "chrome", "chromium"}
	default:
		return []string{"chrome-headless-shell", "headless_shell", "google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome"}
	}
}

// knownLocations are the per-platform install locations, most preferred first.
func knownLocations() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing",
			filepath.Join(os.Getenv("HOME"), "Applications/Google Chrome.app/Contents/MacOS/Google Chrome"),
		}
	case "windows":
		var paths []string
		for _, root := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LocalAppData")} {
			if root != "" {
				paths = append(paths, filepath.Join(root, `Google\Chrome\Application\chrome.exe`))
			}
		}
		return paths
	default:
		return []string{
			"/opt/google/chrome/chrome",
			"/usr/bin/google-chrome",
			"/usr/bin/chromium",
			"/usr/bin/chromium-browser",
			"/snap/bin/chromium",
		}
	}
}

// CacheDir is the directory shots keeps downloaded browsers in.
func CacheDir() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "shots"), nil
}

// installDir is the directory "shots install" extracts the pinned browser into.
func installDir() (string, error) {
	cache, err := CacheDir()
	if err != nil {
		return "", err
	}
	build, err := pinnedBuild()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, "chrome-headless-shell", pinned.Version, build.platform), nil
}

// InstalledPath is where "shots install" puts the pinned browser's executable
// on this platform. It reports an error only when neither the cache directory
// nor the platform can be determined; the path itself may not exist yet.
func InstalledPath() (string, error) {
	dir, err := installDir()
	if err != nil {
		return "", err
	}
	build, err := pinnedBuild()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, filepath.FromSlash(build.Binary)), nil
}

// missing reports whether err means "this file does not exist".
func missing(err error) bool { return errors.Is(err, fs.ErrNotExist) }
