// Package browser launches or connects to Chrome and drives it over the
// DevTools Protocol.
//
// Every CDP type stays inside this package. Callers pass and receive plain Go
// values, so the CDP library can be replaced without touching command code.
package browser

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// DefaultTimeout bounds navigation and script execution.
const DefaultTimeout = 30 * time.Second

// Options configures [Connect].
//
// Setting CDPURL means connecting to a browser somebody else started, which
// cannot change how it was started, so every launch option is then ignored. The
// command layer rejects the combination as a usage error.
type Options struct {
	// Path is an explicit browser executable. Empty means [Discover].
	Path string
	// CDPURL is the DevTools endpoint of an already running browser, either a
	// http://host:port address or a ws:// browser WebSocket URL.
	CDPURL string
	// Args are extra Chrome command-line arguments, without the leading "--".
	Args []string
	// Headed shows the browser window instead of running headless.
	Headed bool
	// DevTools opens developer tools, and implies Headed.
	DevTools bool
	// Window sizes the browser window at launch. Zero leaves Chrome's default.
	// [Page.Fit] then trims it to an exact content size.
	WindowWidth, WindowHeight int
	// Timeout bounds navigation and script execution. Zero means
	// [DefaultTimeout].
	Timeout time.Duration
}

// Browser is a Chrome process, or a connection to one somebody else started.
//
// It holds a context because that is how chromedp identifies a connection; the
// context passed to [Connect] owns the browser's lifetime, so cancelling it
// closes a browser shots launched.
type Browser struct {
	ctx     context.Context
	cancel  context.CancelFunc
	alloc   context.CancelFunc
	path    string
	timeout time.Duration
	// launched is false when shots connected to an existing browser and must
	// therefore leave it running.
	launched bool
}

// Connect launches Chrome, or connects to the browser named by
// [Options].CDPURL. The caller must call [Browser.Close].
func Connect(ctx context.Context, opts Options) (*Browser, error) {
	b := &Browser{timeout: opts.Timeout, launched: opts.CDPURL == ""}
	if b.timeout <= 0 {
		b.timeout = DefaultTimeout
	}

	var allocCtx context.Context
	if b.launched {
		path, err := discover(opts.Path, opts.Headed || opts.DevTools)
		if err != nil {
			return nil, err
		}
		b.path = path
		allocCtx, b.alloc = chromedp.NewExecAllocator(ctx, execOptions(path, opts)...)
	} else {
		allocCtx, b.alloc = chromedp.NewRemoteAllocator(ctx, opts.CDPURL)
	}
	b.ctx, b.cancel = chromedp.NewContext(allocCtx, chromedp.WithErrorf(browserErrorf))

	if err := start(ctx, b.ctx, b.timeout); err != nil {
		b.Close()
		if b.launched {
			return nil, fmt.Errorf("launching %s: %w", b.path, err)
		}
		return nil, fmt.Errorf("connecting to %s: %w", opts.CDPURL, err)
	}
	return b, nil
}

// browserErrorf keeps chromedp's useful internal errors while dropping one
// harmless message caused by Chrome emitting a DOM event chromedp does not yet
// use in its node cache.
func browserErrorf(format string, args ...any) {
	if format == "unhandled node event %T" && len(args) == 1 {
		if _, ok := args[0].(*dom.EventAdoptedStyleSheetsModified); ok {
			return
		}
	}
	log.Printf(format, args...)
}

func execOptions(exe string, opts Options) []chromedp.ExecAllocatorOption {
	out := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	out = append(out, chromedp.ExecPath(exe))
	if opts.Headed || opts.DevTools {
		out = append(out, chromedp.Flag("headless", false))
	}
	if opts.DevTools {
		out = append(out, chromedp.Flag("auto-open-devtools-for-tabs", true))
	}
	if opts.WindowWidth > 0 && opts.WindowHeight > 0 {
		out = append(out, chromedp.WindowSize(opts.WindowWidth, opts.WindowHeight))
	}
	for _, arg := range opts.Args {
		name, value, ok := splitArg(arg)
		if ok {
			out = append(out, chromedp.Flag(name, value))
		} else {
			out = append(out, chromedp.Flag(name, true))
		}
	}
	return out
}

// splitArg turns "--disable-gpu" or "--lang=fr" into a chromedp flag name and
// an optional value.
func splitArg(arg string) (name, value string, hasValue bool) {
	name = arg
	for len(name) > 0 && name[0] == '-' {
		name = name[1:]
	}
	for i := range name {
		if name[i] == '=' {
			return name[:i], name[i+1:], true
		}
	}
	return name, "", false
}

// Close ends the session. A browser shots launched is terminated; a browser
// reached through --cdp-url is left running.
func (b *Browser) Close() {
	if b.cancel != nil {
		b.cancel()
	}
	if b.alloc != nil {
		b.alloc()
	}
}

// Lost is closed when the connection to the browser is gone, which for a
// browser shots launched means Chrome exited. Callers that wait for input
// select on it so they do not wait for a browser that can no longer answer.
func (b *Browser) Lost() <-chan struct{} {
	return chromedp.FromContext(b.ctx).Browser.LostConnection
}

// Version reports the browser's product string, such as "Chrome/152.0.7977.65".
func (b *Browser) Version(ctx context.Context) (string, error) {
	var product string
	err := b.run(ctx, chromedp.ActionFunc(func(c context.Context) error {
		var err error
		_, product, _, _, _, err = browser.GetVersion().Do(c)
		return err
	}))
	return product, err
}

// run executes actions against the browser, bounded by its timeout and by ctx.
func (b *Browser) run(ctx context.Context, actions ...chromedp.Action) error {
	return run(ctx, b.ctx, b.timeout, actions...)
}

// start performs the first Run on a chromedp context.
//
// It must be the context itself, never a derived one: chromedp ties the browser
// process and the page target to the context of their first Run, so a deadline
// wrapped around it would tear them down the moment it expired. The timeout is
// therefore applied by waiting rather than by cancelling.
func start(ctx, cdpCtx context.Context, timeout time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- chromedp.Run(cdpCtx) }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		return fmt.Errorf("timed out after %s", timeout)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// run grafts a caller's cancellation onto a chromedp context: chromedp needs
// its own context to find the connection, and callers need theirs to stop the
// work. Deadlines cannot merge, so the operation gets both.
func run(ctx, cdpCtx context.Context, timeout time.Duration, actions ...chromedp.Action) error {
	opCtx, cancel := context.WithTimeout(cdpCtx, timeout)
	defer cancel()
	defer context.AfterFunc(ctx, cancel)()

	err := chromedp.Run(opCtx, actions...)
	switch {
	case err == nil:
		return nil
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("timed out after %s", timeout)
	}
	return err
}

// executor derives a context that sends commands to the browser itself rather
// than to a page target, keeping ctx's deadline and cancellation.
func (b *Browser) executor(ctx context.Context) context.Context {
	return cdp.WithExecutor(ctx, chromedp.FromContext(b.ctx).Browser)
}

// newBrowserContext creates an isolated browser context.
//
// chromedp's WithNewBrowserContext cannot be used: Chrome refuses
// Target.createTarget in a fresh browser context unless newWindow is set, and
// chromedp does not set it, so shots creates the context and its targets itself.
func (b *Browser) newBrowserContext(ctx context.Context) (cdp.BrowserContextID, error) {
	var contextID cdp.BrowserContextID
	err := b.run(ctx, chromedp.ActionFunc(func(c context.Context) error {
		var err error
		contextID, err = target.CreateBrowserContext().Do(b.executor(c))
		return err
	}))
	return contextID, err
}

// newTargetIn opens another page in an existing browser context, so it shares
// that context's cookies and storage.
func (b *Browser) newTargetIn(ctx context.Context, contextID cdp.BrowserContextID) (target.ID, error) {
	var targetID target.ID
	err := b.run(ctx, chromedp.ActionFunc(func(c context.Context) error {
		var err error
		targetID, err = target.CreateTarget("about:blank").
			WithBrowserContextID(contextID).
			WithNewWindow(true).
			Do(b.executor(c))
		return err
	}))
	return targetID, err
}
