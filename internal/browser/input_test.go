package browser

import (
	"runtime"
	"testing"

	"github.com/chromedp/cdproto/input"
)

func TestGlob(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		url     string
		want    bool
	}{
		{"exact", "http://x/a", "http://x/a", true},
		{"exact is whole", "http://x/a", "http://x/ab", false},
		{"star stops at a slash", "http://x/*", "http://x/a", true},
		{"star really stops at a slash", "http://x/*", "http://x/a/b", false},
		{"double star crosses slashes", "**/dashboard", "http://x/a/b/dashboard", true},
		{"double star at the end", "http://x/**", "http://x/a/b", true},
		{"question mark is one character", "http://x/?", "http://x/a", true},
		{"question mark is not two", "http://x/?", "http://x/ab", false},
		{"a fragment is literal", "**#searched", "http://x/a.html#searched", true},
		{"dots are not wildcards", "http://x/a.b", "http://x/axb", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			re, err := glob(tt.pattern)
			if err != nil {
				t.Fatal(err)
			}
			if got := re.MatchString(tt.url); got != tt.want {
				t.Errorf("glob(%q).Match(%q) = %v, want %v", tt.pattern, tt.url, got, tt.want)
			}
		})
	}
}

func TestEncodeKey(t *testing.T) {
	shortcut := input.ModifierCtrl
	if runtime.GOOS == "darwin" {
		shortcut = input.ModifierCommand
	}
	tests := []struct {
		name      string
		key       string
		wantKey   string
		modifiers input.Modifier
		// text reports whether a character event carrying text is sent.
		text bool
	}{
		{"a named key", "Enter", "Enter", 0, true},
		{"an arrow", "ArrowDown", "ArrowDown", 0, false},
		{"a single character", "a", "a", 0, true},
		{"space", "Space", " ", 0, true},
		{"a modifier suppresses the text", "Control+a", "a", input.ModifierCtrl, false},
		{"the portable modifier", "ControlOrMeta+a", "a", shortcut, false},
		{"several modifiers", "Control+Shift+Tab", "Tab", input.ModifierCtrl | input.ModifierShift, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events, err := encodeKey(tt.key)
			if err != nil {
				t.Fatal(err)
			}
			if len(events) == 0 {
				t.Fatal("no events")
			}
			if events[0].Key != tt.wantKey {
				t.Errorf("key = %q, want %q", events[0].Key, tt.wantKey)
			}
			if events[0].Modifiers&tt.modifiers != tt.modifiers {
				t.Errorf("modifiers = %d, want %d set", events[0].Modifiers, tt.modifiers)
			}
			var text bool
			for _, ev := range events {
				if ev.Type == input.KeyChar {
					text = true
				}
			}
			if text != tt.text {
				t.Errorf("a character event was sent = %v, want %v", text, tt.text)
			}
			if events[len(events)-1].Type != input.KeyUp {
				t.Errorf("the last event is %s, want keyUp", events[len(events)-1].Type)
			}
		})
	}
}

func TestEncodeKeyErrors(t *testing.T) {
	for _, key := range []string{"Nonesuch", "Hyper+a", "Control+Nonesuch"} {
		if _, err := encodeKey(key); err == nil {
			t.Errorf("encodeKey(%q) succeeded, want an error", key)
		}
	}
}
