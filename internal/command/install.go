package command

import (
	"context"
	"fmt"
	"io"
	"strings"

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
	exe, err := browser.Install(ctx, c.Force, progress)
	if err != nil {
		return err
	}
	// The archive carries no system dependencies, so a download can succeed and
	// still leave a browser that cannot start. shots reports them and installs
	// nothing (PLAN.md Section 5).
	if missing := browser.MissingLibraries(ctx, exe); len(missing) > 0 {
		return fmt.Errorf("%s needs shared libraries this machine does not have: %s", exe, strings.Join(missing, ", "))
	}
	return nil
}
