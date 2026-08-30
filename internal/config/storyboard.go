package config

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Storyboard is a parsed video storyboard: the setup a recording needs, and the
// scenes it plays.
type Storyboard struct {
	// BaseDir is the directory relative paths resolve against: the storyboard
	// file's directory, or the current directory for one read from standard
	// input.
	BaseDir string `yaml:"-"`

	// Output is the WebM destination, already resolved against BaseDir. The
	// command's --output overrides it.
	Output string `yaml:"output"`
	// URL is the starting page. It may be empty when the first scene opens one.
	URL string `yaml:"url"`

	// Sh, Python and Server run before the browser opens, in that order.
	Sh     Command `yaml:"sh"`
	Python string  `yaml:"python"`
	Server Command `yaml:"server"`

	Viewport Viewport `yaml:"viewport"`
	Cursor   *Cursor  `yaml:"cursor"`

	// Wait is seconds to pause once the starting page has loaded.
	Wait       float64 `yaml:"wait"`
	WaitFor    string  `yaml:"wait_for"`
	WaitForURL string  `yaml:"wait_for_url"`
	JavaScript string  `yaml:"javascript"`

	Scenes Scenes `yaml:"scenes"`
}

// Viewport is the size the video is recorded at. A zero dimension means the
// default; see [Storyboard.Size].
type Viewport struct {
	Width  int `yaml:"width"`
	Height int `yaml:"height"`
}

// DefaultViewport is the size a storyboard records at when it names neither.
const (
	DefaultViewportWidth  = 1280
	DefaultViewportHeight = 720
)

// Size is the viewport to record at.
func (s *Storyboard) Size() (width, height int) {
	width, height = s.Viewport.Width, s.Viewport.Height
	if width == 0 {
		width = DefaultViewportWidth
	}
	if height == 0 {
		height = DefaultViewportHeight
	}
	return width, height
}

// Cursor configures the pointer and click effects drawn into the page while
// recording. A recording browser has no system cursor of its own.
type Cursor struct {
	Visible   bool   `yaml:"visible"`
	Clicks    bool   `yaml:"clicks"`
	Color     string `yaml:"color"`
	Size      int    `yaml:"size"`
	ClickSize int    `yaml:"click_size"`
}

// Enabled reports whether anything would be drawn. "cursor: false" decodes to a
// cursor with both effects off, which is the same thing as no cursor at all.
func (c *Cursor) Enabled() bool { return c != nil && (c.Visible || c.Clicks) }

// Scene is one segment of a storyboard.
type Scene struct {
	Name string `yaml:"name"`
	// Open navigates at the start of the scene; a relative URL resolves against
	// the page's current one.
	Open       string  `yaml:"open"`
	WaitFor    string  `yaml:"wait_for"`
	WaitForURL string  `yaml:"wait_for_url"`
	Sh         Command `yaml:"sh"`
	Python     string  `yaml:"python"`
	Do         Actions `yaml:"do"`
}

// Scenes and Actions are named slice types so that a failure inside one is
// reported against its index: scenes[1].do[2].click.selector.
type (
	Scenes  []Scene
	Actions []Action
)

// Action is one step of a scene's do list. Every action is one struct rather
// than an interface with thirteen implementations: the executor is a switch on
// Kind either way, and the decoder already knows which keys each kind accepts.
type Action struct {
	// Kind is the action name, with "js" normalized to "javascript".
	Kind string `yaml:"-"`

	// Selector names the element for click, type, fill, press, wait_for and
	// screenshot. "into" is type and fill's spelling of the same key.
	Selector string `yaml:"selector" alias:"into"`
	Button   string `yaml:"button"`
	Count    int    `yaml:"count"`
	Text     string `yaml:"text"`
	// DelayMS is the pause between keystrokes of a type action.
	DelayMS float64 `yaml:"delay_ms"`
	Key     string  `yaml:"key"`

	// X, Y, To and Duration belong to scroll; Duration is in seconds and
	// enables smooth scrolling.
	X        float64 `yaml:"x"`
	Y        float64 `yaml:"y"`
	To       string  `yaml:"to"`
	Duration float64 `yaml:"duration"`

	// Seconds is a pause.
	Seconds float64 `yaml:"seconds"`
	// URL belongs to open and wait_for_url.
	URL string `yaml:"url"`
	// Code is JavaScript or Python source.
	Code string `yaml:"code"`
	// Output is a screenshot's destination, already resolved against
	// [Storyboard].BaseDir.
	Output   string `yaml:"output"`
	FullPage bool   `yaml:"full_page"`
	// Command is a shell step.
	Command Command `yaml:"command"`
}

// actionSpec describes one action: the keys its mapping form accepts, and the
// key its scalar shorthand fills. An empty shorthand means the mapping form is
// the only one.
type actionSpec struct {
	shorthand string
	keys      []string
}

var actionSpecs = map[string]actionSpec{
	"click":        {"selector", []string{"selector", "button", "count"}},
	"type":         {"", []string{"selector", "text", "delay_ms"}},
	"fill":         {"", []string{"selector", "text"}},
	"press":        {"key", []string{"selector", "key"}},
	"scroll":       {"y", []string{"x", "y", "to", "duration"}},
	"pause":        {"seconds", []string{"seconds"}},
	"wait_for":     {"selector", []string{"selector"}},
	"wait_for_url": {"url", []string{"url"}},
	"open":         {"url", []string{"url"}},
	"javascript":   {"code", []string{"code"}},
	"js":           {"code", []string{"code"}},
	"screenshot":   {"output", []string{"output", "selector", "full_page"}},
	"sh":           {"command", []string{"command"}},
	"python":       {"code", []string{"code"}},
}

// LoadStoryboard reads a storyboard file, resolving relative paths against the
// file's directory.
func LoadStoryboard(path string) (*Storyboard, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseStoryboard(data, filepath.Dir(path))
}

// ParseStoryboard parses a storyboard. Relative paths in it resolve against
// baseDir; an empty baseDir leaves them untouched.
func ParseStoryboard(data []byte, baseDir string) (*Storyboard, error) {
	s := &Storyboard{BaseDir: baseDir}
	root, err := document(data)
	if err != nil {
		return nil, err
	}
	if root == nil {
		return nil, &Error{Msg: "storyboard is empty"}
	}
	lines, err := decodeStruct(root, "", s)
	if err != nil {
		return nil, err
	}
	if err := s.validate(root.Line, lines); err != nil {
		return nil, err
	}
	s.resolve()
	return s, nil
}

// document decodes the single YAML document a configuration file holds. A
// second document is refused rather than silently dropped.
func document(data []byte) (*yaml.Node, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	switch err := dec.Decode(&doc); {
	case errors.Is(err, io.EOF):
		return nil, nil
	case err != nil:
		return nil, &Error{Msg: strings.TrimPrefix(err.Error(), "yaml: ")}
	}
	var extra yaml.Node
	switch err := dec.Decode(&extra); {
	case errors.Is(err, io.EOF):
	case err != nil:
		return nil, &Error{Msg: strings.TrimPrefix(err.Error(), "yaml: ")}
	default:
		return nil, errorf(&extra, "", "expected a single YAML document")
	}
	if len(doc.Content) == 0 {
		return nil, nil
	}
	root := resolve(doc.Content[0])
	if root.Kind == yaml.ScalarNode && root.Tag == "!!null" {
		return nil, nil
	}
	return root, nil
}

func (s *Storyboard) validate(line int, lines map[string]int) error {
	c := &check{line: line, lines: lines}
	if c.present("output") {
		c.required("output", s.Output)
	}
	if c.present("sh") && s.Sh.Empty() {
		c.fail("sh", "must not be empty")
	}
	if c.present("python") && strings.TrimSpace(s.Python) == "" {
		c.fail("python", "must not be empty")
	}
	if c.present("server") && s.Server.Empty() {
		c.fail("server", "must not be empty")
	}
	c.nonNegativeFloat("wait", s.Wait)
	if len(s.Scenes) == 0 {
		c.fail("scenes", "must list at least one scene")
	} else if strings.TrimSpace(s.URL) == "" && strings.TrimSpace(s.Scenes[0].Open) == "" {
		c.fail("url", "required unless the first scene opens one")
	}
	return c.result()
}

// resolve rewrites the paths a storyboard names so they mean the same file
// wherever shots was run from, as a multi-shot configuration's do.
func (s *Storyboard) resolve() {
	s.Output = against(s.BaseDir, s.Output)
	for i := range s.Scenes {
		for j := range s.Scenes[i].Do {
			a := &s.Scenes[i].Do[j]
			a.Output = against(s.BaseDir, a.Output)
		}
	}
}

// UnmarshalYAML implements [yaml.Unmarshaler].
func (v *Viewport) UnmarshalYAML(n *yaml.Node) error {
	lines, err := decodeStruct(n, "", v)
	if err != nil {
		return err
	}
	c := &check{line: resolve(n).Line, lines: lines}
	c.positive("width", v.Width)
	c.positive("height", v.Height)
	return c.result()
}

// UnmarshalYAML implements [yaml.Unmarshaler]. "cursor: true" is the default
// cursor, "cursor: false" is one with nothing turned on, and a mapping
// overrides the defaults key by key.
func (c *Cursor) UnmarshalYAML(n *yaml.Node) error {
	*c = Cursor{Visible: true, Clicks: true, Color: "#ff4f00", Size: 18, ClickSize: 44}
	n = resolve(n)
	if n.Kind == yaml.ScalarNode {
		var on bool
		if err := n.Decode(&on); err != nil {
			return errorf(n, "", "expected true, false or a mapping")
		}
		if !on {
			c.Visible, c.Clicks = false, false
		}
		return nil
	}
	lines, err := decodeStruct(n, "", c)
	if err != nil {
		return err
	}
	chk := &check{line: n.Line, lines: lines}
	if c.Color == "" {
		chk.fail("color", "must not be empty")
	}
	chk.positive("size", c.Size)
	chk.positive("click_size", c.ClickSize)
	return chk.result()
}

// UnmarshalYAML implements [yaml.Unmarshaler].
func (s *Scenes) UnmarshalYAML(n *yaml.Node) error { return decodeSlice(n, (*[]Scene)(s)) }

// UnmarshalYAML implements [yaml.Unmarshaler].
func (a *Actions) UnmarshalYAML(n *yaml.Node) error { return decodeSlice(n, (*[]Action)(a)) }

// UnmarshalYAML implements [yaml.Unmarshaler].
func (s *Scene) UnmarshalYAML(n *yaml.Node) error {
	lines, err := decodeStruct(n, "", s)
	if err != nil {
		return err
	}
	c := &check{line: resolve(n).Line, lines: lines}
	if c.present("sh") && s.Sh.Empty() {
		c.fail("sh", "must not be empty")
	}
	if c.present("python") && strings.TrimSpace(s.Python) == "" {
		c.fail("python", "must not be empty")
	}
	return c.result()
}

// UnmarshalYAML implements [yaml.Unmarshaler]. An action is a single-key
// mapping naming the action, whose value is either a mapping of its own keys or
// the scalar shorthand for the one key that matters most.
func (a *Action) UnmarshalYAML(n *yaml.Node) error {
	n = resolve(n)
	if n.Kind != yaml.MappingNode || len(n.Content) != 2 {
		return errorf(n, "", "expected a single-key mapping naming an action")
	}
	key, value := n.Content[0], resolve(n.Content[1])
	name := key.Value
	spec, ok := actionSpecs[name]
	if !ok {
		return errorf(key, name, "unknown action")
	}
	a.Kind = name
	if name == "js" {
		a.Kind = "javascript"
	}

	lines := map[string]int{}
	switch {
	case value.Kind == yaml.MappingNode:
		var err error
		if lines, err = decodeStruct(value, name, a, spec.keys...); err != nil {
			return err
		}
	case spec.shorthand == "":
		return errorf(value, name, "expected a mapping")
	default:
		if err := setField(value, field(name, spec.shorthand), a, spec.shorthand); err != nil {
			return err
		}
		lines[spec.shorthand] = value.Line
	}
	return a.validate(name, key.Line, lines)
}

func (a *Action) validate(name string, line int, lines map[string]int) error {
	c := &check{path: name, line: line, lines: lines}
	// The spelling the file used, so that "into:" is not reported as
	// "selector:".
	selector := c.spelling([]string{"selector", "into"})
	switch a.Kind {
	case "click":
		c.required(selector, a.Selector)
		if c.present("button") && !slices.Contains([]string{"left", "right", "middle"}, a.Button) {
			c.fail("button", "must be left, right or middle")
		}
		c.positive("count", a.Count)
	case "type", "fill":
		c.required(selector, a.Selector)
		if !c.present("text") {
			c.fail("text", "required")
		}
		c.nonNegativeFloat("delay_ms", a.DelayMS)
	case "press":
		c.required("key", a.Key)
	case "scroll":
		c.nonNegativeFloat("duration", a.Duration)
		if c.present("to") {
			c.required("to", a.To)
		}
	case "pause":
		if !c.present("seconds") {
			c.fail("seconds", "required")
		}
		c.nonNegativeFloat("seconds", a.Seconds)
	case "wait_for":
		c.required(selector, a.Selector)
	case "wait_for_url", "open":
		c.required("url", a.URL)
	case "javascript", "python":
		c.required("code", a.Code)
	case "screenshot":
		c.required("output", a.Output)
	case "sh":
		if a.Command.Empty() {
			c.fail("command", "required")
		}
	}
	return c.result()
}
