package command

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/mostlyserious/shots/internal/artifact"
	"github.com/mostlyserious/shots/internal/browser"
	"github.com/mostlyserious/shots/internal/har"
)

type shotCmd struct {
	URL string `arg:"" help:"URL, bare domain, or path to a local HTML file."`

	Output    string `short:"o" placeholder:"FILE" help:"Write to this file, or - for standard output."`
	NoClobber bool   `short:"n" help:"Fail instead of overwriting an existing output file."`

	Width  int `short:"w" default:"1280" help:"Viewport width."`
	Height int `short:"h" help:"Viewport and shot height; omitted means capture the full page."`

	Selector      []string `short:"s" sep:"none" placeholder:"CSS" help:"Capture the first element matching this CSS selector; repeatable."`
	SelectorAll   []string `sep:"none" placeholder:"CSS" help:"Capture every element matching this CSS selector; repeatable."`
	JSSelector    []string `name:"js-selector" sep:"none" placeholder:"EXPR" help:"Capture the first element for which this JavaScript expression over 'el' is true; repeatable."`
	JSSelectorAll []string `name:"js-selector-all" sep:"none" placeholder:"EXPR" help:"Capture every element for which this JavaScript expression over 'el' is true; repeatable."`
	Padding       int      `short:"p" help:"Pixels of padding around the selected region."`

	Retina         bool    `help:"Use a device scale factor of 2."`
	ScaleFactor    float64 `placeholder:"N" help:"Device scale factor."`
	Quality        int     `help:"JPEG quality, 1 to 100. Implies JPEG output."`
	OmitBackground bool    `help:"Leave the page background transparent."`

	LogRequests string `placeholder:"FILE" help:"Write one JSON object per network request to this file, or - for standard output."`

	harFlags
	pageFlags
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
	script, err := c.script(ctx, e)
	if err != nil {
		return err
	}

	requests, closeRequests, err := c.requestLog(e)
	if err != nil {
		return err
	}
	defer closeRequests()

	harPath, harZip, err := c.harFlags.path()
	if err != nil {
		return err
	}
	var recorder *har.Recorder
	if harPath != "" {
		recorder = har.NewRecorder(Version)
	}

	page, closer, err := c.open(ctx, e, target, script, browser.PageOptions{
		Width:       c.Width,
		Height:      c.viewportHeight(),
		ScaleFactor: c.scaleFactor(),
		OnRequest:   requests,
		HAR:         recorder,
	})
	if err != nil {
		return err
	}
	defer closer()

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
	if err := c.emit(e, "Screenshot", target, output, bytes.NewReader(data), artifact.Options{NoClobber: c.NoClobber}); err != nil {
		return err
	}
	if recorder == nil {
		return nil
	}
	return c.writeHAR(e, page, recorder, harPath, harZip)
}

func (c *shotCmd) validate() error {
	if err := c.pageFlags.validate(); err != nil {
		return err
	}
	switch {
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
	case c.Padding < 0:
		return usagef("--padding must not be negative")
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

func isJPEGPath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		return true
	}
	return false
}
