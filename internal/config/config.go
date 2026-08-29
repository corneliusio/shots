// Package config decodes shots configuration files.
//
// Multi-shot entries are a union type — a screenshot request, or a `sh:`,
// `python:` or `server:` step — so decoding goes through [yaml.Node]: each
// entry is classified by its discriminating key, decoded strictly into the
// matching struct, and reported with a field path such as "entries[3].server".
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

// Entry is one step of a multi-shot run: a [*Shot], [*Sh], [*Python] or
// [*Server].
type Entry interface{ entry() }

// Shot is a screenshot request.
type Shot struct {
	URL string `yaml:"url"`
	// Output is the destination path, already resolved against
	// [Multi].BaseDir. Empty means an automatic filename.
	Output string `yaml:"output"`

	Width  int `yaml:"width"`
	Height int `yaml:"height"`

	Quality        int     `yaml:"quality"`
	ScaleFactor    float64 `yaml:"scale_factor"`
	Retina         bool    `yaml:"retina"`
	OmitBackground bool    `yaml:"omit_background"`

	// Wait and Timeout are milliseconds.
	Wait       int    `yaml:"wait"`
	WaitFor    string `yaml:"wait_for"`
	Timeout    int    `yaml:"timeout"`
	JavaScript string `yaml:"javascript"`

	// The singular and plural spellings of each selector key are aliases;
	// values from both accumulate in source order.
	Selectors      StringList `yaml:"selectors" alias:"selector"`
	SelectorsAll   StringList `yaml:"selectors_all" alias:"selector_all"`
	JSSelectors    StringList `yaml:"js_selectors" alias:"js_selector"`
	JSSelectorsAll StringList `yaml:"js_selectors_all" alias:"js_selector_all"`
	Padding        int        `yaml:"padding"`

	Skip bool `yaml:"skip"`
	Fail bool `yaml:"fail"`
}

// Sh runs a shell command.
type Sh struct{ Command }

// Python runs a Python script.
type Python struct{ Code string }

// Server starts a background server for the rest of the run.
type Server struct{ Command }

func (*Shot) entry()   {}
func (*Sh) entry()     {}
func (*Python) entry() {}
func (*Server) entry() {}
