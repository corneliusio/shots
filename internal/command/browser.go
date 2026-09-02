package command

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"

	"github.com/mostlyserious/shots/internal/browser"
)

// errBrowserExited reports that the browser went away under a command that
// was waiting to use it again.
var errBrowserExited = errors.New("browser exited")

// browserCmd launches Chrome for other commands to share through --cdp-url. It
// prints the DevTools URL, then stays in the foreground until it is interrupted
// or Chrome exits, and closes Chrome on its way out. It is the launch half of
// --cdp-url, so it takes the launch options --cdp-url rejects.
type browserCmd struct {
	Port        int      `placeholder:"PORT" help:"Loopback port for the DevTools endpoint; defaults to a free one."`
	BrowserPath string   `placeholder:"PATH" help:"Chrome executable to use, ahead of $SHOTS_BROWSER_PATH and discovery."`
	BrowserArg  []string `sep:"none" placeholder:"ARG" help:"Extra Chrome command-line argument; repeatable."`
}

func (c *browserCmd) Run(ctx context.Context, e *env) error {
	if c.Port < 0 || c.Port > 65535 {
		return usagef("--port must be between 0 and 65535")
	}
	port := c.Port
	if port == 0 {
		var err error
		if port, err = freePort(); err != nil {
			return err
		}
	}
	// Chrome binds a remote debugging port to loopback unless told otherwise,
	// and chromedp leaves an explicit port alone.
	args := append(slices.Clone(c.BrowserArg), fmt.Sprintf("remote-debugging-port=%d", port))
	b, err := browser.Connect(ctx, browser.Options{Path: c.BrowserPath, Args: args})
	if err != nil {
		return err
	}
	defer b.Close()
	if _, err := fmt.Fprintf(e.stdout, "http://127.0.0.1:%d\n", port); err != nil {
		return fmt.Errorf("writing the DevTools URL: %w", err)
	}
	select {
	case <-ctx.Done():
		return nil
	case <-b.Lost():
		// Cancellation also drops the connection, and is the normal way to end.
		if ctx.Err() != nil {
			return nil
		}
		return errBrowserExited
	}
}

// freePort asks the kernel for an unused loopback port. Chrome binds it a
// moment later, which is the usual small race.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
