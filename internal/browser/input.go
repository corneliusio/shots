package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

// centre scrolls the first element matching a selector into view and returns
// its centre in viewport coordinates, or null while it is not there yet.
const centreJS = `(() => {
	const el = document.querySelector(%s);
	if (!el) return null;
	el.scrollIntoView({block: 'center', inline: 'center'});
	const r = el.getBoundingClientRect();
	if (r.width === 0 && r.height === 0) return null;
	return {x: r.x + r.width / 2, y: r.y + r.height / 2};
})()`

// focusJS focuses the first element matching a selector.
const focusJS = `(() => {
	const el = document.querySelector(%s);
	if (!el) return false;
	el.focus();
	return true;
})()`

// fillJS replaces a field's value the way a user would leave it: through the
// native value setter, so a framework watching the property sees the change,
// then the input and change events a form listener waits for.
const fillJS = `(() => {
	const el = document.querySelector(%s);
	if (!el) return false;
	el.focus();
	const proto = Object.getPrototypeOf(el);
	const setter = Object.getOwnPropertyDescriptor(proto, 'value')?.set;
	if (setter) setter.call(el, %s); else el.value = %s;
	el.dispatchEvent(new Event('input', {bubbles: true}));
	el.dispatchEvent(new Event('change', {bubbles: true}));
	return true;
})()`

// scrollByJS scrolls the window, easing over duration seconds when there is
// one, so a recorded scroll reads as movement rather than as a jump.
const scrollByJS = `(({x, y, duration}) => new Promise(resolve => {
	if (!duration) { window.scrollBy(x, y); resolve(); return; }
	const startX = window.scrollX, startY = window.scrollY, start = performance.now();
	const ease = t => t < 0.5 ? 2 * t * t : 1 - Math.pow(-2 * t + 2, 2) / 2;
	const step = now => {
		const progress = Math.min((now - start) / (duration * 1000), 1);
		window.scrollTo(startX + x * ease(progress), startY + y * ease(progress));
		if (progress < 1) requestAnimationFrame(step); else resolve();
	};
	requestAnimationFrame(step);
}))(%s)`

// scrollToJS brings an element into view, smoothly when there is a duration.
const scrollToJS = `(() => {
	const el = document.querySelector(%s);
	if (!el) return false;
	el.scrollIntoView({behavior: %s, block: 'center'});
	return true;
})()`

// Click dispatches a real mouse click at the centre of the first element
// matching selector, waiting for it to appear first.
//
// The pointer is moved there before the button goes down, so an injected cursor
// travels to the element instead of appearing on top of it.
func (p *Page) Click(ctx context.Context, selector, button string, count int) error {
	x, y, err := p.centre(ctx, selector)
	if err != nil {
		return err
	}
	if button == "" {
		button = "left"
	}
	if count < 1 {
		count = 1
	}
	move := &input.DispatchMouseEventParams{Type: input.MouseMoved, X: x, Y: y}
	press := &input.DispatchMouseEventParams{
		Type: input.MousePressed, X: x, Y: y,
		Button: input.MouseButton(button), ClickCount: int64(count),
	}
	release := *press
	release.Type = input.MouseReleased
	return p.run(ctx, chromedp.ActionFunc(func(c context.Context) error {
		if err := move.Do(c); err != nil {
			return err
		}
		if err := press.Do(c); err != nil {
			return err
		}
		return release.Do(c)
	}))
}

// Fill replaces the value of the first element matching selector.
func (p *Page) Fill(ctx context.Context, selector, text string) error {
	value := quote(text)
	return p.act(ctx, selector, fmt.Sprintf(fillJS, quote(selector), value, value))
}

// Type focuses the element matching selector, when there is one, and sends the
// text a key at a time, delay apart, so a recording shows it being typed.
func (p *Page) Type(ctx context.Context, selector, text string, delay time.Duration) error {
	if selector != "" {
		if err := p.act(ctx, selector, fmt.Sprintf(focusJS, quote(selector))); err != nil {
			return err
		}
	}
	for _, r := range text {
		if err := p.keys(ctx, kb.Encode(r)); err != nil {
			return err
		}
		if err := p.Wait(ctx, delay); err != nil {
			return err
		}
	}
	return nil
}

// Press sends one key press, to the element matching selector when one is
// given and to whatever has focus otherwise.
func (p *Page) Press(ctx context.Context, selector, key string) error {
	if selector != "" {
		if err := p.act(ctx, selector, fmt.Sprintf(focusJS, quote(selector))); err != nil {
			return err
		}
	}
	events, err := encodeKey(key)
	if err != nil {
		return err
	}
	return p.keys(ctx, events)
}

// Scroll scrolls the page by x and y, or brings the element matching to into
// view. A duration eases the movement over that many seconds.
func (p *Page) Scroll(ctx context.Context, to string, x, y, duration float64) error {
	if to != "" {
		behavior := `'auto'`
		if duration > 0 {
			behavior = `'smooth'`
		}
		if err := p.act(ctx, to, fmt.Sprintf(scrollToJS, quote(to), behavior)); err != nil {
			return err
		}
		return p.Wait(ctx, seconds(duration))
	}
	args, err := json.Marshal(map[string]float64{"x": x, "y": y, "duration": duration})
	if err != nil {
		return err
	}
	return p.eval(ctx, fmt.Sprintf(scrollByJS, args), nil)
}

// WaitForSelector waits until an element matches selector.
func (p *Page) WaitForSelector(ctx context.Context, selector string) error {
	return p.WaitFor(ctx, "document.querySelector("+quote(selector)+") !== null")
}

// WaitForURL waits until the page's URL matches pattern, a glob in which "*"
// stands for anything but a path separator and "**" for anything at all. A
// pattern with no wildcard has to match the whole URL.
func (p *Page) WaitForURL(ctx context.Context, pattern string) error {
	re, err := glob(pattern)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(p.timeout)
	for {
		current, err := p.URL(ctx)
		if err != nil {
			return err
		}
		if re.MatchString(current) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("waiting for %s: still at %s after %s", pattern, current, p.timeout)
		}
		if err := p.Wait(ctx, 50*time.Millisecond); err != nil {
			return err
		}
	}
}

// URL is the page's current address.
func (p *Page) URL(ctx context.Context) (string, error) {
	var current string
	err := p.eval(ctx, "location.href", &current)
	return current, err
}

// centre waits for an element and returns the point to click.
func (p *Page) centre(ctx context.Context, selector string) (x, y float64, err error) {
	expr := fmt.Sprintf(centreJS, quote(selector))
	deadline := time.Now().Add(p.timeout)
	for {
		var point *struct{ X, Y float64 }
		if err := p.eval(ctx, expr, &point); err != nil {
			return 0, 0, err
		}
		if point != nil {
			return point.X, point.Y, nil
		}
		if time.Now().After(deadline) {
			return 0, 0, fmt.Errorf("no visible element matches %s after %s", selector, p.timeout)
		}
		if err := p.Wait(ctx, 50*time.Millisecond); err != nil {
			return 0, 0, err
		}
	}
}

// act waits for the element and then runs an expression over it, which returns
// false while it is not there.
func (p *Page) act(ctx context.Context, selector, expr string) error {
	deadline := time.Now().Add(p.timeout)
	for {
		var ok bool
		if err := p.eval(ctx, expr, &ok); err != nil {
			return err
		}
		if ok {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("no element matches %s after %s", selector, p.timeout)
		}
		if err := p.Wait(ctx, 50*time.Millisecond); err != nil {
			return err
		}
	}
}

// keys dispatches an encoded key sequence.
func (p *Page) keys(ctx context.Context, events []*input.DispatchKeyEventParams) error {
	return p.run(ctx, chromedp.ActionFunc(func(c context.Context) error {
		for _, ev := range events {
			if err := ev.Do(c); err != nil {
				return err
			}
		}
		return nil
	}))
}

// modifiers are the names a key combination may hold down. ControlOrMeta is
// Playwright's portable spelling of the platform's own shortcut modifier.
var modifiers = map[string]input.Modifier{
	"Alt":     input.ModifierAlt,
	"Control": input.ModifierCtrl,
	"Ctrl":    input.ModifierCtrl,
	"Meta":    input.ModifierCommand,
	"Shift":   input.ModifierShift,
}

// keyRunes maps every key name Chrome knows to the rune the keyboard tables are
// indexed by, so that "Enter", "ArrowDown" and "F5" all encode correctly.
var keyRunes = func() map[string]rune {
	names := make(map[string]rune, len(kb.Keys))
	for r, key := range kb.Keys {
		if _, taken := names[key.Key]; !taken {
			names[key.Key] = r
		}
	}
	names["Space"] = ' '
	return names
}()

// encodeKey turns a key name, optionally prefixed with held modifiers
// ("Shift+Tab", "ControlOrMeta+A"), into the events that press and release it.
func encodeKey(key string) ([]*input.DispatchKeyEventParams, error) {
	parts := strings.Split(key, "+")
	var held input.Modifier
	for _, name := range parts[:len(parts)-1] {
		if name == "ControlOrMeta" {
			name = "Control"
			if runtime.GOOS == "darwin" {
				name = "Meta"
			}
		}
		modifier, ok := modifiers[name]
		if !ok {
			return nil, fmt.Errorf("unknown key modifier %q in %q", name, key)
		}
		held |= modifier
	}
	name := parts[len(parts)-1]
	r, ok := keyRunes[name]
	if !ok {
		runes := []rune(name)
		if len(runes) != 1 {
			return nil, fmt.Errorf("unknown key %q", key)
		}
		r = runes[0]
	}
	events := kb.Encode(r)
	if held == 0 {
		return events, nil
	}
	// A key held with a modifier produces no text: Control+A selects, it does
	// not insert an "a", so the char event goes and the rest carry the mask.
	held |= events[0].Modifiers
	out := events[:0]
	for _, ev := range events {
		if ev.Type == input.KeyChar {
			continue
		}
		ev.Modifiers = held
		ev.Text, ev.UnmodifiedText = "", ""
		out = append(out, ev)
	}
	return out, nil
}

// globPattern splits a glob into the wildcards and the literal text between
// them.
var globPattern = regexp.MustCompile(`\*\*|[*?]`)

// glob compiles a URL glob. "**" matches anything, "*" anything without a
// slash, "?" one character that is not a slash.
func glob(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString(`\A`)
	last := 0
	for _, m := range globPattern.FindAllStringIndex(pattern, -1) {
		b.WriteString(regexp.QuoteMeta(pattern[last:m[0]]))
		switch pattern[m[0]:m[1]] {
		case "**":
			b.WriteString(`.*`)
		case "*":
			b.WriteString(`[^/]*`)
		default:
			b.WriteString(`[^/]`)
		}
		last = m[1]
	}
	b.WriteString(regexp.QuoteMeta(pattern[last:]))
	b.WriteString(`\z`)
	return regexp.Compile(b.String())
}

// quote renders a string as a JavaScript literal, so nothing a storyboard
// carries is ever spliced into source.
func quote(s string) string {
	quoted, err := json.Marshal(s)
	if err != nil { // a Go string always marshals
		return `""`
	}
	return string(quoted)
}

// seconds converts a storyboard's fractional seconds to a duration.
func seconds(f float64) time.Duration {
	if f <= 0 {
		return 0
	}
	return time.Duration(f * float64(time.Second))
}
