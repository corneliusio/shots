package browser

import (
	"context"
	_ "embed"
)

//go:embed stable.js
var stableScript string

// Stabilize removes visual motion, waits for fonts and images that the page
// has already discovered, then lets Chrome paint two frames.
func (p *Page) Stabilize(ctx context.Context) error {
	return p.Exec(ctx, stableScript)
}
