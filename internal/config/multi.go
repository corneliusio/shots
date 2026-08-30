package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	m := &Multi{BaseDir: baseDir}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	switch err := dec.Decode(&doc); {
	case errors.Is(err, io.EOF):
		return m, nil
	case err != nil:
		return nil, &Error{Msg: strings.TrimPrefix(err.Error(), "yaml: ")}
	}
	// A file may hold several YAML documents; taking only the first would drop
	// entries silently.
	var extra yaml.Node
	switch err := dec.Decode(&extra); {
	case errors.Is(err, io.EOF):
	case err != nil:
		return nil, &Error{Msg: strings.TrimPrefix(err.Error(), "yaml: ")}
	default:
		return nil, errorf(&extra, "", "expected a single YAML document")
	}

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

// Resolve resolves a path given outside the configuration — an --output filter
// naming an entry's output — the way the configuration's own paths resolve, so
// that both spell the same file the same way.
func (m *Multi) Resolve(path string) string {
	if path == "-" { // standard output, not a path
		return path
	}
	return against(m.BaseDir, path)
}

func decodeEntry(n *yaml.Node, path, baseDir string) (Entry, error) {
	var e Entry
	lines, err := decodeStruct(n, path, &e)
	if err != nil {
		return e, err
	}
	if err := e.validate(path, resolve(n).Line, lines); err != nil {
		return e, err
	}
	if e.Output != "-" { // "-" is standard output, not a path
		e.Output = against(baseDir, e.Output)
	}
	if !strings.HasPrefix(e.JSFile, "gh:") {
		e.JSFile = against(baseDir, e.JSFile)
	}
	return e, nil
}

// against resolves a relative path against baseDir. An empty path, an empty
// baseDir and an absolute path are all left alone.
func against(baseDir, path string) string {
	if path == "" || baseDir == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(baseDir, path)
}

func (e *Entry) validate(path string, line int, lines map[string]int) error {
	c := &check{path: path, line: line, lines: lines}
	if c.present("sh") && e.Sh.Empty() {
		c.fail("sh", "must not be empty")
	}
	if c.present("python") && strings.TrimSpace(e.Python) == "" {
		c.fail("python", "must not be empty")
	}
	if c.present("server") && e.Server.Empty() {
		c.fail("server", "must not be empty")
	}
	// A step-only entry needs no URL; anything else must ask for a screenshot.
	step := c.present("sh") || c.present("python") || c.present("server")
	if (!step || c.present("url")) && strings.TrimSpace(e.URL) == "" {
		c.fail("url", "required")
	}
	if c.present("output") && strings.TrimSpace(e.Output) == "" {
		c.fail("output", "must not be empty")
	}
	c.positive("width", e.Width)
	c.positive("height", e.Height)
	c.nonNegative("wait", e.Wait)
	c.nonNegative("padding", e.Padding)
	if c.present("quality") && (e.Quality < 1 || e.Quality > 100) {
		c.fail("quality", "must be between 1 and 100")
	}
	if c.present("javascript") && c.present("js_file") {
		c.fail("js_file", "cannot be combined with javascript")
	}
	for _, group := range []struct {
		names     []string
		selectors StringList
	}{
		{[]string{"selectors", "selector"}, e.Selectors},
		{[]string{"selectors_all", "selector_all"}, e.SelectorsAll},
		{[]string{"js_selectors", "js_selector"}, e.JSSelectors},
		{[]string{"js_selectors_all", "js_selector_all"}, e.JSSelectorsAll},
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
