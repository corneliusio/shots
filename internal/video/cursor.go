package video

import (
	_ "embed"
	"encoding/json"
	"strings"
)

//go:embed cursor.js
var cursorJS string

// Cursor configures the pointer drawn into a recorded page. A browser driven
// over the DevTools Protocol has no cursor of its own, so a recording shows
// things happening with nothing visibly doing them.
type Cursor struct {
	Visible   bool   `json:"visible"`
	Clicks    bool   `json:"clicks"`
	Color     string `json:"color"`
	Size      int    `json:"size"`
	ClickSize int    `json:"clickSize"`
}

// CursorScript is a script to run in every document a recording loads. It draws
// a dot that follows dispatched mouse events and a ring around each click.
func CursorScript(c Cursor) string {
	options, err := json.Marshal(c)
	if err != nil { // the struct is plain data
		return ""
	}
	return strings.Replace(cursorJS, "OPTIONS", string(options), 1)
}
