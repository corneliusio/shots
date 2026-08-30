package command

import (
	"context"
	"io"

	"github.com/mostlyserious/shots/internal/browser"
)

type installCmd struct {
	Force  bool `help:"Download again even when the pinned build is already installed."`
	Silent bool `help:"Do not report progress."`
}

func (c *installCmd) Run(ctx context.Context, e *env) error {
	var progress io.Writer = e.stderr
	if c.Silent {
		progress = nil
	}
	_, err := browser.Install(ctx, c.Force, progress)
	return err
}
