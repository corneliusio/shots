package command

import (
	"context"
	"strconv"
	"strings"

	"github.com/mostlyserious/shots/internal/artifact"
	"github.com/mostlyserious/shots/internal/browser"
)

type pdfCmd struct {
	URL string `arg:"" help:"URL, bare domain, or path to a local HTML file."`

	Output string `short:"o" placeholder:"FILE" help:"Write to this file, or - for standard output. Omitted means a name derived from the URL."`

	Format          string  `placeholder:"NAME" help:"Paper format: Letter, Legal, Tabloid, Ledger, or A0 to A6."`
	Width           string  `placeholder:"SIZE" help:"Paper width with units, such as 8.5in, 210mm or 800px."`
	Height          string  `placeholder:"SIZE" help:"Paper height with units."`
	Landscape       bool    `help:"Print in landscape orientation."`
	Scale           float64 `default:"1" placeholder:"N" help:"Scale of the rendered page, 0.1 to 2."`
	PrintBackground bool    `help:"Print background graphics."`
	MediaScreen     bool    `help:"Render with screen media instead of print media."`

	pageFlags
}

// paper holds the standard formats, in inches, as Playwright defines them.
var paper = map[string]browser.PDFOptions{
	"letter":  {PaperWidth: 8.5, PaperHeight: 11},
	"legal":   {PaperWidth: 8.5, PaperHeight: 14},
	"tabloid": {PaperWidth: 11, PaperHeight: 17},
	"ledger":  {PaperWidth: 17, PaperHeight: 11},
	"a0":      {PaperWidth: 33.1, PaperHeight: 46.8},
	"a1":      {PaperWidth: 23.4, PaperHeight: 33.1},
	"a2":      {PaperWidth: 16.54, PaperHeight: 23.4},
	"a3":      {PaperWidth: 11.7, PaperHeight: 16.54},
	"a4":      {PaperWidth: 8.27, PaperHeight: 11.7},
	"a5":      {PaperWidth: 5.83, PaperHeight: 8.27},
	"a6":      {PaperWidth: 4.13, PaperHeight: 5.83},
}

// inches converts the CSS units a paper dimension may be given in.
var inches = map[string]float64{"in": 1, "px": 1.0 / 96, "cm": 1 / 2.54, "mm": 1 / 25.4}

func (c *pdfCmd) Run(ctx context.Context, e *env) error {
	if err := c.validate(); err != nil {
		return err
	}
	pdf, err := c.paperSize()
	if err != nil {
		return err
	}
	pdf.Landscape = c.Landscape
	pdf.Scale = c.Scale
	pdf.PrintBackground = c.PrintBackground

	target, err := NormalizeURL(c.URL)
	if err != nil {
		return err
	}
	output, err := artifact.Path(c.Output, target, "pdf")
	if err != nil {
		return err
	}
	script, err := c.script(ctx, e)
	if err != nil {
		return err
	}

	media := ""
	if c.MediaScreen {
		media = "screen"
	}
	page, closer, err := c.open(ctx, e, target, script, browser.PageOptions{Media: media})
	if err != nil {
		return err
	}
	defer closer()

	stream, err := page.PDF(ctx, pdf)
	if err != nil {
		return err
	}
	defer stream.Close()
	return c.emit(e, "PDF", target, output, stream, artifact.Options{})
}

func (c *pdfCmd) validate() error {
	if err := c.pageFlags.validate(); err != nil {
		return err
	}
	switch {
	case c.Scale < 0.1 || c.Scale > 2:
		return usagef("--scale must be between 0.1 and 2")
	case c.Format != "" && (c.Width != "" || c.Height != ""):
		return usagef("--format cannot be combined with --width or --height")
	}
	return nil
}

// paperSize resolves --format, or --width and --height, to inches. Nothing set
// leaves Chrome's default, which is Letter.
func (c *pdfCmd) paperSize() (browser.PDFOptions, error) {
	var out browser.PDFOptions
	if c.Format != "" {
		size, ok := paper[strings.ToLower(c.Format)]
		if !ok {
			return out, usagef("--format must be one of Letter, Legal, Tabloid, Ledger, or A0 to A6")
		}
		return size, nil
	}
	var err error
	if out.PaperWidth, err = paperInches("--width", c.Width); err != nil {
		return out, err
	}
	out.PaperHeight, err = paperInches("--height", c.Height)
	return out, err
}

// paperInches parses a CSS length. A bare number is pixels, as in CSS.
func paperInches(flag, size string) (float64, error) {
	if size == "" {
		return 0, nil
	}
	number, scale := size, inches["px"]
	if len(size) > 2 {
		if unit, ok := inches[strings.ToLower(size[len(size)-2:])]; ok {
			number, scale = size[:len(size)-2], unit
		}
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(number), 64)
	if err != nil || value <= 0 {
		return 0, usagef("%s must be a positive length such as 8.5in, 210mm or 800px, not %q", flag, size)
	}
	return value * scale, nil
}
