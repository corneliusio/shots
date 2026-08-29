package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Multi is a parsed multi-shot configuration.
type Multi struct {
	// BaseDir is the directory that relative paths resolve against: the
	// configuration file's directory, or the current directory for
	// configuration read from standard input.
	BaseDir string
	Entries []Entry
}

// LoadMulti reads a multi-shot configuration file, resolving relative paths
// against the file's directory.
func LoadMulti(path string) (*Multi, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseMulti(data, filepath.Dir(path))
}

// ParseMulti parses a multi-shot configuration. Relative paths in it resolve
// against baseDir; an empty baseDir leaves them untouched. An empty document is
// a valid configuration with no entries.
func ParseMulti(data []byte, baseDir string) (*Multi, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, &Error{Msg: strings.TrimPrefix(err.Error(), "yaml: ")}
	}
	m := &Multi{BaseDir: baseDir}
	if len(doc.Content) == 0 {
		return m, nil
	}
	root := resolve(doc.Content[0])
	if root.Kind == yaml.ScalarNode && root.Tag == "!!null" {
		return m, nil
	}
	if root.Kind != yaml.SequenceNode {
		return nil, errorf(root, "", "expected a list of entries, got %s", kindOf(root))
	}
	for i, n := range root.Content {
		e, err := decodeEntry(n, index("entries", i), baseDir)
		if err != nil {
			return nil, err
		}
		m.Entries = append(m.Entries, e)
	}
	return m, nil
}

// steps are the keys that discriminate a non-screenshot entry.
var steps = []string{"sh", "python", "server"}

func decodeEntry(n *yaml.Node, path, baseDir string) (Entry, error) {
	n = resolve(n)
	if n.Kind != yaml.MappingNode {
		return nil, errorf(n, path, "expected a mapping, got %s", kindOf(n))
	}
	// Repeated spellings of one step are deduplicated here so that they reach
	// decodeStruct and are reported as a duplicate field, not as a combination.
	var found []string
	for i := 0; i+1 < len(n.Content); i += 2 {
		if step := n.Content[i].Value; slices.Contains(steps, step) && !slices.Contains(found, step) {
			found = append(found, step)
		}
	}
	if len(found) > 1 {
		return nil, errorf(n, path, "%s cannot be combined in one entry", strings.Join(found, " and "))
	}
	if len(found) == 0 {
		return decodeShot(n, path, baseDir)
	}

	switch found[0] {
	case "sh":
		var e struct {
			Sh Command `yaml:"sh"`
		}
		lines, err := decodeStruct(n, path, &e)
		if err != nil {
			return nil, err
		}
		if err := requireCommand(e.Sh, path, "sh", lines); err != nil {
			return nil, err
		}
		return &Sh{e.Sh}, nil
	case "server":
		var e struct {
			Server Command `yaml:"server"`
		}
		lines, err := decodeStruct(n, path, &e)
		if err != nil {
			return nil, err
		}
		if err := requireCommand(e.Server, path, "server", lines); err != nil {
			return nil, err
		}
		return &Server{e.Server}, nil
	default:
		var e struct {
			Python string `yaml:"python"`
		}
		lines, err := decodeStruct(n, path, &e)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(e.Python) == "" {
			return nil, &Error{Path: field(path, "python"), Line: lines["python"], Msg: "must not be empty"}
		}
		return &Python{Code: e.Python}, nil
	}
}

func requireCommand(c Command, path, name string, lines map[string]int) error {
	if c.Empty() {
		return &Error{Path: field(path, name), Line: lines[name], Msg: "must not be empty"}
	}
	return nil
}

func decodeShot(n *yaml.Node, path, baseDir string) (Entry, error) {
	var s Shot
	lines, err := decodeStruct(n, path, &s)
	if err != nil {
		return nil, err
	}
	if err := s.validate(path, n.Line, lines); err != nil {
		return nil, err
	}
	if s.Output != "" && s.Output != "-" && !filepath.IsAbs(s.Output) && baseDir != "" {
		s.Output = filepath.Join(baseDir, s.Output)
	}
	return &s, nil
}

func (s *Shot) validate(path string, line int, lines map[string]int) error {
	c := &check{path: path, line: line, lines: lines}
	if strings.TrimSpace(s.URL) == "" {
		c.fail("url", "required")
	}
	if c.present("output") && strings.TrimSpace(s.Output) == "" {
		c.fail("output", "must not be empty")
	}
	c.positive("width", s.Width)
	c.positive("height", s.Height)
	c.positive("timeout", s.Timeout)
	c.nonNegative("wait", s.Wait)
	c.nonNegative("padding", s.Padding)
	if c.present("scale_factor") {
		if s.ScaleFactor <= 0 {
			c.fail("scale_factor", "must be positive")
		}
		if c.present("retina") {
			c.fail("scale_factor", "cannot be combined with retina")
		}
	}
	if c.present("quality") && (s.Quality < 1 || s.Quality > 100) {
		c.fail("quality", "must be between 1 and 100")
	}
	if s.Skip && s.Fail {
		c.fail("fail", "cannot be combined with skip")
	}
	for _, group := range []struct {
		names     []string
		selectors StringList
	}{
		{[]string{"selectors", "selector"}, s.Selectors},
		{[]string{"selectors_all", "selector_all"}, s.SelectorsAll},
		{[]string{"js_selectors", "js_selector"}, s.JSSelectors},
		{[]string{"js_selectors_all", "js_selector_all"}, s.JSSelectorsAll},
	} {
		for _, selector := range group.selectors {
			if strings.TrimSpace(selector) == "" {
				c.fail(c.spelling(group.names), "must not contain an empty selector")
			}
		}
	}
	if c.err != nil {
		return c.err
	}
	return nil
}

// check collects the first validation failure for one mapping, reporting it
// against the line the offending key appeared on.
type check struct {
	path  string
	line  int
	lines map[string]int
	err   *Error
}

func (c *check) present(name string) bool {
	_, ok := c.lines[name]
	return ok
}

// spelling returns whichever of a key's accepted spellings was used.
func (c *check) spelling(names []string) string {
	for _, name := range names {
		if c.present(name) {
			return name
		}
	}
	return names[0]
}

func (c *check) fail(name, format string, args ...any) {
	if c.err != nil {
		return
	}
	line := c.line
	if l, ok := c.lines[name]; ok {
		line = l
	}
	c.err = &Error{Path: field(c.path, name), Line: line, Msg: fmt.Sprintf(format, args...)}
}

func (c *check) positive(name string, value int) {
	if c.present(name) && value <= 0 {
		c.fail(name, "must be positive")
	}
}

func (c *check) nonNegative(name string, value int) {
	if c.present(name) && value < 0 {
		c.fail(name, "must not be negative")
	}
}
