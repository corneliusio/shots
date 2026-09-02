package command

import (
	"context"
	"strings"

	"github.com/corneliusio/shots/internal/artifact"
	"github.com/corneliusio/shots/internal/browser"
)

type htmlCmd struct {
	URL string `arg:"" help:"URL, bare domain, or path to a local HTML file."`

	Output   string `short:"o" default:"-" placeholder:"FILE" help:"Write to this file, or - for standard output."`
	Selector string `short:"s" placeholder:"CSS" help:"Output the outer HTML of the first element matching this CSS selector."`

	pageFlags
}

func (c *htmlCmd) Run(ctx context.Context, e *env) error {
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

	html, err := page.HTML(ctx, c.Selector)
	if err != nil {
		return err
	}
	return c.emit(e, "HTML", target, c.Output, strings.NewReader(html+"\n"), artifact.Options{})
}
