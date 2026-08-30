package browser

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	cdppage "github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// Message is one console message.
type Message struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Request is one completed network request, as reported by --log-requests.
type Request struct {
	Method string `json:"method"`
	URL    string `json:"url"`
	Status int64  `json:"status"`
	// Size is the number of bytes Chrome received on the wire, or null when
	// the request failed.
	Size *int64 `json:"size"`
}

// PageOptions configures a page before anything is loaded into it.
type PageOptions struct {
	// Width and Height set the viewport. Zero leaves Chrome's own default.
	Width, Height int
	// ScaleFactor is the device scale factor. Zero means 1.
	ScaleFactor float64
	// UserAgent overrides the browser's user agent.
	UserAgent string
	// ReducedMotion emulates prefers-reduced-motion: reduce.
	ReducedMotion bool
	// Media emulates a CSS media type, such as "screen" or "print". Empty
	// leaves the page's own.
	Media string
	// BypassCSP disables the page's Content-Security-Policy.
	BypassCSP bool
	// Username and Password answer HTTP basic authentication challenges. They
	// are sent only in response to a challenge, and never logged.
	Username, Password string
	// OnConsole and OnRequest, when set, receive console messages and
	// completed requests. They are called from a background goroutine and must
	// not block.
	OnConsole func(Message)
	OnRequest func(Request)
}

// Page is one tab in its own isolated browser context.
type Page struct {
	ctx       context.Context
	cancel    context.CancelFunc
	browser   *Browser
	contextID cdp.BrowserContextID
	timeout   time.Duration
	// mainFrame is written once, before any event listener exists, and read
	// only afterwards.
	mainFrame cdp.FrameID

	mu sync.Mutex
	// docStatus is the HTTP status of the main frame's most recent document
	// response, which is the top-level navigation's final response.
	docStatus int64
	pending   map[network.RequestID]*Request
}

// NewPage opens a page in a fresh isolated browser context, so cookies and
// storage never leak between pages. The caller must call [Page.Close].
func (b *Browser) NewPage(ctx context.Context, opts PageOptions) (*Page, error) {
	contextID, targetID, err := b.newTarget(ctx)
	if err != nil {
		return nil, fmt.Errorf("creating page: %w", err)
	}
	p := &Page{
		browser:   b,
		contextID: contextID,
		timeout:   b.timeout,
		pending:   map[network.RequestID]*Request{},
	}
	p.ctx, p.cancel = chromedp.NewContext(b.ctx, chromedp.WithTargetID(targetID))
	if err := start(ctx, p.ctx, p.timeout); err != nil {
		p.Close()
		return nil, fmt.Errorf("attaching to page: %w", err)
	}
	// The main frame is resolved before any listener exists, so the event
	// goroutine that reads it never races the assignment.
	if err := p.resolveMainFrame(ctx); err != nil {
		p.Close()
		return nil, err
	}
	p.listen(opts)
	if err := p.configure(ctx, opts); err != nil {
		p.Close()
		return nil, err
	}
	return p, nil
}

// resolveMainFrame records the target's own frame, which is the one whose
// document response carries the navigation's HTTP status.
func (p *Page) resolveMainFrame(ctx context.Context) error {
	return p.run(ctx, chromedp.ActionFunc(func(c context.Context) error {
		tree, err := cdppage.GetFrameTree().Do(c)
		if err != nil {
			return err
		}
		p.mainFrame = tree.Frame.ID
		return nil
	}))
}

// Close closes the page and disposes its browser context.
func (p *Page) Close() {
	if p.cancel != nil {
		p.cancel()
	}
	if p.contextID != "" {
		// The page is already gone; disposing its context is best-effort
		// cleanup that must not hang a command that is on its way out.
		ctx, cancel := context.WithTimeout(p.browser.ctx, 5*time.Second)
		defer cancel()
		target.DisposeBrowserContext(p.contextID).Do(p.browser.executor(ctx))
		p.contextID = ""
	}
}

func (p *Page) run(ctx context.Context, actions ...chromedp.Action) error {
	return run(ctx, p.ctx, p.timeout, actions...)
}

func (p *Page) configure(ctx context.Context, opts PageOptions) error {
	return p.run(ctx, chromedp.ActionFunc(func(c context.Context) error {
		if err := network.Enable().Do(c); err != nil {
			return err
		}
		if err := runtime.Enable().Do(c); err != nil {
			return err
		}
		if opts.Width > 0 || opts.Height > 0 || opts.ScaleFactor > 0 {
			scale := opts.ScaleFactor
			if scale <= 0 {
				scale = 1
			}
			metrics := emulation.SetDeviceMetricsOverride(int64(opts.Width), int64(opts.Height), scale, false)
			if err := metrics.Do(c); err != nil {
				return err
			}
		}
		if opts.UserAgent != "" {
			if err := emulation.SetUserAgentOverride(opts.UserAgent).Do(c); err != nil {
				return err
			}
		}
		// One command carries both, because a second call replaces the first.
		if opts.ReducedMotion || opts.Media != "" {
			media := emulation.SetEmulatedMedia().WithMedia(opts.Media)
			if opts.ReducedMotion {
				media = media.WithFeatures([]*emulation.MediaFeature{{Name: "prefers-reduced-motion", Value: "reduce"}})
			}
			if err := media.Do(c); err != nil {
				return err
			}
		}
		if opts.BypassCSP {
			if err := cdppage.SetBypassCSP(true).Do(c); err != nil {
				return err
			}
		}
		if opts.Username != "" || opts.Password != "" {
			return fetch.Enable().WithHandleAuthRequests(true).Do(c)
		}
		return nil
	}))
}

// listen wires the CDP events shots reports on. chromedp calls listeners on the
// connection's read goroutine, so anything that talks back to Chrome runs in a
// goroutine of its own.
func (p *Page) listen(opts PageOptions) {
	chromedp.ListenTarget(p.ctx, func(ev any) {
		switch ev := ev.(type) {
		case *network.EventRequestWillBeSent:
			if opts.OnRequest != nil {
				p.mu.Lock()
				p.pending[ev.RequestID] = &Request{Method: ev.Request.Method, URL: ev.Request.URL}
				p.mu.Unlock()
			}
		case *network.EventResponseReceived:
			p.mu.Lock()
			if ev.Type == network.ResourceTypeDocument && ev.FrameID == p.mainFrame {
				p.docStatus = ev.Response.Status
			}
			if r := p.pending[ev.RequestID]; r != nil {
				r.Status = ev.Response.Status
				r.URL = ev.Response.URL
			}
			p.mu.Unlock()
		case *network.EventLoadingFinished:
			if r := p.finish(ev.RequestID); r != nil {
				size := int64(ev.EncodedDataLength)
				r.Size = &size
				opts.OnRequest(*r)
			}
		case *network.EventLoadingFailed:
			if r := p.finish(ev.RequestID); r != nil {
				opts.OnRequest(*r)
			}
		case *runtime.EventConsoleAPICalled:
			if opts.OnConsole != nil {
				opts.OnConsole(Message{Type: ev.Type.String(), Text: consoleText(ev.Args)})
			}
		case *fetch.EventAuthRequired:
			go p.answerAuth(ev, opts)
		case *fetch.EventRequestPaused:
			go p.run(context.Background(), chromedp.ActionFunc(func(c context.Context) error {
				return fetch.ContinueRequest(ev.RequestID).Do(c)
			}))
		}
	})
}

// finish removes a tracked request and returns it, or nil when requests are not
// being logged.
func (p *Page) finish(id network.RequestID) *Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	r := p.pending[id]
	delete(p.pending, id)
	return r
}

func (p *Page) answerAuth(ev *fetch.EventAuthRequired, opts PageOptions) {
	response := &fetch.AuthChallengeResponse{Response: fetch.AuthChallengeResponseResponseProvideCredentials, Username: opts.Username, Password: opts.Password}
	p.run(context.Background(), chromedp.ActionFunc(func(c context.Context) error {
		return fetch.ContinueWithAuth(ev.RequestID, response).Do(c)
	}))
}

func consoleText(args []*runtime.RemoteObject) string {
	parts := make([]string, 0, len(args))
	for _, arg := range args {
		switch {
		case arg.Type == runtime.TypeString && len(arg.Value) > 0:
			var s string
			if err := json.Unmarshal(arg.Value, &s); err == nil {
				parts = append(parts, s)
				continue
			}
			parts = append(parts, string(arg.Value))
		case len(arg.Value) > 0:
			parts = append(parts, string(arg.Value))
		case arg.Description != "":
			parts = append(parts, arg.Description)
		default:
			parts = append(parts, string(arg.Type))
		}
	}
	return strings.Join(parts, " ")
}

// Navigate loads rawURL and reports the HTTP status of the main document. The
// status is 0 when the page did not come from HTTP, such as a file: URL.
func (p *Page) Navigate(ctx context.Context, rawURL string) (int, error) {
	p.mu.Lock()
	p.docStatus = 0
	p.mu.Unlock()
	err := p.run(ctx, chromedp.Navigate(rawURL))
	p.mu.Lock()
	status := int(p.docStatus)
	p.mu.Unlock()
	// Chrome turns an unanswered authentication challenge into a navigation
	// error, but the 401 response still arrived. Reporting the status lets
	// --skip and --fail treat it like any other HTTP failure.
	if err != nil && status < 400 {
		return 0, fmt.Errorf("navigating to %s: %w", rawURL, err)
	}
	return status, nil
}

// Wait sleeps, or returns early if ctx is cancelled.
func (p *Page) Wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-p.ctx.Done():
		return p.ctx.Err()
	}
}

// Eval runs the caller's JavaScript and returns its result as JSON, or nil for
// undefined and for an empty source.
func (p *Page) Eval(ctx context.Context, source string) (json.RawMessage, error) {
	return p.evaluate(ctx, source, true)
}

// Exec runs the caller's JavaScript for its effect on the page and discards its
// result. Chrome is never asked to serialize the value, so a script ending in
// something JSON cannot hold — window, a DOM node, a cyclic object — still
// succeeds.
func (p *Page) Exec(ctx context.Context, source string) error {
	_, err := p.evaluate(ctx, source, false)
	return err
}

// evaluate runs source, optionally bringing its value back.
//
// A source that evaluates to a function is called, as Playwright's
// page.evaluate does, so both "document.title" and "() => { ... }" behave as
// upstream users expect. Promises are awaited, at either step.
func (p *Page) evaluate(ctx context.Context, source string, byValue bool) (json.RawMessage, error) {
	if strings.TrimSpace(source) == "" {
		return nil, nil
	}
	var value json.RawMessage
	err := p.run(ctx, chromedp.ActionFunc(func(c context.Context) error {
		// The first evaluation cannot ask for the value: a source that
		// evaluates to a function has to come back as a handle to call.
		result, exc, err := runtime.Evaluate(source).WithAwaitPromise(true).WithUserGesture(true).Do(c)
		if err != nil {
			return err
		}
		if exc != nil {
			return jsError(exc)
		}
		// A handle has to go back to the page to be called, or to be
		// serialized. A caller that wants neither is already done.
		isFunc := result.Type == runtime.TypeFunction
		if result.ObjectID != "" && (byValue || isFunc) {
			body := "function() { return this; }"
			if isFunc {
				body = "function() { return this(); }"
			}
			call := runtime.CallFunctionOn(body).WithObjectID(result.ObjectID).WithAwaitPromise(true)
			result, exc, err = call.WithReturnByValue(byValue).Do(c)
			if err != nil {
				return err
			}
			if exc != nil {
				return jsError(exc)
			}
		}
		value = json.RawMessage(result.Value)
		return nil
	}))
	if err != nil {
		return nil, err
	}
	return value, nil
}

// WaitFor polls a JavaScript condition until it is truthy or the timeout
// expires.
func (p *Page) WaitFor(ctx context.Context, condition string) error {
	if strings.TrimSpace(condition) == "" {
		return nil
	}
	// Wrapping in a function call keeps a condition written as a function
	// ("() => done") working alongside a plain expression.
	expr := "Promise.resolve((() => { const r = (" + condition + "); return typeof r === 'function' ? r() : r; })()).then(Boolean)"
	deadline := time.Now().Add(p.timeout)
	for {
		var done bool
		if err := p.eval(ctx, expr, &done); err != nil {
			return fmt.Errorf("waiting for %s: %w", condition, err)
		}
		if done {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("waiting for %s: timed out after %s", condition, p.timeout)
		}
		if err := p.Wait(ctx, 50*time.Millisecond); err != nil {
			return err
		}
	}
}

// eval evaluates an expression shots itself wrote and decodes the result.
func (p *Page) eval(ctx context.Context, expr string, out any) error {
	return p.run(ctx, chromedp.ActionFunc(func(c context.Context) error {
		result, exc, err := runtime.Evaluate(expr).WithAwaitPromise(true).WithReturnByValue(true).Do(c)
		if err != nil {
			return err
		}
		if exc != nil {
			return jsError(exc)
		}
		if out == nil {
			return nil
		}
		return json.Unmarshal(result.Value, out)
	}))
}

// documentHTML serializes the rendered document the way Playwright's
// page.content() does: the doctype, when there is one, then the document
// element's markup.
const documentHTML = `(() => {
	const doctype = document.doctype;
	const prefix = doctype ? new XMLSerializer().serializeToString(doctype) + '\n' : '';
	return prefix + document.documentElement.outerHTML;
})()`

// elementHTML is the outer HTML of the first match, or null when nothing
// matches, which the caller turns into an error.
const elementHTML = `(() => {
	const el = document.querySelector(%s);
	return el && el.outerHTML;
})()`

// HTML returns the rendered document, or the outer HTML of the first element
// matching selector.
func (p *Page) HTML(ctx context.Context, selector string) (string, error) {
	if selector == "" {
		var html string
		if err := p.eval(ctx, documentHTML, &html); err != nil {
			return "", err
		}
		return html, nil
	}
	quoted, err := json.Marshal(selector)
	if err != nil {
		return "", err
	}
	var html *string
	if err := p.eval(ctx, fmt.Sprintf(elementHTML, quoted), &html); err != nil {
		return "", err
	}
	if html == nil {
		return "", fmt.Errorf("no element matches %s", selector)
	}
	return *html, nil
}

func jsError(exc *runtime.ExceptionDetails) error {
	if exc.Exception != nil && exc.Exception.Description != "" {
		// Chrome's description carries a stack trace; the first line is the
		// message users need.
		return fmt.Errorf("%s", strings.SplitN(exc.Exception.Description, "\n", 2)[0])
	}
	return fmt.Errorf("%s", exc.Text)
}

// Rect is a rectangle in CSS pixels, relative to the top of the document.
type Rect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// Selectors names the elements a screenshot should be cropped to. Selectors and
// JS are single matches; the All variants take every match. The result is the
// bounding rectangle that contains all of them.
type Selectors struct {
	CSS     []string `json:"selectors"`
	CSSAll  []string `json:"selectorsAll"`
	JS      []string `json:"jsSelectors"`
	JSAll   []string `json:"jsSelectorsAll"`
	Padding int      `json:"padding"`
}

// Empty reports whether no selector was given.
func (s Selectors) Empty() bool {
	return len(s.CSS)+len(s.CSSAll)+len(s.JS)+len(s.JSAll) == 0
}

//go:embed region.js
var regionJS string

// Region resolves selectors to the padded rectangle enclosing every element
// they match.
func (p *Page) Region(ctx context.Context, sel Selectors) (Rect, error) {
	spec, err := json.Marshal(sel)
	if err != nil {
		return Rect{}, err
	}
	var result struct {
		Rect
		Error string `json:"error"`
	}
	if err := p.eval(ctx, "("+regionJS+")("+string(spec)+")", &result); err != nil {
		return Rect{}, err
	}
	if result.Error != "" {
		return Rect{}, fmt.Errorf("%s", result.Error)
	}
	return result.Rect, nil
}

// ShotOptions configures [Page.Screenshot].
type ShotOptions struct {
	// JPEG selects JPEG output; PNG is the default.
	JPEG bool
	// Quality is the JPEG quality, 1 to 100. Ignored for PNG.
	Quality int
	// Clip crops the screenshot to a rectangle in document coordinates.
	Clip *Rect
	// FullPage captures the whole document rather than the viewport. It is
	// ignored when Clip is set.
	FullPage bool
	// OmitBackground makes the page's default white background transparent.
	OmitBackground bool
}

// Screenshot captures the page.
func (p *Page) Screenshot(ctx context.Context, opts ShotOptions) ([]byte, error) {
	var buf []byte
	err := p.run(ctx, chromedp.ActionFunc(func(c context.Context) error {
		if opts.OmitBackground {
			transparent := emulation.SetDefaultBackgroundColorOverride().WithColor(&cdp.RGBA{})
			if err := transparent.Do(c); err != nil {
				return err
			}
			defer emulation.SetDefaultBackgroundColorOverride().Do(c)
		}
		shot := cdppage.CaptureScreenshot().WithFormat(cdppage.CaptureScreenshotFormatPng)
		if opts.JPEG {
			shot = shot.WithFormat(cdppage.CaptureScreenshotFormatJpeg).WithQuality(int64(opts.Quality))
		}
		clip := opts.Clip
		if clip == nil && opts.FullPage {
			_, _, _, _, _, content, err := cdppage.GetLayoutMetrics().Do(c)
			if err != nil {
				return err
			}
			clip = &Rect{Width: content.Width, Height: content.Height}
		}
		if clip != nil {
			if clip.Width <= 0 || clip.Height <= 0 {
				return fmt.Errorf("region is empty: %gx%g", clip.Width, clip.Height)
			}
			viewport := &cdppage.Viewport{X: clip.X, Y: clip.Y, Width: clip.Width, Height: clip.Height, Scale: 1}
			shot = shot.WithClip(viewport).WithCaptureBeyondViewport(true)
		}
		var err error
		buf, err = shot.Do(c)
		return err
	}))
	if err != nil {
		return nil, err
	}
	return buf, nil
}
