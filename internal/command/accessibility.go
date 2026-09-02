package command

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/corneliusio/shots/internal/artifact"
	"github.com/corneliusio/shots/internal/browser"
)

type accessibilityCmd struct {
	URL string `arg:"" help:"URL, bare domain, or path to a local HTML file."`

	Output string `short:"o" default:"-" placeholder:"FILE" help:"Write to this file, or - for standard output."`

	pageFlags
}

func (c *accessibilityCmd) Run(ctx context.Context, e *env) error {
	if err := c.pageFlags.validate(); err != nil {
		return err
	}
	target, err := NormalizeURL(c.URL)
	if err != nil {
		return err
	}
	script, err := c.script(ctx, e)
	if err != nil {
		return err
	}
	page, closer, err := c.open(ctx, e, target, script, browser.PageOptions{})
	if err != nil {
		return err
	}
	defer closer()

	tree, err := page.Accessibility(ctx)
	if err != nil {
		return err
	}
	out, err := json.MarshalIndent(tree, "", jsonIndent)
	if err != nil {
		return err
	}
	return c.emit(e, "Accessibility tree", target, c.Output, bytes.NewReader(append(out, '\n')), artifact.Options{})
}
