package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/domstorage"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	cdppage "github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/storage"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// State is a browser context's authentication state: its cookies, and the
// local storage of the origins that have some.
//
// The JSON shape is Playwright's storage_state, so a file written by
// shot-scraper auth restores here unchanged, and a file shots wrote restores
// there. Its values are credentials: nothing in shots prints them.
type State struct {
	Cookies []Cookie `json:"cookies"`
	Origins []Origin `json:"origins"`
}

// Cookie is one cookie. Expires is seconds since the Unix epoch, and -1 for a
// session cookie.
type Cookie struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain"`
	Path     string  `json:"path"`
	Expires  float64 `json:"expires"`
	HTTPOnly bool    `json:"httpOnly"`
	Secure   bool    `json:"secure"`
	// SameSite is "Strict", "Lax" or "None". It is omitted when the browser
	// did not report one, which Playwright also accepts.
	SameSite string `json:"sameSite,omitempty"`
}

// Origin is one origin's local storage.
type Origin struct {
	Origin       string `json:"origin"`
	LocalStorage []Item `json:"localStorage"`
}

// Item is one local-storage key and its value.
type Item struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// State exports the page's browser context: its cookies, and the local storage
// of every origin the page holds a frame for.
func (p *Page) State(ctx context.Context) (State, error) {
	state := State{Cookies: []Cookie{}, Origins: []Origin{}}
	err := p.run(ctx, chromedp.ActionFunc(func(c context.Context) error {
		cookies, err := storage.GetCookies().WithBrowserContextID(p.contextID).Do(p.browser.executor(c))
		if err != nil {
			return fmt.Errorf("reading cookies: %w", err)
		}
		state.Cookies = exportCookies(cookies)
		if err := domstorage.Enable().Do(c); err != nil {
			return err
		}
		tree, err := cdppage.GetFrameTree().Do(c)
		if err != nil {
			return err
		}
		for _, origin := range frameOrigins(tree) {
			id := &domstorage.StorageID{SecurityOrigin: origin, IsLocalStorage: true}
			items, err := domstorage.GetDOMStorageItems(id).Do(c)
			if err != nil {
				return fmt.Errorf("reading local storage for %s: %w", origin, err)
			}
			if len(items) == 0 {
				continue
			}
			state.Origins = append(state.Origins, Origin{Origin: origin, LocalStorage: exportItems(items)})
		}
		return nil
	}))
	if err != nil {
		return State{}, err
	}
	return state, nil
}

// restore writes state into a fresh browser context, before anything is loaded
// into it.
//
// Cookies go in directly. Local storage belongs to an origin and can only be
// written by a page on it, so each recorded origin gets a page of its own,
// closed again before the caller navigates. Its request is answered with a
// blank document instead of being fetched, so restoring a state file never
// touches the network.
func (b *Browser) restore(ctx context.Context, contextID cdp.BrowserContextID, state *State) error {
	if state == nil {
		return nil
	}
	if len(state.Cookies) > 0 {
		err := b.run(ctx, chromedp.ActionFunc(func(c context.Context) error {
			return storage.SetCookies(restoreCookies(state.Cookies)).WithBrowserContextID(contextID).Do(b.executor(c))
		}))
		if err != nil {
			return fmt.Errorf("restoring cookies: %w", err)
		}
	}
	for _, origin := range state.Origins {
		// Only an origin a page can be opened on has storage to restore.
		if len(origin.LocalStorage) == 0 || !networkOrigin(origin.Origin) {
			continue
		}
		if err := b.restoreOrigin(ctx, contextID, origin); err != nil {
			return fmt.Errorf("restoring local storage for %s: %w", origin.Origin, err)
		}
	}
	return nil
}

func (b *Browser) restoreOrigin(ctx context.Context, contextID cdp.BrowserContextID, origin Origin) error {
	targetID, err := b.newTargetIn(ctx, contextID)
	if err != nil {
		return err
	}
	defer b.closeTarget(targetID)
	pageCtx, cancel := chromedp.NewContext(b.ctx, chromedp.WithTargetID(targetID))
	defer cancel()
	if err := start(ctx, pageCtx, b.timeout); err != nil {
		return err
	}
	chromedp.ListenTarget(pageCtx, func(ev any) {
		if paused, ok := ev.(*fetch.EventRequestPaused); ok {
			go run(ctx, pageCtx, b.timeout, chromedp.ActionFunc(func(c context.Context) error {
				return fetch.FulfillRequest(paused.RequestID, 200).
					WithResponseHeaders([]*fetch.HeaderEntry{{Name: "Content-Type", Value: "text/html; charset=utf-8"}}).
					WithBody("").Do(c)
			}))
		}
	})
	script, err := writeStorage(origin.LocalStorage)
	if err != nil {
		return err
	}
	return run(ctx, pageCtx, b.timeout, chromedp.ActionFunc(func(c context.Context) error {
		pattern := []*fetch.RequestPattern{{URLPattern: origin.Origin + "/*"}}
		if err := fetch.Enable().WithPatterns(pattern).Do(c); err != nil {
			return err
		}
		if err := chromedp.Navigate(origin.Origin + "/").Do(c); err != nil {
			return err
		}
		_, exc, err := runtime.Evaluate(script).Do(c)
		if err != nil {
			return err
		}
		if exc != nil {
			return jsError(exc)
		}
		return fetch.Disable().Do(c)
	}))
}

// closeTarget disposes a page that has served its purpose. It is best-effort
// cleanup that must not hang a command on its way out.
func (b *Browser) closeTarget(id target.ID) {
	ctx, cancel := context.WithTimeout(b.ctx, 5*time.Second)
	defer cancel()
	target.CloseTarget(id).Do(b.executor(ctx))
}

// writeStorage builds the script that fills one origin's local storage. The
// items are passed as a JSON literal, so no value is ever spliced into code.
func writeStorage(items []Item) (string, error) {
	encoded, err := json.Marshal(items)
	if err != nil {
		return "", err
	}
	return "(items => { for (const item of items) localStorage.setItem(item.name, item.value); })(" + string(encoded) + ")", nil
}

// frameOrigins lists the distinct network origins of a page's frames, in a
// stable order. Frames without one — about:blank, a sandboxed iframe — have no
// storage to read.
func frameOrigins(tree *cdppage.FrameTree) []string {
	var origins []string
	var walk func(*cdppage.FrameTree)
	walk = func(node *cdppage.FrameTree) {
		if origin := node.Frame.SecurityOrigin; networkOrigin(origin) && !slices.Contains(origins, origin) {
			origins = append(origins, origin)
		}
		for _, child := range node.ChildFrames {
			walk(child)
		}
	}
	walk(tree)
	slices.Sort(origins)
	return origins
}

// networkOrigin reports whether an origin owns web storage shots can read or
// write. about:blank, a sandboxed iframe's "null" and file: do not.
func networkOrigin(origin string) bool {
	return strings.HasPrefix(origin, "http://") || strings.HasPrefix(origin, "https://")
}

func exportItems(items []domstorage.Item) []Item {
	out := make([]Item, 0, len(items))
	for _, item := range items {
		if len(item) == 2 {
			out = append(out, Item{Name: item[0], Value: item[1]})
		}
	}
	slices.SortFunc(out, func(a, b Item) int { return strings.Compare(a.Name, b.Name) })
	return out
}

func exportCookies(cookies []*network.Cookie) []Cookie {
	out := make([]Cookie, 0, len(cookies))
	for _, c := range cookies {
		out = append(out, Cookie{
			Name:     c.Name,
			Value:    c.Value,
			Domain:   c.Domain,
			Path:     c.Path,
			Expires:  c.Expires,
			HTTPOnly: c.HTTPOnly,
			Secure:   c.Secure,
			SameSite: string(c.SameSite),
		})
	}
	slices.SortFunc(out, func(a, b Cookie) int {
		return strings.Compare(a.Domain+a.Path+a.Name, b.Domain+b.Path+b.Name)
	})
	return out
}

func restoreCookies(cookies []Cookie) []*network.CookieParam {
	out := make([]*network.CookieParam, 0, len(cookies))
	for _, c := range cookies {
		param := &network.CookieParam{
			Name:     c.Name,
			Value:    c.Value,
			Domain:   c.Domain,
			Path:     c.Path,
			Secure:   c.Secure,
			HTTPOnly: c.HTTPOnly,
			SameSite: network.CookieSameSite(c.SameSite),
		}
		// A session cookie has no expiry; Playwright writes it as -1.
		if c.Expires > 0 {
			expires := cdp.TimeSinceEpoch(time.UnixMilli(int64(c.Expires * 1000)))
			param.Expires = &expires
		}
		out = append(out, param)
	}
	return out
}
