// Package config decodes shots configuration files.
//
// Decoding goes through [yaml.Node] rather than stock strict unmarshalling, so
// that unknown, duplicate and malformed keys are reported with a field path
// such as "entries[3].server" and the line they appeared on.
package config

import (
	"errors"
	"strings"

	"go.yaml.in/yaml/v3"
)

// StringList is a YAML value that is either one string or a list of strings.
type StringList []string

// UnmarshalYAML implements [yaml.Unmarshaler].
func (s *StringList) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		if n.Tag == "!!null" {
			*s = nil
			return nil
		}
		*s = StringList{n.Value}
		return nil
	}
	var list []string
	if err := n.Decode(&list); err != nil {
		return errors.New("expected a string or a list of strings")
	}
	*s = list
	return nil
}

// Command is an external command: either a single string, run through the
// platform shell, or an argument vector, executed directly.
type Command struct {
	Shell string
	Args  []string
}

// UnmarshalYAML implements [yaml.Unmarshaler].
func (c *Command) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		if n.Tag != "!!null" {
			c.Shell = n.Value
		}
		return nil
	}
	var args []string
	if err := n.Decode(&args); err != nil {
		return errors.New("expected a command string or a list of arguments")
	}
	c.Args = args
	return nil
}

// Empty reports whether the command has nothing to run.
func (c Command) Empty() bool {
	return strings.TrimSpace(c.Shell) == "" && len(c.Args) == 0
}

// Entry is one entry of a multi-shot run. Its parts run in field order: shell
// and server steps first, then a screenshot if URL is set. A single entry can
// prepare a page and capture it.
//
// Every part is optional except that an entry with no step must request a
// screenshot. Empty steps and an empty URL are rejected during decoding, so a
// non-zero value means "present".
//
// Settings such as retina, scale factor, timeout, skip and fail belong to the
// command, not an entry, so they are not fields here.
type Entry struct {
	Sh     Command `yaml:"sh"`
	Server Command `yaml:"server"`

	URL string `yaml:"url"`
	// Output is the destination path, already resolved against
	// [Multi].BaseDir. Empty means an automatic filename.
	Output string `yaml:"output"`

	Width  int `yaml:"width"`
	Height int `yaml:"height"`

	Format         string `yaml:"format"`
	Quality        int    `yaml:"quality"`
	OmitBackground bool   `yaml:"omit_background"`

	// Wait is milliseconds.
	Wait       int    `yaml:"wait"`
	WaitFor    string `yaml:"wait_for"`
	JavaScript string `yaml:"javascript"`
	// JSFile is a path to a JavaScript file, resolved against [Multi].BaseDir,
	// or a "gh:user/repo/path" reference, which is left as written.
	JSFile string `yaml:"js_file"`

	// The singular and plural spellings of each selector key are aliases;
	// values from both accumulate in source order.
	Selectors      StringList `yaml:"selectors" alias:"selector"`
	SelectorsAll   StringList `yaml:"selectors_all" alias:"selector_all"`
	JSSelectors    StringList `yaml:"js_selectors" alias:"js_selector"`
	JSSelectorsAll StringList `yaml:"js_selectors_all" alias:"js_selector_all"`
	Padding        int        `yaml:"padding"`
}
