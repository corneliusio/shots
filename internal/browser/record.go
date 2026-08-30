package browser

import (
	"context"
	"encoding/base64"
	"time"

	cdpbrowser "github.com/chromedp/cdproto/browser"
	cdppage "github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// Frame is one frame of a screencast: an encoded PNG and the moment Chrome
// composited it.
type Frame struct {
	Data []byte
	At   time.Time
}

// fitAttempts bounds [Page.Fit]'s search. One adjustment is normally enough;
// a window manager that rounds the size gets a couple more chances.
const fitAttempts = 4

// Fit resizes the browser window until the page's content area is width by
// height, and reports the size it reached.
//
// A screencast captures the window's rendered surface, not the emulated
// viewport, so a recording comes out at the asked-for size only if the real
// window is that size. How much larger than its content a window is differs by
// platform, so the difference is measured rather than assumed.
//
// The result is what the caller got, which is not always what it asked for:
// Chrome enforces a minimum window size, so a viewport narrower than that
// cannot be reached. Recording at the size the window actually is beats
// refusing to record at all.
func (p *Page) Fit(ctx context.Context, width, height int) (int, int, error) {
	var inner, previous struct{ W, H int }
	for range fitAttempts {
		if err := p.eval(ctx, `({W: innerWidth, H: innerHeight})`, &inner); err != nil {
			return 0, 0, err
		}
		// Either it fits, or the window has stopped moving towards the size.
		if (inner.W == width && inner.H == height) || inner == previous {
			break
		}
		previous = inner
		if err := p.resize(ctx, width-inner.W, height-inner.H); err != nil {
			return 0, 0, err
		}
	}
	return inner.W, inner.H, nil
}

// resize grows the browser window by dw and dh.
func (p *Page) resize(ctx context.Context, dw, dh int) error {
	return p.run(ctx, chromedp.ActionFunc(func(c context.Context) error {
		id := chromedp.FromContext(c).Target.TargetID
		window, bounds, err := cdpbrowser.GetWindowForTarget().WithTargetID(id).Do(p.bc.b.executor(c))
		if err != nil {
			return err
		}
		next := &cdpbrowser.Bounds{Width: bounds.Width + int64(dw), Height: bounds.Height + int64(dh)}
		return cdpbrowser.SetWindowBounds(window, next).Do(p.bc.b.executor(c))
	}))
}

// Record streams screencast frames to onFrame until [Page.StopRecording].
//
// onFrame runs on the connection's read goroutine, in frame order, so it must
// hand the frame on rather than do the work itself. Chrome sends the next frame
// only once this one is acknowledged, which happens on a goroutine of its own
// because acknowledging is another CDP call.
func (p *Page) Record(ctx context.Context, width, height int, onFrame func(Frame)) error {
	p.mu.Lock()
	p.onFrame = onFrame
	p.mu.Unlock()
	return p.run(ctx, chromedp.ActionFunc(func(c context.Context) error {
		return cdppage.StartScreencast().
			WithFormat(cdppage.ScreencastFormatPng).
			WithMaxWidth(int64(width)).
			WithMaxHeight(int64(height)).
			WithEveryNthFrame(1).
			Do(c)
	}))
}

// StopRecording ends the screencast. Frames already in flight are dropped.
func (p *Page) StopRecording(ctx context.Context) error {
	p.mu.Lock()
	p.onFrame = nil
	p.mu.Unlock()
	return p.run(ctx, chromedp.ActionFunc(func(c context.Context) error {
		return cdppage.StopScreencast().Do(c)
	}))
}

// frame delivers one screencast frame and acknowledges it.
func (p *Page) frame(ev *cdppage.EventScreencastFrame) {
	p.mu.Lock()
	onFrame := p.onFrame
	p.mu.Unlock()
	// Acknowledging is what makes Chrome send the next frame; a frame that
	// arrives after StopRecording is acknowledged too, so a screencast the page
	// outlives cannot wedge.
	go p.run(context.Background(), chromedp.ActionFunc(func(c context.Context) error {
		return cdppage.ScreencastFrameAck(ev.SessionID).Do(c)
	}))
	if onFrame == nil {
		return
	}
	data, err := base64.StdEncoding.DecodeString(ev.Data)
	if err != nil {
		return
	}
	at := time.Now()
	if ev.Metadata != nil && ev.Metadata.Timestamp != nil {
		at = ev.Metadata.Timestamp.Time()
	}
	onFrame(Frame{Data: data, At: at})
}
