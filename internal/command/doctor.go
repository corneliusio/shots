package command

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/mostlyserious/shots/internal/browser"
)

type doctorCmd struct {
	BrowserPath string `placeholder:"PATH" help:"Chrome executable to check, ahead of $SHOTS_BROWSER_PATH and discovery."`
	CDPURL      string `name:"cdp-url" placeholder:"URL" help:"Check this running browser instead of launching one."`
}

// report accumulates doctor's findings and remembers whether any of them is a
// problem, which decides the exit status.
type checks struct {
	w        io.Writer
	problems int
}

func (r *checks) line(name, format string, args ...any) {
	fmt.Fprintf(r.w, "%-12s %s\n", name, fmt.Sprintf(format, args...))
}

func (r *checks) problem(name string, err error) {
	r.problems++
	r.line(name, "PROBLEM: %s", err)
}

func (c *doctorCmd) Run(ctx context.Context, e *env) error {
	if c.CDPURL != "" && c.BrowserPath != "" {
		return usagef("--cdp-url cannot be combined with --browser-path")
	}
	r := &checks{w: e.stdout}
	r.line("shots", "%s", Version)
	r.line("platform", "%s/%s", runtime.GOOS, runtime.GOARCH)
	r.line("pinned", "Chrome %s for %s", browser.Pinned().Version, orNone(browser.Platform(), "unsupported platform"))

	c.checkBrowser(ctx, r)
	c.checkCache(r)
	checkTool(r, "ffmpeg", "ffmpeg is only needed for the video command", "ffmpeg")
	checkTool(r, "python", "Python is only needed for python: steps", pythonNames()...)

	if r.problems > 0 {
		return fmt.Errorf("%d problem(s) found", r.problems)
	}
	return nil
}

func (c *doctorCmd) checkBrowser(ctx context.Context, r *checks) {
	if c.CDPURL == "" {
		path, err := browser.Discover(c.BrowserPath)
		if err != nil {
			r.problem("browser", err)
			return
		}
		r.line("browser", "%s", path)
		if missing := browser.MissingLibraries(ctx, path); len(missing) > 0 {
			r.problem("libraries", fmt.Errorf("%s needs shared libraries this machine does not have: %s", path, strings.Join(missing, ", ")))
		}
	} else {
		r.line("browser", "%s (--cdp-url)", c.CDPURL)
	}
	b, err := browser.Connect(ctx, browser.Options{Path: c.BrowserPath, CDPURL: c.CDPURL})
	if err != nil {
		r.problem("cdp", err)
		return
	}
	defer b.Close()
	version, err := b.Version(ctx)
	if err != nil {
		r.problem("cdp", err)
		return
	}
	r.line("cdp", "ok, %s", version)
}

func (c *doctorCmd) checkCache(r *checks) {
	dir, err := browser.CacheDir()
	if err != nil {
		r.problem("cache", err)
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		r.problem("cache", err)
		return
	}
	f, err := os.CreateTemp(dir, ".shots-doctor-*")
	if err != nil {
		r.problem("cache", fmt.Errorf("%s: not writable: %w", dir, err))
		return
	}
	f.Close()
	os.Remove(f.Name())
	r.line("cache", "%s (writable)", dir)
}

// checkTool reports the first of names found on PATH. A missing optional tool
// is reported with the note explaining what it would be needed for, and is not
// counted as a problem.
func checkTool(r *checks, label, note string, names ...string) {
	for _, name := range names {
		if path, err := exec.LookPath(name); err == nil {
			r.line(label, "%s", path)
			return
		}
	}
	r.line(label, "not found (%s)", note)
}

// pythonNames is the resolution order from PLAN.md Section 9.
func pythonNames() []string {
	if custom := os.Getenv("SHOTS_PYTHON"); custom != "" {
		return []string{custom}
	}
	return []string{"python3", "python"}
}

func orNone(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
