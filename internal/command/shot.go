package command

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mostlyserious/shots/internal/artifact"
	"github.com/mostlyserious/shots/internal/browser"
	"github.com/mostlyserious/shots/internal/har"
)

// defaultWidth and defaultHeight are the viewport a shot gets when it names
// neither. They repeat --width's kong default, which has to be a literal.
const (
	defaultWidth       = 1280
	defaultHeight      = 720
	defaultJPEGQuality = 80
)

type shotCmd struct {
	URL string `arg:"" help:"URL, bare domain, or path to a local HTML file."`

	Output    string `short:"o" placeholder:"FILE" help:"Write to this file, or - for standard output."`
	NoClobber bool   `short:"n" help:"Fail instead of overwriting an existing output file."`
	Report    string `placeholder:"FILE" help:"Write a JSON capture report to this file, or - for standard output."`
	Repeat    bool   `help:"Capture once, then capture again for each line read from standard input."`
	Stable    bool   `help:"Reduce visual motion and wait for fonts, images, and two paint frames."`

	Width  int `short:"w" default:"1280" help:"Viewport width."`
	Height int `short:"h" help:"Viewport and shot height; omitted means capture the full page."`

	Selector      []string `short:"s" sep:"none" placeholder:"CSS" help:"Capture the first element matching this CSS selector; repeatable."`
	SelectorAll   []string `sep:"none" placeholder:"CSS" help:"Capture every element matching this CSS selector; repeatable."`
	JSSelector    []string `name:"js-selector" sep:"none" placeholder:"EXPR" help:"Capture the first element for which this JavaScript expression over 'el' is true; repeatable."`
	JSSelectorAll []string `name:"js-selector-all" sep:"none" placeholder:"EXPR" help:"Capture every element for which this JavaScript expression over 'el' is true; repeatable."`
	Padding       int      `short:"p" help:"Pixels of padding around the selected region."`

	Format         string `placeholder:"FORMAT" help:"Image format: jpeg or png. Defaults to jpeg; a .png output selects PNG."`
	Quality        int    `help:"JPEG quality, 1 to 100. Defaults to 80."`
	OmitBackground bool   `help:"Leave the page background transparent."`

	LogRequests string `placeholder:"FILE" help:"Write one JSON object per network request to this file, or - for standard output."`

	scaleFlags
	harFlags
	pageFlags
}

// scaleFlags are the device-scale options shot and multi share.
type scaleFlags struct {
	Retina      bool    `help:"Use a device scale factor of 2."`
	ScaleFactor float64 `placeholder:"N" help:"Device scale factor."`
}

func (f scaleFlags) validate() error {
	switch {
	case f.Retina && f.ScaleFactor != 0:
		return usagef("--retina and --scale-factor cannot be used together")
	case f.ScaleFactor < 0:
		return usagef("--scale-factor must be positive")
	}
	return nil
}

// factor is the device scale factor to use. Zero leaves Chrome's own.
func (f scaleFlags) factor() float64 {
	if f.Retina {
		return 2
	}
	return f.ScaleFactor
}

func (c *shotCmd) Run(ctx context.Context, e *env) error {
	if err := c.validate(); err != nil {
		return err
	}
	target, err := NormalizeURL(c.URL)
	if err != nil {
		return err
	}
	encoding, err := screenshotEncodingFor(c.Output, c.Format, c.Quality, c.OmitBackground)
	if err != nil {
		return err
	}
	output, err := artifact.Path(c.Output, target, encoding.extension)
	if err != nil {
		return err
	}
	if sameArtifactPath(c.Report, output) {
		return usagef("--report and --output must use different files")
	}
	diagnostics := newShotDiagnostics(c.Report != "")
	started := time.Now()
	script, err := c.script(ctx, e)
	if err != nil {
		return c.finishReport(e, diagnostics, target, "", output, 0, started, err)
	}

	harPath, harZip, err := c.harFlags.path()
	if err != nil {
		return c.finishReport(e, diagnostics, target, "", output, 0, started, err)
	}
	if sameArtifactPath(c.Report, harPath) {
		return usagef("--report and HAR output must use different files")
	}
	var recorder *har.Recorder
	if harPath != "" {
		recorder = har.NewRecorder(Version)
	}
	requests, closeRequests, err := c.requestLog(e)
	if err != nil {
		return c.finishReport(e, diagnostics, target, "", output, 0, started, err)
	}
	defer closeRequests()

	opts := browser.PageOptions{
		Width:       c.Width,
		Height:      c.viewportHeight(),
		ScaleFactor: c.factor(),
		OnRequest:   requests,
		HAR:         recorder,
	}
	diagnostics.attach(&opts)
	flags := c.pageFlags
	if c.Stable {
		flags.ReducedMotion = true
	}
	session, err := flags.connect(ctx, e, opts)
	if err != nil {
		return c.finishReport(e, diagnostics, target, "", output, 0, started, err)
	}
	defer session.close()

	capture := func() error {
		return c.capture(ctx, e, flags, session.page, diagnostics, target, output, script, recorder, harPath, harZip, encoding)
	}
	if !c.Repeat {
		return capture()
	}
	return repeatCaptures(ctx, e, session.browser.Lost(), capture)
}

func (c *shotCmd) capture(
	ctx context.Context,
	e *env,
	flags pageFlags,
	page *browser.Page,
	diagnostics *shotDiagnostics,
	target, output, script string,
	recorder *har.Recorder,
	harPath string,
	harZip bool,
	encoding screenshotEncoding,
) error {
	diagnostics.reset()
	started := time.Now()
	status, captureErr := flags.loadStatus(ctx, e, page, target, script)
	if captureErr == nil && c.Stable {
		captureErr = page.Stabilize(ctx)
	}

	shot := browser.ShotOptions{
		JPEG:           encoding.jpeg,
		Quality:        encoding.quality,
		OmitBackground: c.OmitBackground,
		FullPage:       c.Height == 0,
	}
	if captureErr == nil {
		selectors := c.selectors()
		if !selectors.Empty() {
			region, err := page.Region(ctx, selectors)
			if err != nil {
				captureErr = err
			} else {
				shot.Clip = &region
			}
		}
	}
	if captureErr == nil {
		data, err := page.Screenshot(ctx, shot)
		if err != nil {
			captureErr = err
		} else {
			captureErr = c.emit(e, "Screenshot", target, output, bytes.NewReader(data), artifact.Options{NoClobber: c.NoClobber})
		}
	}
	if captureErr == nil && recorder != nil {
		page.FlushHAR()
		captureErr = c.writeHAR(e, recorder, harPath, harZip)
	}
	finalURL, _ := page.URL(ctx)
	reportOutput := output
	if errors.Is(captureErr, errSkipped) {
		reportOutput = ""
	}
	return c.finishReport(e, diagnostics, target, finalURL, reportOutput, status, started, captureErr)
}

func (c *shotCmd) finishReport(
	e *env,
	diagnostics *shotDiagnostics,
	target, finalURL, output string,
	status int,
	started time.Time,
	captureErr error,
) error {
	reportErr := writeShotReport(e, c.Report, diagnostics.snapshot(target, finalURL, output, status, started, captureErr))
	if reportErr == nil {
		return captureErr
	}
	reportErr = fmt.Errorf("writing report: %w", reportErr)
	if captureErr == nil || errors.Is(captureErr, errSkipped) {
		return reportErr
	}
	return errors.Join(captureErr, reportErr)
}

func (c *shotCmd) validate() error {
	if err := c.pageFlags.validate(); err != nil {
		return err
	}
	if err := c.scaleFlags.validate(); err != nil {
		return err
	}
	switch {
	case c.Width <= 0:
		return usagef("--width must be positive")
	case c.Height < 0:
		return usagef("--height must be positive")
	case c.Quality != 0 && (c.Quality < 1 || c.Quality > 100):
		return usagef("--quality must be between 1 and 100")
	case c.Padding < 0:
		return usagef("--padding must not be negative")
	case c.Report == artifact.Stdout && c.Output == artifact.Stdout:
		return usagef("--report - cannot be combined with --output -")
	case c.Report == artifact.Stdout && c.LogRequests == artifact.Stdout:
		return usagef("--report - cannot be combined with --log-requests -")
	case c.Report == artifact.Stdout && c.HARFile == artifact.Stdout:
		return usagef("--report - cannot be combined with --har-file -")
	case sameArtifactPath(c.Report, c.Output):
		return usagef("--report and --output must use different files")
	case sameArtifactPath(c.Report, c.LogRequests):
		return usagef("--report and --log-requests must use different files")
	case sameArtifactPath(c.Report, c.HARFile):
		return usagef("--report and --har-file must use different files")
	case c.Repeat && c.Output == "":
		return usagef("--repeat requires an explicit --output file")
	case c.Repeat && c.Output == artifact.Stdout:
		return usagef("--repeat cannot be combined with --output -")
	case c.Repeat && c.NoClobber:
		return usagef("--repeat cannot be combined with --no-clobber")
	case c.Repeat && c.JSFile == artifact.Stdout:
		return usagef("--repeat cannot be combined with --js-file -")
	case c.Repeat && c.Interactive:
		return usagef("--repeat cannot be combined with --interactive")
	case c.Repeat && c.DevTools:
		return usagef("--repeat cannot be combined with --devtools")
	case c.Repeat && (c.HAR || c.HARZip || c.HARFile != ""):
		return usagef("--repeat cannot be combined with HAR recording")
	}
	_, err := screenshotEncodingFor(c.Output, c.Format, c.Quality, c.OmitBackground)
	return err
}

func sameArtifactPath(a, b string) bool {
	return a != "" && b != "" && a != artifact.Stdout && b != artifact.Stdout && filepath.Clean(a) == filepath.Clean(b)
}

// repeatCaptures runs capture once, then again for every complete line on
// stdin. A failed capture is reported on stderr and the loop goes on, so one
// broken edit in an iteration loop does not end the session; EOF then ends it
// with the failure count. Context cancellation interrupts a blocked stdin read,
// and so does losing the browser, which no later line could recover from.
func repeatCaptures(ctx context.Context, e *env, lost <-chan struct{}, capture func() error) error {
	events := make(chan error)
	go func() {
		defer close(events)
		scanner := bufio.NewScanner(e.stdin)
		for scanner.Scan() {
			select {
			case events <- nil:
			case <-ctx.Done():
				return
			}
		}
		if err := scanner.Err(); err != nil {
			select {
			case events <- err:
			case <-ctx.Done():
			}
		}
	}()
	captures, failed := 0, 0
	attempt := func() error {
		captures++
		err := capture()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil && !errors.Is(err, errSkipped) {
			failed++
			fmt.Fprintln(e.stderr, "shots:", err)
		}
		return nil
	}
	if err := attempt(); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-lost:
			return errBrowserExited
		case err, ok := <-events:
			switch {
			case !ok && failed > 0:
				return fmt.Errorf("%d of %d captures failed", failed, captures)
			case !ok:
				return nil
			case err != nil:
				return err
			}
			if err := attempt(); err != nil {
				return err
			}
		}
	}
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

// viewportHeight is the height the viewport is set to. Upstream always sets a
// viewport, defaulting the height to 720 when only a width is given, and
// captures the full page when --height is absent.
func (c *shotCmd) viewportHeight() int {
	if c.Height > 0 {
		return c.Height
	}
	return defaultHeight
}

// requestLog opens the --log-requests destination and returns a callback that
// writes one JSON object per completed request, plus its closer.
//
// The callback runs on a browser event goroutine, which can still deliver an
// event while the page is closing. Writing straight to the destination keeps
// that safe: an [encoding/json.Encoder] emits each record in a single Write,
// e.stdout serializes those against the main goroutine, and an *os.File tosses
// out a write that arrives after Close instead of racing it.
func (c *shotCmd) requestLog(e *env) (func(browser.Request), func(), error) {
	if c.LogRequests == "" {
		return nil, func() {}, nil
	}
	w, closer := e.stdout, func() {}
	if c.LogRequests != artifact.Stdout {
		f, err := os.Create(c.LogRequests)
		if err != nil {
			return nil, nil, err
		}
		w, closer = f, func() { f.Close() }
	}
	enc := json.NewEncoder(w)
	return func(r browser.Request) { enc.Encode(r) }, closer, nil
}

type screenshotEncoding struct {
	jpeg      bool
	quality   int
	extension string
}

func screenshotEncodingFor(output, requested string, quality int, omitBackground bool) (screenshotEncoding, error) {
	requested = strings.ToLower(requested)
	if requested == "jpg" {
		requested = "jpeg"
	}
	if requested != "" && requested != "jpeg" && requested != "png" {
		return screenshotEncoding{}, usagef("--format must be jpeg or png")
	}

	fromPath := ""
	switch strings.ToLower(filepath.Ext(output)) {
	case ".jpg", ".jpeg":
		fromPath = "jpeg"
	case ".png":
		fromPath = "png"
	}
	if requested != "" && fromPath != "" && requested != fromPath {
		return screenshotEncoding{}, usagef("--format %s conflicts with output extension %s", requested, filepath.Ext(output))
	}
	format := requested
	if format == "" {
		format = fromPath
	}
	if format == "" && omitBackground {
		format = "png"
	}
	if format == "" {
		format = "jpeg"
	}
	if format == "png" && quality > 0 {
		return screenshotEncoding{}, usagef("--quality requires JPEG output")
	}
	if format == "jpeg" && omitBackground {
		return screenshotEncoding{}, usagef("--omit-background requires PNG output")
	}
	if format == "png" {
		return screenshotEncoding{extension: "png"}, nil
	}
	if quality == 0 {
		quality = defaultJPEGQuality
	}
	return screenshotEncoding{jpeg: true, quality: quality, extension: "jpg"}, nil
}
