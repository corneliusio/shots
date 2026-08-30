package command

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/mostlyserious/shots/internal/artifact"
	"github.com/mostlyserious/shots/internal/browser"
)

// browserFlags are the browser-selection options shared by every command that
// drives a page.
//
// BrowserPath carries no env tag: $SHOTS_BROWSER_PATH is step 2 of
// [browser.Discover]'s order, where a path that no longer exists falls through
// to the next candidate instead of failing the run.
type browserFlags struct {
	BrowserPath   string   `placeholder:"PATH" help:"Chrome executable to use, ahead of $SHOTS_BROWSER_PATH and discovery."`
	CDPURL        string   `name:"cdp-url" placeholder:"URL" help:"Connect to a running browser instead of launching one."`
	BrowserArg    []string `sep:"none" placeholder:"ARG" help:"Extra Chrome command-line argument; repeatable."`
	UserAgent     string   `help:"Override the browser's user agent."`
	ReducedMotion bool     `help:"Emulate prefers-reduced-motion."`
	BypassCSP     bool     `name:"bypass-csp" help:"Ignore the page's Content-Security-Policy."`
	AuthUsername  string   `help:"Username for HTTP basic authentication."`
	AuthPassword  string   `help:"Password for HTTP basic authentication."`
}

// validate rejects launch options combined with --cdp-url: connecting to a
// browser somebody else started cannot change how it was started.
func (f browserFlags) validate() error {
	switch {
	case f.CDPURL == "":
		return nil
	case f.BrowserPath != "":
		return usagef("--cdp-url cannot be combined with --browser-path")
	case len(f.BrowserArg) > 0:
		return usagef("--cdp-url cannot be combined with --browser-arg")
	}
	return nil
}

// sessionFlags are the options that apply to a whole run rather than to one
// page: which browser to drive, which authentication state to restore, how long
// to wait, and what to report. Every browser command has them, including multi,
// where one set of them covers every entry.
type sessionFlags struct {
	Auth string `short:"a" placeholder:"FILE" help:"Restore cookies and local storage from this authentication state file."`

	Timeout int `help:"Navigation and script timeout in milliseconds."`

	Skip bool `help:"Exit successfully without output when the page returns 4xx or 5xx."`
	Fail bool `help:"Fail when the page returns 4xx or 5xx."`

	LogConsole bool `help:"Write console messages to standard error."`
	Silent     bool `help:"Do not report progress."`

	browserFlags
}

func (f sessionFlags) validate() error {
	if err := f.browserFlags.validate(); err != nil {
		return err
	}
	switch {
	case f.Skip && f.Fail:
		return usagef("--skip and --fail cannot be used together")
	case f.Timeout < 0:
		return usagef("--timeout must not be negative")
	}
	return nil
}

// pageFlags are the options a command that loads one page of its own accepts.
// The lifecycle they drive is [pageFlags.open].
type pageFlags struct {
	JavaScript string `short:"j" name:"javascript" placeholder:"CODE" help:"JavaScript to run on the page."`
	JSFile     string `name:"js-file" placeholder:"FILE" help:"Read the JavaScript from this file, - for standard input, or gh:user/script.js."`

	Wait    int    `help:"Milliseconds to wait after loading."`
	WaitFor string `placeholder:"EXPR" help:"Wait until this JavaScript expression is true."`

	Interactive bool `short:"i" help:"Show the browser and wait for Enter before producing output."`
	DevTools    bool `name:"devtools" help:"Show the browser with developer tools open, and wait for Enter."`

	sessionFlags
}

func (f pageFlags) validate() error {
	if err := f.sessionFlags.validate(); err != nil {
		return err
	}
	switch {
	case f.CDPURL != "" && f.Interactive:
		return usagef("--cdp-url cannot be combined with --interactive")
	case f.CDPURL != "" && f.DevTools:
		return usagef("--cdp-url cannot be combined with --devtools")
	case f.JavaScript != "" && f.JSFile != "":
		return usagef("--javascript and --js-file cannot be used together")
	case f.JSFile == "-" && (f.Interactive || f.DevTools):
		// The Enter prompt reads the same stream the script would.
		return usagef("--js-file - cannot be combined with --interactive or --devtools")
	case f.Wait < 0:
		return usagef("--wait must not be negative")
	}
	return nil
}

// browserOptions are the launch options a session shares. The caller adds
// whether the browser is headed, which is a per-command decision.
func (f sessionFlags) browserOptions() browser.Options {
	return browser.Options{
		Path:    f.BrowserPath,
		CDPURL:  f.CDPURL,
		Args:    f.BrowserArg,
		Timeout: time.Duration(f.Timeout) * time.Millisecond,
	}
}

// newPage opens a page carrying the options every page of a session shares. The
// caller fills the viewport and any logging it owns in opts.
func (f sessionFlags) newPage(ctx context.Context, e *env, bc *browser.Context, opts browser.PageOptions) (*browser.Page, error) {
	opts.UserAgent = f.UserAgent
	opts.ReducedMotion = f.ReducedMotion
	opts.BypassCSP = f.BypassCSP
	opts.Username, opts.Password = f.AuthUsername, f.AuthPassword
	if f.LogConsole {
		opts.OnConsole = func(m browser.Message) { fmt.Fprintf(e.stderr, "%s: %s\n", m.Type, m.Text) }
	}
	return bc.NewPage(ctx, opts)
}

// check applies the --skip and --fail policy to a navigation's status.
func (f sessionFlags) check(e *env, status int, target string) error {
	switch {
	case status < 400:
	case f.Skip:
		fmt.Fprintf(e.stderr, "%d error for %s, skipping\n", status, target)
		return errSkipped
	case f.Fail:
		return fmt.Errorf("%d error for %s", status, target)
	}
	return nil
}

// open runs steps 3 to 9 of the lifecycle in PLAN.md Section 6: connect to
// Chrome, open a page in a fresh browser context, navigate, apply --skip and
// --fail, then wait and run script.
//
// On failure — including the [errSkipped] that --skip reports — everything
// opened is closed again; on success the caller must call the returned
// function.
func (f pageFlags) open(ctx context.Context, e *env, target, script string, opts browser.PageOptions) (*browser.Page, func(), error) {
	state, err := loadState(f.Auth)
	if err != nil {
		return nil, nil, err
	}
	launch := f.browserOptions()
	launch.Headed, launch.DevTools = f.Interactive || f.DevTools, f.DevTools
	b, err := browser.Connect(ctx, launch)
	if err != nil {
		return nil, nil, err
	}
	bc, err := b.NewContext(ctx, state)
	if err != nil {
		b.Close()
		return nil, nil, err
	}
	page, err := f.newPage(ctx, e, bc, opts)
	if err != nil {
		bc.Close()
		b.Close()
		return nil, nil, err
	}
	closer := func() {
		page.Close()
		bc.Close()
		b.Close()
	}
	if err := f.load(ctx, e, page, target, script); err != nil {
		closer()
		return nil, nil, err
	}
	return page, closer, nil
}

func (f pageFlags) load(ctx context.Context, e *env, page *browser.Page, target, script string) error {
	status, err := page.Navigate(ctx, target)
	if err != nil {
		return err
	}
	if err := f.check(e, status, target); err != nil {
		return err
	}
	if f.Interactive || f.DevTools {
		fmt.Fprintln(e.stderr, "Hit <enter> to continue and close the browser window:")
		if err := waitForEnter(ctx, e.stdin); err != nil {
			return err
		}
	}
	return prepare(ctx, page, time.Duration(f.Wait)*time.Millisecond, script, f.WaitFor)
}

// prepare runs the steps between a navigation and the artifact: a fixed wait,
// the page's script, then a condition to wait for.
func prepare(ctx context.Context, page *browser.Page, wait time.Duration, script, waitFor string) error {
	if err := page.Wait(ctx, wait); err != nil {
		return err
	}
	if err := page.Exec(ctx, script); err != nil {
		return err
	}
	return page.WaitFor(ctx, waitFor)
}

// emit writes an artifact and, unless it went to standard output, reports where
// it went. --silent suppresses the report, never the artifact.
func (f sessionFlags) emit(e *env, kind, target, output string, r io.Reader, opts artifact.Options) error {
	opts.Stdout = e.stdout
	if err := artifact.WriteStream(output, r, opts); err != nil {
		return err
	}
	if output != artifact.Stdout {
		e.progress(f.Silent, "%s of %q written to %q", kind, target, output)
	}
	return nil
}

// script resolves the JavaScript to run on the page, from --javascript or from
// --js-file. The two are mutually exclusive, so at most one is read.
func (f pageFlags) script(ctx context.Context, e *env) (string, error) {
	if f.JSFile == "" {
		return f.JavaScript, nil
	}
	return readScript(ctx, e.stdin, f.JSFile)
}

// scriptsRepo is the repository gh:user/script.js is shorthand for.
const scriptsRepo = "shot-scraper-scripts"

// readScript reads JavaScript from a file, from standard input when ref is "-",
// or from GitHub for a gh: reference.
func readScript(ctx context.Context, stdin io.Reader, ref string) (string, error) {
	switch {
	case ref == "-":
		data, err := io.ReadAll(stdin)
		return string(data), err
	case strings.HasPrefix(ref, "gh:"):
		url, err := githubURL(ref)
		if err != nil {
			return "", err
		}
		return download(ctx, url)
	default:
		data, err := os.ReadFile(ref)
		return string(data), err
	}
}

// githubURL expands the gh: convention: gh:user/repo/path/to/script.js, or
// gh:user/script.js as shorthand for gh:user/shot-scraper-scripts/script.js.
// A missing .js suffix is added.
func githubURL(ref string) (string, error) {
	parts := strings.Split(strings.TrimPrefix(ref, "gh:"), "/")
	if len(parts) < 2 {
		return "", usagef("%s: expected gh:user/script.js or gh:user/repo/path/script.js", ref)
	}
	user, repo, path := parts[0], scriptsRepo, parts[1:]
	if len(path) > 1 {
		repo, path = path[0], path[1:]
	}
	name := strings.Join(path, "/")
	if user == "" || repo == "" || strings.Contains(name, "//") || strings.HasPrefix(name, "/") || name == "" {
		return "", usagef("%s: expected gh:user/script.js or gh:user/repo/path/script.js", ref)
	}
	if !strings.HasSuffix(name, ".js") {
		name += ".js"
	}
	return "https://raw.githubusercontent.com/" + user + "/" + repo + "/main/" + name, nil
}

// download fetches a remote script. Remote scripts are user-trusted code, as
// PLAN.md Section 11 says: nothing here sandboxes them.
func download(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: %s", url, resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("%s: %w", url, err)
	}
	return string(data), nil
}

// waitForEnter blocks until a newline arrives on r, or ctx is cancelled.
func waitForEnter(ctx context.Context, r io.Reader) error {
	done := make(chan error, 1)
	go func() {
		_, err := bufio.NewReader(r).ReadString('\n')
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil && err != io.EOF {
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
