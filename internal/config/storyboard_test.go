package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// valid is the smallest storyboard that parses, with room to append keys.
const valid = `output: demo.webm
url: http://localhost:8000/
scenes:
- name: One
  do:
  - pause: 1
`

func TestParseStoryboard(t *testing.T) {
	s, err := ParseStoryboard([]byte(`output: demo.webm
url: http://localhost:8000/
sh: echo hi
python: print("hi")
server: [python, -m, http.server]
viewport:
  width: 800
  height: 600
cursor: true
wait: 0.5
wait_for: h1
wait_for_url: "**/done"
javascript: window.ready = true
scenes:
- name: Intro
  open: /a.html
  wait_for: h1
  sh: [touch, x]
  do:
  - click: "#go"
  - type:
      into: "#q"
      text: hello
      delay_ms: 25
  - press:
      selector: "#q"
      key: Enter
  - scroll: 800
  - scroll: {to: "#end", duration: 1.5}
  - pause: 0.25
  - wait_for: .done
  - wait_for_url: "**/done"
  - open: /b.html
  - js: window.x = 1
  - javascript: window.y = 2
  - screenshot: step.png
  - screenshot: {output: el.png, selector: h1, full_page: true}
  - sh: echo scene
  - python: print(1)
`), "/base")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := s.Output, filepath.Join("/base", "demo.webm"); got != want {
		t.Errorf("Output = %q, want %q", got, want)
	}
	if w, h := s.Size(); w != 800 || h != 600 {
		t.Errorf("Size() = %dx%d, want 800x600", w, h)
	}
	if !s.Cursor.Enabled() || s.Cursor.Color != "#ff4f00" || s.Cursor.Size != 18 || s.Cursor.ClickSize != 44 {
		t.Errorf("Cursor = %+v, want the defaults", s.Cursor)
	}
	if s.Sh.Shell != "echo hi" || len(s.Server.Args) != 3 {
		t.Errorf("steps = %+v %+v", s.Sh, s.Server)
	}
	if len(s.Scenes) != 1 {
		t.Fatalf("Scenes = %d, want 1", len(s.Scenes))
	}
	scene := s.Scenes[0]
	if scene.Name != "Intro" || scene.Open != "/a.html" || scene.WaitFor != "h1" || len(scene.Sh.Args) != 2 {
		t.Errorf("scene = %+v", scene)
	}
	kinds := make([]string, len(scene.Do))
	for i, a := range scene.Do {
		kinds[i] = a.Kind
	}
	want := "click type press scroll scroll pause wait_for wait_for_url open javascript javascript screenshot screenshot sh python"
	if got := strings.Join(kinds, " "); got != want {
		t.Errorf("kinds = %q, want %q", got, want)
	}
	do := scene.Do
	if do[0].Selector != "#go" {
		t.Errorf("click selector = %q", do[0].Selector)
	}
	if do[1].Selector != "#q" || do[1].Text != "hello" || do[1].DelayMS != 25 {
		t.Errorf("type = %+v", do[1])
	}
	if do[2].Key != "Enter" || do[2].Selector != "#q" {
		t.Errorf("press = %+v", do[2])
	}
	if do[3].Y != 800 || do[3].X != 0 {
		t.Errorf("scroll = %+v", do[3])
	}
	if do[4].To != "#end" || do[4].Duration != 1.5 {
		t.Errorf("scroll to = %+v", do[4])
	}
	if do[5].Seconds != 0.25 {
		t.Errorf("pause = %+v", do[5])
	}
	if do[8].URL != "/b.html" {
		t.Errorf("open = %+v", do[8])
	}
	if do[9].Code != "window.x = 1" || do[10].Code != "window.y = 2" {
		t.Errorf("javascript = %+v %+v", do[9], do[10])
	}
	if got, want := do[11].Output, filepath.Join("/base", "step.png"); got != want {
		t.Errorf("screenshot output = %q, want %q", got, want)
	}
	if do[12].Selector != "h1" || !do[12].FullPage {
		t.Errorf("screenshot = %+v", do[12])
	}
	if do[13].Command.Shell != "echo scene" || do[14].Code != "print(1)" {
		t.Errorf("steps = %+v %+v", do[13], do[14])
	}
}

func TestStoryboardDefaults(t *testing.T) {
	s, err := ParseStoryboard([]byte(valid), "")
	if err != nil {
		t.Fatal(err)
	}
	if w, h := s.Size(); w != DefaultViewportWidth || h != DefaultViewportHeight {
		t.Errorf("Size() = %dx%d, want %dx%d", w, h, DefaultViewportWidth, DefaultViewportHeight)
	}
	if s.Cursor.Enabled() {
		t.Errorf("Cursor = %+v, want none", s.Cursor)
	}
	if s.Output != "demo.webm" {
		t.Errorf("Output = %q, want it left relative", s.Output)
	}
}

func TestStoryboardCursor(t *testing.T) {
	for _, tt := range []struct {
		name    string
		yaml    string
		enabled bool
		color   string
	}{
		{"absent", "", false, ""},
		{"true", "cursor: true\n", true, "#ff4f00"},
		{"false", "cursor: false\n", false, "#ff4f00"},
		{"mapping", "cursor:\n  color: red\n  visible: false\n", true, "red"},
		{"all off", "cursor:\n  visible: false\n  clicks: false\n", false, "#ff4f00"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, err := ParseStoryboard([]byte(tt.yaml+valid), "")
			if err != nil {
				t.Fatal(err)
			}
			if got := s.Cursor.Enabled(); got != tt.enabled {
				t.Errorf("Enabled() = %v, want %v", got, tt.enabled)
			}
			if tt.color != "" && s.Cursor.Color != tt.color {
				t.Errorf("Color = %q, want %q", s.Cursor.Color, tt.color)
			}
		})
	}
}

func TestParseStoryboardErrors(t *testing.T) {
	for _, tt := range []struct {
		name string
		yaml string
		want string
	}{
		{"empty", "", "storyboard is empty"},
		{"not a mapping", "- a\n", "line 1: expected a mapping, got a list"},
		{"unknown key", valid + "nope: 1\n", "nope (line 7): unknown field"},
		{"no scenes", "output: a.webm\nurl: http://x/\nscenes: []\n", "scenes (line 3): must list at least one scene"},
		{"no url", "output: a.webm\nscenes:\n- do: []\n", "url (line 1): required unless the first scene opens one"},
		{"scene url ok", "output: a.webm\nscenes:\n- open: http://x/\n", ""},
		{"bad viewport", valid + "viewport:\n  width: 0\n", "viewport.width (line 8): must be positive"},
		{"unknown viewport key", valid + "viewport:\n  depth: 3\n", "viewport.depth (line 8): unknown field"},
		{"bad cursor", valid + "cursor:\n  size: -1\n", "cursor.size (line 8): must be positive"},
		{"cursor scalar", valid + "cursor: purple\n", "cursor (line 7): expected true, false or a mapping"},
		{"negative wait", valid + "wait: -1\n", "wait (line 7): must not be negative"},
		{"scenes not a list", "output: a.webm\nurl: http://x/\nscenes: {}\n", "scenes (line 3): expected a list, got a mapping"},
		{"unknown scene key", "output: a.webm\nurl: http://x/\nscenes:\n- nope: 1\n", "scenes[0].nope (line 4): unknown field"},
		{"do not a list", "output: a.webm\nurl: http://x/\nscenes:\n- do: 3\n", "scenes[0].do (line 4): expected a list, got a value"},
		{
			"unknown action",
			"output: a.webm\nurl: http://x/\nscenes:\n- do:\n  - dance: now\n",
			"scenes[0].do[0].dance (line 5): unknown action",
		},
		{
			"two keys",
			"output: a.webm\nurl: http://x/\nscenes:\n- do:\n  - click: a\n    pause: 1\n",
			"scenes[0].do[0] (line 5): expected a single-key mapping naming an action",
		},
		{
			"unknown action key",
			"output: a.webm\nurl: http://x/\nscenes:\n- do:\n  - click: {key: Enter}\n",
			"scenes[0].do[0].click.key (line 5): unknown field",
		},
		{
			"missing selector",
			"output: a.webm\nurl: http://x/\nscenes:\n- do:\n  - click: \"\"\n",
			"scenes[0].do[0].click.selector (line 5): required",
		},
		{
			"into is reported as into",
			"output: a.webm\nurl: http://x/\nscenes:\n- do:\n  - type: {into: \"\", text: x}\n",
			"scenes[0].do[0].type.into (line 5): required",
		},
		{
			"type needs a mapping",
			"output: a.webm\nurl: http://x/\nscenes:\n- do:\n  - type: hello\n",
			"scenes[0].do[0].type (line 5): expected a mapping",
		},
		{
			"type needs text",
			"output: a.webm\nurl: http://x/\nscenes:\n- do:\n  - type: {into: \"#q\"}\n",
			"scenes[0].do[0].type.text (line 5): required",
		},
		{
			"bad button",
			"output: a.webm\nurl: http://x/\nscenes:\n- do:\n  - click: {selector: a, button: sideways}\n",
			"scenes[0].do[0].click.button (line 5): must be left, right or middle",
		},
		{
			"negative pause",
			"output: a.webm\nurl: http://x/\nscenes:\n- do:\n  - pause: -1\n",
			"scenes[0].do[0].pause.seconds (line 5): must not be negative",
		},
		{
			"pause is not a string",
			"output: a.webm\nurl: http://x/\nscenes:\n- do:\n  - pause: soon\n",
			"scenes[0].do[0].pause.seconds (line 5): cannot unmarshal !!str `soon` into float64",
		},
		{
			"screenshot needs an output",
			"output: a.webm\nurl: http://x/\nscenes:\n- do:\n  - screenshot: {selector: h1}\n",
			"scenes[0].do[0].screenshot.output (line 5): required",
		},
		{
			"sh needs a command",
			"output: a.webm\nurl: http://x/\nscenes:\n- do:\n  - sh: \"\"\n",
			"scenes[0].do[0].sh.command (line 5): required",
		},
		{"two documents", valid + "---\nx: 1\n", "line 7: expected a single YAML document"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseStoryboard([]byte(tt.yaml), "")
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.want == "":
				return
			case err == nil:
				t.Fatalf("no error, want %q", tt.want)
			case err.Error() != tt.want:
				t.Errorf("error = %q, want %q", err, tt.want)
			}
		})
	}
}
