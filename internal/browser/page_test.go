package browser

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/corneliusio/shots/internal/testsite"
)

func TestScreenshotRestoresExternalOverlays(t *testing.T) {
	path, err := Discover("")
	if err != nil {
		t.Skipf("no browser: %v", err)
	}
	ctx := context.Background()
	b, err := Connect(ctx, Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	bc, err := b.NewContext(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer bc.Close()
	page, err := bc.NewPage(ctx, PageOptions{Width: 1280, Height: 720})
	if err != nil {
		t.Fatal(err)
	}
	defer page.Close()
	srv := testsite.New()
	defer srv.Close()
	if _, err := page.Navigate(ctx, srv.URL+"/fixed-overlap.html"); err != nil {
		t.Fatal(err)
	}
	if err := page.Exec(ctx, `() => document.querySelector('header').style.setProperty('visibility', 'visible', 'important')`); err != nil {
		t.Fatal(err)
	}
	region, err := page.Region(ctx, Selectors{CSS: []string{"#target"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := page.Screenshot(ctx, ShotOptions{Clip: &region}); err != nil {
		t.Fatal(err)
	}

	raw, err := page.Eval(ctx, `() => ({
		fixedVisibility: document.querySelector('header').style.getPropertyValue('visibility'),
		fixedPriority: document.querySelector('header').style.getPropertyPriority('visibility'),
		stickyStyle: document.querySelector('#sticky-overlay').getAttribute('style'),
		hasSelectedState: Object.hasOwn(globalThis, Symbol.for('github.com/corneliusio/shots.selected-elements')),
		hasRestoreState: Object.hasOwn(globalThis, Symbol.for('github.com/corneliusio/shots.restore-overlays')),
	})`)
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		FixedVisibility  string  `json:"fixedVisibility"`
		FixedPriority    string  `json:"fixedPriority"`
		StickyStyle      *string `json:"stickyStyle"`
		HasSelectedState bool    `json:"hasSelectedState"`
		HasRestoreState  bool    `json:"hasRestoreState"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if state.FixedVisibility != "visible" || state.FixedPriority != "important" {
		t.Errorf("fixed visibility after capture = %q %q, want visible important", state.FixedVisibility, state.FixedPriority)
	}
	if state.StickyStyle != nil {
		t.Errorf("sticky style after capture = %v, want absent", state.StickyStyle)
	}
	if state.HasSelectedState || state.HasRestoreState {
		t.Errorf("temporary screenshot state remains: selected=%v restore=%v", state.HasSelectedState, state.HasRestoreState)
	}
}
