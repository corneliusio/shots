package command

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mostlyserious/shots/internal/artifact"
	"github.com/mostlyserious/shots/internal/browser"
)

type shotCmd struct {
	URL string `arg:"" help:"URL, bare domain, or path to a local HTML file."`

	Output    string `short:"o" placeholder:"FILE" help:"Write to this file, or - for standard output."`
	NoClobber bool   `short:"n" help:"Fail instead of overwriting an existing output file."`
	Silent    bool   `help:"Do not report progress."`

	Width  int `short:"w" default:"1280" help:"Viewport width."`
	Height int `short:"h" help:"Viewport and shot height; omitted means capture the full page."`

	Selector      []string `short:"s" sep:"none" placeholder:"CSS" help:"Capture the first element matching this CSS selector; repeatable."`
	SelectorAll   []string `sep:"none" placeholder:"CSS" help:"Capture every element matching this CSS selector; repeatable."`
	JSSelector    []string `name:"js-selector" sep:"none" placeholder:"EXPR" help:"Capture the first element for which this JavaScript expression over 'el' is true; repeatable."`
	JSSelectorAll []string `name:"js-selector-all" sep:"none" placeholder:"EXPR" help:"Capture every element for which this JavaScript expression over 'el' is true; repeatable."`
	Padding       int      `short:"p" help:"Pixels of padding around the selected region."`

	JavaScript string `short:"j" name:"javascript" placeholder:"CODE" help:"JavaScript to run before capturing."`

	Retina         bool    `help:"Use a device scale factor of 2."`
	ScaleFactor    float64 `placeholder:"N" help:"Device scale factor."`
	Quality        int     `help:"JPEG quality, 1 to 100. Implies JPEG output."`
	OmitBackground bool    `help:"Leave the page background transparent."`

	Wait    int    `help:"Milliseconds to wait after loading."`
	WaitFor string `placeholder:"EXPR" help:"Wait until this JavaScript expression is true."`
	Timeout int    `help:"Navigation and script timeout in milliseconds."`

	Skip bool `help:"Exit successfully without a screenshot when the page returns 4xx or 5xx."`
	Fail bool `help:"Fail when the page returns 4xx or 5xx."`

	Interactive bool   `short:"i" help:"Show the browser and wait for Enter before capturing."`
	DevTools    bool   `name:"devtools" help:"Show the browser with developer tools open, and wait for Enter."`
	LogConsole  bool   `help:"Write console messages to standard error."`
	LogRequests string `placeholder:"FILE" help:"Write one JSON object per network request to this file, or - for standard output."`

	browserFlags
}

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
// browser somebody else started cannot change how it was started. Commands with
// launch flags of their own check those too.
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

func (c *shotCmd) Run(ctx context.Context, e *env) error {
	if err := c.validate(); err != nil {
		return err
	}
	target, err := NormalizeURL(c.URL)
	if err != nil {
		return err
	}
	jpeg := c.Quality > 0 || isJPEGPath(c.Output)
	ext := "png"
	if jpeg {
		ext = "jpg"
	}
	output, err := artifact.Path(c.Output, target, ext)
	if err != nil {
		return err
	}

	requests, closeRequests, err := c.requestLog(e)
	if err != nil {
		return err
	}
	defer closeRequests()

	b, err := browser.Connect(ctx, browser.Options{
		Path:     c.BrowserPath,
		CDPURL:   c.CDPURL,
		Args:     c.BrowserArg,
		Headed:   c.Interactive || c.DevTools,
		DevTools: c.DevTools,
		Timeout:  time.Duration(c.Timeout) * time.Millisecond,
	})
	if err != nil {
		return err
	}
	defer b.Close()

	page, err := b.NewPage(ctx, browser.PageOptions{
		Width:         c.Width,
		Height:        c.viewportHeight(),
		ScaleFactor:   c.scaleFactor(),
		UserAgent:     c.UserAgent,
		ReducedMotion: c.ReducedMotion,
		BypassCSP:     c.BypassCSP,
		Username:      c.AuthUsername,
		Password:      c.AuthPassword,
		OnConsole:     c.consoleLog(e),
		OnRequest:     requests,
	})
	if err != nil {
		return err
	}
	defer page.Close()

	status, err := page.Navigate(ctx, target)
	if err != nil {
		return err
	}
	switch {
	case status < 400:
	case c.Skip:
		fmt.Fprintf(e.stderr, "%d error for %s, skipping\n", status, target)
		return errSkipped
	case c.Fail:
		return fmt.Errorf("%d error for %s", status, target)
	}

	if c.Interactive || c.DevTools {
		fmt.Fprintln(e.stderr, "Hit <enter> to take the shot and close the browser window:")
		if err := waitForEnter(ctx, e.stdin); err != nil {
			return err
		}
	}
	if err := page.Wait(ctx, time.Duration(c.Wait)*time.Millisecond); err != nil {
		return err
	}
	if err := page.Exec(ctx, c.JavaScript); err != nil {
		return err
	}
	if err := page.WaitFor(ctx, c.WaitFor); err != nil {
		return err
	}

	shot := browser.ShotOptions{
		JPEG:           jpeg,
		Quality:        c.Quality,
		OmitBackground: c.OmitBackground,
		FullPage:       c.Height == 0,
	}
	selectors := c.selectors()
	if !selectors.Empty() {
		region, err := page.Region(ctx, selectors)
		if err != nil {
			return err
		}
		shot.Clip = &region
	}
	data, err := page.Screenshot(ctx, shot)
	if err != nil {
		return err
	}
	if err := artifact.Write(output, data, artifact.Options{NoClobber: c.NoClobber, Stdout: e.stdout}); err != nil {
		return err
	}
	if output != artifact.Stdout {
		e.progress(c.Silent, "Screenshot of %q written to %q", target, output)
	}
	return nil
}

func (c *shotCmd) validate() error {
	if err := c.browserFlags.validate(); err != nil {
		return err
	}
	switch {
	case c.CDPURL != "" && c.Interactive:
		return usagef("--cdp-url cannot be combined with --interactive")
	case c.CDPURL != "" && c.DevTools:
		return usagef("--cdp-url cannot be combined with --devtools")
	case c.Skip && c.Fail:
		return usagef("--skip and --fail cannot be used together")
	case c.Retina && c.ScaleFactor != 0:
		return usagef("--retina and --scale-factor cannot be used together")
	case c.ScaleFactor < 0:
		return usagef("--scale-factor must be positive")
	case c.Width <= 0:
		return usagef("--width must be positive")
	case c.Height < 0:
		return usagef("--height must be positive")
	case c.Quality != 0 && (c.Quality < 1 || c.Quality > 100):
		return usagef("--quality must be between 1 and 100")
	case c.Wait < 0:
		return usagef("--wait must not be negative")
	case c.Padding < 0:
		return usagef("--padding must not be negative")
	case c.Timeout < 0:
		return usagef("--timeout must not be negative")
	}
	return nil
}

func (c *shotCmd) selectors() browser.Selectors {
	return browser.Selectors{
		CSS:     c.Selector,
		CSSAll:  c.SelectorAll,
		JS:      c.JSSelector,
		JSAll:   c.JSSelectorAll,
		Padding: c.Padding,
	}
}

func (c *shotCmd) scaleFactor() float64 {
	if c.Retina {
		return 2
	}
	return c.ScaleFactor
}

// viewportHeight is the height the viewport is set to. Upstream always sets a
// viewport, defaulting the height to 720 when only a width is given, and
// captures the full page when --height is absent.
func (c *shotCmd) viewportHeight() int {
	if c.Height > 0 {
		return c.Height
	}
	return 720
}

func (c *shotCmd) consoleLog(e *env) func(browser.Message) {
	if !c.LogConsole {
		return nil
	}
	return func(m browser.Message) {
		fmt.Fprintf(e.stderr, "%s: %s\n", m.Type, m.Text)
	}
}

// requestLog opens the --log-requests destination and returns a callback that
// writes one JSON object per completed request, plus its closer.
func (c *shotCmd) requestLog(e *env) (func(browser.Request), func(), error) {
	if c.LogRequests == "" {
		return nil, func() {}, nil
	}
	var w io.Writer = e.stdout
	closer := func() {}
	if c.LogRequests != artifact.Stdout {
		f, err := os.Create(c.LogRequests)
		if err != nil {
			return nil, nil, err
		}
		w, closer = f, func() { f.Close() }
	}
	buffered := bufio.NewWriter(w)
	enc := json.NewEncoder(buffered)
	// The callback runs on a CDP event goroutine, which can still deliver an
	// event while the page is closing, so writes are serialized and stop once
	// the log is closed.
	var mu sync.Mutex
	closed := false
	return func(r browser.Request) {
			mu.Lock()
			defer mu.Unlock()
			if !closed {
				enc.Encode(r)
			}
		}, func() {
			mu.Lock()
			defer mu.Unlock()
			closed = true
			buffered.Flush()
			closer()
		}, nil
}

func isJPEGPath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		return true
	}
	return false
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
