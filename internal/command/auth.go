package command

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/mostlyserious/shots/internal/artifact"
	"github.com/mostlyserious/shots/internal/browser"
)

// stateMode keeps an authentication state file readable by its owner only: it
// holds session cookies and storage values.
const stateMode = 0o600

type authCmd struct {
	URL       string `arg:"" help:"URL, bare domain, or path to a local HTML file."`
	StateFile string `arg:"" name:"state-file" help:"Where to write the authentication state, or - for standard output."`

	DevTools   bool `name:"devtools" help:"Open developer tools in the sign-in browser."`
	LogConsole bool `help:"Write console messages to standard error."`

	browserFlags
}

// Run opens a visible browser, waits for the user to sign in, and saves the
// resulting cookies and per-origin local storage.
func (c *authCmd) Run(ctx context.Context, e *env) error {
	if err := c.browserFlags.validate(); err != nil {
		return err
	}
	target, err := NormalizeURL(c.URL)
	if err != nil {
		return err
	}
	b, err := browser.Connect(ctx, browser.Options{
		Path:     c.BrowserPath,
		CDPURL:   c.CDPURL,
		Args:     c.BrowserArg,
		Headed:   true,
		DevTools: c.DevTools,
	})
	if err != nil {
		return err
	}
	defer b.Close()

	opts := browser.PageOptions{
		UserAgent:     c.UserAgent,
		ReducedMotion: c.ReducedMotion,
		BypassCSP:     c.BypassCSP,
		Insecure:      c.Insecure,
		Username:      c.AuthUsername,
		Password:      c.AuthPassword,
	}
	if c.LogConsole {
		opts.OnConsole = func(m browser.Message) { fmt.Fprintf(e.stderr, "%s: %s\n", m.Type, m.Text) }
	}
	bc, err := b.NewContext(ctx, nil)
	if err != nil {
		return err
	}
	defer bc.Close()
	page, err := bc.NewPage(ctx, opts)
	if err != nil {
		return err
	}
	defer page.Close()

	if _, err := page.Navigate(ctx, target); err != nil {
		return err
	}
	fmt.Fprintln(e.stderr, "Hit <enter> after you have signed in:")
	if err := waitForEnter(ctx, e.stdin); err != nil {
		return err
	}
	state, err := page.State(ctx)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return artifact.Write(c.StateFile, append(data, '\n'), artifact.Options{Mode: stateMode, Stdout: e.stdout})
}

// loadState reads an authentication state file, in Playwright's storage_state
// shape. Unknown fields are ignored, so a file from a newer Playwright still
// restores the cookies and storage shots understands.
func loadState(path string) (*browser.State, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var state browser.State
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &state, nil
}
