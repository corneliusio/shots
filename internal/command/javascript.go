package command

import (
	"bytes"
	"context"
	"encoding/json"
	"io"

	"github.com/corneliusio/shots/internal/artifact"
	"github.com/corneliusio/shots/internal/browser"
)

// jsonIndent keeps JavaScript results readable.
const jsonIndent = "    "

type javascriptCmd struct {
	URL    string `arg:"" help:"URL, bare domain, or path to a local HTML file."`
	Script string `arg:"" optional:"" placeholder:"SCRIPT" help:"JavaScript to evaluate. Read from standard input when neither this nor --javascript or --js-file is given."`

	Output string `short:"o" default:"-" placeholder:"FILE" help:"Write to this file, or - for standard output."`
	Raw    bool   `short:"r" help:"Print a string result as text instead of as JSON."`

	Width  int `short:"w" default:"1280" help:"Viewport width."`
	Height int `short:"h" help:"Viewport height."`

	pageFlags
}

func (c *javascriptCmd) Run(ctx context.Context, e *env) error {
	if err := c.validate(); err != nil {
		return err
	}
	target, err := NormalizeURL(c.URL)
	if err != nil {
		return err
	}
	source, err := c.source(ctx, e)
	if err != nil {
		return err
	}

	// The script is run after the waits rather than as the page's before
	// script: its result is the artifact.
	page, closer, err := c.open(ctx, e, target, "", browser.PageOptions{Width: c.Width, Height: c.viewportHeight()})
	if err != nil {
		return err
	}
	defer closer()

	value, err := page.Eval(ctx, source)
	if err != nil {
		return err
	}
	out, err := format(value, c.Raw)
	if err != nil {
		return err
	}
	return c.emit(e, "Result", target, c.Output, bytes.NewReader(out), artifact.Options{})
}

func (c *javascriptCmd) validate() error {
	if err := c.pageFlags.validate(); err != nil {
		return err
	}
	switch {
	case c.Script != "" && (c.JavaScript != "" || c.JSFile != ""):
		return usagef("a script argument cannot be combined with --javascript or --js-file")
	case c.Width <= 0:
		return usagef("--width must be positive")
	case c.Height < 0:
		return usagef("--height must be positive")
	}
	return nil
}

// source is the JavaScript to evaluate: the argument, else --javascript or
// --js-file, else standard input.
func (c *javascriptCmd) source(ctx context.Context, e *env) (string, error) {
	if c.Script != "" {
		return c.Script, nil
	}
	source, err := c.script(ctx, e)
	if err != nil {
		return "", err
	}
	// Standard input is the last resort, and not available at all when the
	// Enter prompt is already reading it.
	if source == "" && !c.Interactive && !c.DevTools {
		data, err := io.ReadAll(e.stdin)
		if err != nil {
			return "", err
		}
		source = string(data)
	}
	if source == "" {
		return "", usagef("no JavaScript to run: pass a script argument, --javascript, --js-file, or standard input")
	}
	return source, nil
}

func (c *javascriptCmd) viewportHeight() int {
	if c.Height > 0 {
		return c.Height
	}
	return 720
}

// format renders a result. JSON is the default; --raw prints a string result as
// itself, which is what makes the output pipeable. Everything ends in a
// newline, so the artifact is a text file either way.
//
// The JSON is re-indented rather than re-encoded, so object keys keep the order
// the page produced them in.
func format(value json.RawMessage, raw bool) ([]byte, error) {
	if len(value) == 0 {
		value = json.RawMessage("null")
	}
	if raw {
		var text string
		if err := json.Unmarshal(value, &text); err == nil {
			return append([]byte(text), '\n'), nil
		}
	}
	var out bytes.Buffer
	if err := json.Indent(&out, value, "", jsonIndent); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}
