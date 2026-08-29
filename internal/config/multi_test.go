package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mostlyserious/shots/internal/config"
)

func parse(t *testing.T, yaml string) *config.Multi {
	t.Helper()
	m, err := config.ParseMulti([]byte(yaml), "")
	if err != nil {
		t.Fatalf("ParseMulti: %v", err)
	}
	return m
}

func parseErr(t *testing.T, yaml string) *config.Error {
	t.Helper()
	m, err := config.ParseMulti([]byte(yaml), "")
	if err == nil {
		t.Fatalf("ParseMulti: expected an error, got %+v", m)
	}
	var cfgErr *config.Error
	if !errors.As(err, &cfgErr) {
		t.Fatalf("ParseMulti: error %v (%T) is not a *config.Error", err, err)
	}
	return cfgErr
}

func TestParseEmpty(t *testing.T) {
	for _, in := range []string{"", "\n", "# just a comment\n", "---\n", "null\n", "[]\n"} {
		m := parse(t, in)
		if len(m.Entries) != 0 {
			t.Errorf("ParseMulti(%q) = %d entries, want none", in, len(m.Entries))
		}
	}
}

func TestParseShot(t *testing.T) {
	m := parse(t, `
- url: https://example.com/
  output: example.png
  width: 800
  height: 600
  quality: 80
  wait: 500
  wait_for: document.title
  timeout: 10000
  javascript: document.body.classList.add('ready')
  selector: "#main"
  selectors:
    - .a
    - .b
  selector_all: .item
  js_selector: div
  js_selectors_all: [span]
  padding: 10
  retina: true
  omit_background: true
  skip: true
`)
	if len(m.Entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(m.Entries))
	}
	shot, ok := m.Entries[0].(*config.Shot)
	if !ok {
		t.Fatalf("entry is %T, want *config.Shot", m.Entries[0])
	}
	want := &config.Shot{
		URL:        "https://example.com/",
		Output:     "example.png",
		Width:      800,
		Height:     600,
		Quality:    80,
		Wait:       500,
		WaitFor:    "document.title",
		Timeout:    10000,
		JavaScript: "document.body.classList.add('ready')",
		// selector and selectors are aliases; values accumulate in source order.
		Selectors:      config.StringList{"#main", ".a", ".b"},
		SelectorsAll:   config.StringList{".item"},
		JSSelectors:    config.StringList{"div"},
		JSSelectorsAll: config.StringList{"span"},
		Padding:        10,
		Retina:         true,
		OmitBackground: true,
		Skip:           true,
	}
	if !reflect.DeepEqual(shot, want) {
		t.Errorf("shot =\n%+v\nwant\n%+v", shot, want)
	}
}

func TestParseSteps(t *testing.T) {
	m := parse(t, `
- sh: echo hello
- sh: [echo, hello]
- python: |
    print("hi")
- server: python -m http.server 8000
- server: [python, -m, http.server]
- url: https://example.com/
`)
	if len(m.Entries) != 6 {
		t.Fatalf("got %d entries, want 6", len(m.Entries))
	}
	sh, ok := m.Entries[0].(*config.Sh)
	if !ok || sh.Shell != "echo hello" || sh.Args != nil {
		t.Errorf("entries[0] = %+v, want a shell string step", m.Entries[0])
	}
	sh, ok = m.Entries[1].(*config.Sh)
	if !ok || sh.Shell != "" || !reflect.DeepEqual(sh.Args, []string{"echo", "hello"}) {
		t.Errorf("entries[1] = %+v, want an argument vector step", m.Entries[1])
	}
	py, ok := m.Entries[2].(*config.Python)
	if !ok || py.Code != "print(\"hi\")\n" {
		t.Errorf("entries[2] = %+v, want a python step", m.Entries[2])
	}
	srv, ok := m.Entries[3].(*config.Server)
	if !ok || srv.Shell != "python -m http.server 8000" {
		t.Errorf("entries[3] = %+v, want a server step", m.Entries[3])
	}
	srv, ok = m.Entries[4].(*config.Server)
	if !ok || !reflect.DeepEqual(srv.Args, []string{"python", "-m", "http.server"}) {
		t.Errorf("entries[4] = %+v, want a server argument vector", m.Entries[4])
	}
	if _, ok := m.Entries[5].(*config.Shot); !ok {
		t.Errorf("entries[5] = %T, want *config.Shot", m.Entries[5])
	}
}

func TestParseEntryOrderIsPreserved(t *testing.T) {
	m := parse(t, "- server: serve\n- url: http://localhost/\n- sh: cleanup\n")
	var kinds []string
	for _, e := range m.Entries {
		kinds = append(kinds, strings.TrimPrefix(reflect.TypeOf(e).String(), "*config."))
	}
	if want := []string{"Server", "Shot", "Sh"}; !reflect.DeepEqual(kinds, want) {
		t.Errorf("kinds = %v, want %v", kinds, want)
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		path string
		line int
		msg  string
	}{
		{
			name: "unknown field",
			yaml: "- url: https://example.com/\n- url: https://example.com/\n  widht: 100\n",
			path: "entries[1].widht",
			line: 3,
			msg:  "unknown field",
		},
		{
			name: "unknown field in a step",
			yaml: "- sh: echo hi\n  output: out.txt\n",
			path: "entries[0].output",
			line: 2,
			msg:  "unknown field",
		},
		{
			name: "merge key",
			yaml: "- &base\n  url: https://example.com/\n- <<: *base\n  width: 400\n",
			path: "entries[1]",
			line: 3,
			msg:  "merge keys are not supported; alias the whole entry instead",
		},
		{
			name: "duplicate field",
			yaml: "- url: https://example.com/\n  url: https://example.org/\n",
			path: "entries[0].url",
			line: 2,
			msg:  "duplicate field",
		},
		{
			name: "combined steps",
			yaml: "- sh: echo hi\n  server: serve\n",
			path: "entries[0]",
			line: 1,
			msg:  "sh and server cannot be combined in one entry",
		},
		{
			name: "entry is not a mapping",
			yaml: "- https://example.com/\n",
			path: "entries[0]",
			line: 1,
			msg:  "expected a mapping, got a value",
		},
		{
			name: "empty entry",
			yaml: "-\n",
			path: "entries[0]",
			line: 1,
			msg:  "expected a mapping, got null",
		},
		{
			name: "root is a mapping",
			yaml: "url: https://example.com/\n",
			path: "",
			line: 1,
			msg:  "expected a list of entries, got a mapping",
		},
		{
			name: "wrong scalar type",
			yaml: "- url: https://example.com/\n  width: wide\n",
			path: "entries[0].width",
			line: 2,
			msg:  "cannot unmarshal !!str `wide` into int",
		},
		{
			name: "selector is not a string",
			yaml: "- url: https://example.com/\n  selectors:\n    a: b\n",
			path: "entries[0].selectors",
			line: 3,
			msg:  "expected a string or a list of strings",
		},
		{
			name: "sh is not a command",
			yaml: "- sh:\n    a: b\n",
			path: "entries[0].sh",
			line: 2,
			msg:  "expected a command string or a list of arguments",
		},
		{
			name: "empty sh",
			yaml: "- sh: \"\"\n",
			path: "entries[0].sh",
			line: 1,
			msg:  "must not be empty",
		},
		{
			name: "null sh",
			yaml: "- sh:\n",
			path: "entries[0].sh",
			line: 1,
			msg:  "must not be empty",
		},
		{
			name: "sh with no arguments",
			yaml: "- sh: []\n",
			path: "entries[0].sh",
			line: 1,
			msg:  "must not be empty",
		},
		{
			name: "empty server",
			yaml: "- server: \"   \"\n",
			path: "entries[0].server",
			line: 1,
			msg:  "must not be empty",
		},
		{
			// A repeated step key is a duplicate field, not a combination of
			// two different steps.
			name: "duplicate step",
			yaml: "- sh: a\n  sh: b\n",
			path: "entries[0].sh",
			line: 2,
			msg:  "duplicate field",
		},
		{
			name: "null python",
			yaml: "- python:\n",
			path: "entries[0].python",
			line: 1,
			msg:  "must not be empty",
		},
		{
			name: "missing url",
			yaml: "- output: out.png\n",
			path: "entries[0].url",
			line: 1,
			msg:  "required",
		},
		{
			name: "blank url",
			yaml: "- url: \"  \"\n",
			path: "entries[0].url",
			line: 1,
			msg:  "required",
		},
		{
			name: "empty output",
			yaml: "- url: https://example.com/\n  output: \"\"\n",
			path: "entries[0].output",
			line: 2,
			msg:  "must not be empty",
		},
		{
			name: "zero width",
			yaml: "- url: https://example.com/\n  width: 0\n",
			path: "entries[0].width",
			line: 2,
			msg:  "must be positive",
		},
		{
			name: "negative height",
			yaml: "- url: https://example.com/\n  height: -1\n",
			path: "entries[0].height",
			line: 2,
			msg:  "must be positive",
		},
		{
			name: "negative wait",
			yaml: "- url: https://example.com/\n  wait: -5\n",
			path: "entries[0].wait",
			line: 2,
			msg:  "must not be negative",
		},
		{
			name: "negative padding",
			yaml: "- url: https://example.com/\n  padding: -5\n",
			path: "entries[0].padding",
			line: 2,
			msg:  "must not be negative",
		},
		{
			name: "zero timeout",
			yaml: "- url: https://example.com/\n  timeout: 0\n",
			path: "entries[0].timeout",
			line: 2,
			msg:  "must be positive",
		},
		{
			name: "zero scale factor",
			yaml: "- url: https://example.com/\n  scale_factor: 0\n",
			path: "entries[0].scale_factor",
			line: 2,
			msg:  "must be positive",
		},
		{
			name: "scale factor with retina",
			yaml: "- url: https://example.com/\n  retina: true\n  scale_factor: 2\n",
			path: "entries[0].scale_factor",
			line: 3,
			msg:  "cannot be combined with retina",
		},
		{
			name: "quality too high",
			yaml: "- url: https://example.com/\n  quality: 101\n",
			path: "entries[0].quality",
			line: 2,
			msg:  "must be between 1 and 100",
		},
		{
			name: "quality too low",
			yaml: "- url: https://example.com/\n  quality: 0\n",
			path: "entries[0].quality",
			line: 2,
			msg:  "must be between 1 and 100",
		},
		{
			name: "skip and fail",
			yaml: "- url: https://example.com/\n  skip: true\n  fail: true\n",
			path: "entries[0].fail",
			line: 3,
			msg:  "cannot be combined with skip",
		},
		{
			name: "empty selector",
			yaml: "- url: https://example.com/\n  selector: \" \"\n",
			path: "entries[0].selector",
			line: 2,
			msg:  "must not contain an empty selector",
		},
		{
			name: "empty selector in a list",
			yaml: "- url: https://example.com/\n  js_selectors_all: [\"ok\", \"\"]\n",
			path: "entries[0].js_selectors_all",
			line: 2,
			msg:  "must not contain an empty selector",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := parseErr(t, tt.yaml)
			if err.Path != tt.path {
				t.Errorf("path = %q, want %q (%v)", err.Path, tt.path, err)
			}
			if err.Line != tt.line {
				t.Errorf("line = %d, want %d (%v)", err.Line, tt.line, err)
			}
			if err.Msg != tt.msg {
				t.Errorf("msg = %q, want %q", err.Msg, tt.msg)
			}
		})
	}
}

func TestErrorMessageFormat(t *testing.T) {
	err := parseErr(t, "- url: https://example.com/\n  bogus: 1\n")
	if got, want := err.Error(), "entries[0].bogus (line 2): unknown field"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestSyntaxError(t *testing.T) {
	_, err := config.ParseMulti([]byte("- url: [unclosed\n"), "")
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "yaml:") {
		t.Errorf("error %q still carries the library prefix", err)
	}
}

func TestValidValues(t *testing.T) {
	// Values that are only rejected when the key is actually present.
	m := parse(t, "- url: https://example.com/\n  wait: 0\n  padding: 0\n  scale_factor: 0.5\n  quality: 1\n")
	shot := m.Entries[0].(*config.Shot)
	if shot.ScaleFactor != 0.5 || shot.Quality != 1 {
		t.Errorf("shot = %+v", shot)
	}
}

func TestAnchorsAndAliases(t *testing.T) {
	m := parse(t, `
- &base
  url: https://example.com/
  width: 400
- *base
`)
	if len(m.Entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(m.Entries))
	}
	for i, e := range m.Entries {
		shot, ok := e.(*config.Shot)
		if !ok || shot.Width != 400 {
			t.Errorf("entries[%d] = %+v", i, e)
		}
	}
}

func TestBaseDirResolvesOutput(t *testing.T) {
	m, err := config.ParseMulti([]byte("- url: https://example.com/\n  output: shots/a.png\n- url: https://example.com/\n"), "/tmp/project")
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Entries[0].(*config.Shot).Output; got != filepath.FromSlash("/tmp/project/shots/a.png") {
		t.Errorf("output = %q, want it resolved against the base directory", got)
	}
	if got := m.Entries[1].(*config.Shot).Output; got != "" {
		t.Errorf("output = %q, want it left empty", got)
	}
}

func TestBaseDirLeavesAbsoluteAndStdout(t *testing.T) {
	abs := filepath.Join(string(filepath.Separator), "var", "a.png")
	m, err := config.ParseMulti([]byte("- url: https://example.com/\n  output: "+filepath.ToSlash(abs)+"\n- url: https://example.com/\n  output: \"-\"\n"), "/tmp/project")
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Entries[0].(*config.Shot).Output; got != abs {
		t.Errorf("output = %q, want %q", got, abs)
	}
	if got := m.Entries[1].(*config.Shot).Output; got != "-" {
		t.Errorf("output = %q, want -", got)
	}
}

func TestLoadMulti(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shots.yml")
	if err := os.WriteFile(path, []byte("- url: https://example.com/\n  output: out.png\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := config.LoadMulti(path)
	if err != nil {
		t.Fatal(err)
	}
	if m.BaseDir != dir {
		t.Errorf("BaseDir = %q, want %q", m.BaseDir, dir)
	}
	if got, want := m.Entries[0].(*config.Shot).Output, filepath.Join(dir, "out.png"); got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

func TestLoadMultiMissingFile(t *testing.T) {
	if _, err := config.LoadMulti(filepath.Join(t.TempDir(), "nope.yml")); err == nil {
		t.Fatal("expected an error")
	}
}
