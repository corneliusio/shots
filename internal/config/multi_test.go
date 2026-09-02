package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/corneliusio/shots/internal/config"
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
  format: png
  width: 800
  height: 600
  wait: 500
  wait_for: document.title
  javascript: document.body.classList.add('ready')
  selector: "#main"
  selectors:
    - .a
    - .b
  selector_all: .item
  js_selector: div
  js_selectors_all: [span]
  padding: 10
  omit_background: true
`)
	if len(m.Entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(m.Entries))
	}
	want := config.Entry{
		URL:        "https://example.com/",
		Output:     "example.png",
		Format:     "png",
		Width:      800,
		Height:     600,
		Wait:       500,
		WaitFor:    "document.title",
		JavaScript: "document.body.classList.add('ready')",
		// selector and selectors are aliases; values accumulate in source order.
		Selectors:      config.StringList{"#main", ".a", ".b"},
		SelectorsAll:   config.StringList{".item"},
		JSSelectors:    config.StringList{"div"},
		JSSelectorsAll: config.StringList{"span"},
		Padding:        10,
		OmitBackground: true,
	}
	if got := m.Entries[0]; !reflect.DeepEqual(got, want) {
		t.Errorf("entry =\n%+v\nwant\n%+v", got, want)
	}
}

func TestParseSteps(t *testing.T) {
	m := parse(t, `
- sh: echo hello
- sh: [echo, hello]
- server: python -m http.server 8000
- server: [python, -m, http.server]
- url: https://example.com/
`)
	want := []config.Entry{
		{Sh: config.Command{Shell: "echo hello"}},
		{Sh: config.Command{Args: []string{"echo", "hello"}}},
		{Server: config.Command{Shell: "python -m http.server 8000"}},
		{Server: config.Command{Args: []string{"python", "-m", "http.server"}}},
		{URL: "https://example.com/"},
	}
	if !reflect.DeepEqual(m.Entries, want) {
		t.Errorf("entries =\n%+v\nwant\n%+v", m.Entries, want)
	}
}

// Upstream's documented pattern: one entry does some work and then captures the
// page it produced.
func TestParseStepAndShotInOneEntry(t *testing.T) {
	m := parse(t, `
- server: python -m http.server 8000
  sh: echo "Hello" > index.html
  output: from-shell.png
  url: http://localhost:8000/
`)
	want := []config.Entry{{
		Sh:     config.Command{Shell: `echo "Hello" > index.html`},
		Server: config.Command{Shell: "python -m http.server 8000"},
		URL:    "http://localhost:8000/",
		Output: "from-shell.png",
	}}
	if !reflect.DeepEqual(m.Entries, want) {
		t.Errorf("entries =\n%+v\nwant\n%+v", m.Entries, want)
	}
}

func TestParseEntryOrderIsPreserved(t *testing.T) {
	m := parse(t, "- server: serve\n- url: http://localhost/\n- sh: cleanup\n")
	var got []string
	for _, e := range m.Entries {
		switch {
		case !e.Server.Empty():
			got = append(got, "server")
		case e.URL != "":
			got = append(got, "url")
		case !e.Sh.Empty():
			got = append(got, "sh")
		}
	}
	if want := []string{"server", "url", "sh"}; !reflect.DeepEqual(got, want) {
		t.Errorf("entries = %v, want %v", got, want)
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
			name: "unknown field beside a step",
			yaml: "- sh: echo hi\n  outupt: out.txt\n",
			path: "entries[0].outupt",
			line: 2,
			msg:  "unknown field",
		},
		{
			name: "python step is not supported",
			yaml: "- python: print(1)\n",
			path: "entries[0].python",
			line: 1,
			msg:  "unknown field",
		},
		{
			name: "second document",
			yaml: "- url: https://example.com/\n---\n- url: https://example.org/\n",
			path: "",
			line: 2,
			msg:  "expected a single YAML document",
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
			// A step may stand alone, but a url key still has to name a page.
			name: "blank url beside a step",
			yaml: "- sh: echo hi\n  url: \"\"\n",
			path: "entries[0].url",
			line: 2,
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
			name: "unknown image format",
			yaml: "- url: https://example.com/\n  format: webp\n",
			path: "entries[0].format",
			line: 2,
			msg:  "must be jpeg or png",
		},
		{
			name: "format conflicts with extension",
			yaml: "- url: https://example.com/\n  output: shot.png\n  format: jpeg\n",
			path: "entries[0].format",
			line: 3,
			msg:  "conflicts with output extension .png",
		},
		{
			name: "PNG quality",
			yaml: "- url: https://example.com/\n  format: png\n  quality: 80\n",
			path: "entries[0].quality",
			line: 3,
			msg:  "requires JPEG output",
		},
		{
			name: "transparent JPEG",
			yaml: "- url: https://example.com/\n  format: jpeg\n  omit_background: true\n",
			path: "entries[0].omit_background",
			line: 3,
			msg:  "requires PNG output",
		},
		{
			name: "javascript and js_file",
			yaml: "- url: https://example.com/\n  javascript: 1\n  js_file: a.js\n",
			path: "entries[0].js_file",
			line: 3,
			msg:  "cannot be combined with javascript",
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
	m := parse(t, "- url: https://example.com/\n  wait: 0\n  padding: 0\n  quality: 1\n")
	if e := m.Entries[0]; e.Wait != 0 || e.Padding != 0 || e.Quality != 1 {
		t.Errorf("entry = %+v", e)
	}
}

// Command-line flags upstream, so they are unknown keys here rather than
// silently ignored settings. See COMPAT.md.
func TestFlagsAreNotEntryKeys(t *testing.T) {
	for _, key := range []string{"retina", "scale_factor", "timeout", "skip", "fail"} {
		err := parseErr(t, "- url: https://example.com/\n  "+key+": 1\n")
		if err.Path != "entries[0]."+key || err.Msg != "unknown field" {
			t.Errorf("%s: err = %v", key, err)
		}
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
		if e.Width != 400 {
			t.Errorf("entries[%d] = %+v", i, e)
		}
	}
}

func TestBaseDirResolvesRelativePaths(t *testing.T) {
	m, err := config.ParseMulti([]byte("- url: https://example.com/\n  output: shots/a.png\n  js_file: js/clean.js\n- url: https://example.com/\n"), "/tmp/project")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := m.Entries[0].Output, filepath.FromSlash("/tmp/project/shots/a.png"); got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
	if got, want := m.Entries[0].JSFile, filepath.FromSlash("/tmp/project/js/clean.js"); got != want {
		t.Errorf("js_file = %q, want %q", got, want)
	}
	if got := m.Entries[1].Output; got != "" {
		t.Errorf("output = %q, want it left empty", got)
	}
}

func TestBaseDirLeavesAbsoluteStdoutAndGitHub(t *testing.T) {
	abs := filepath.Join(string(filepath.Separator), "var", "a.png")
	m, err := config.ParseMulti([]byte("- url: https://example.com/\n  output: "+filepath.ToSlash(abs)+"\n- url: https://example.com/\n  output: \"-\"\n  js_file: gh:simonw/shot-scraper/clean.js\n"), "/tmp/project")
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Entries[0].Output; got != abs {
		t.Errorf("output = %q, want %q", got, abs)
	}
	if got := m.Entries[1].Output; got != "-" {
		t.Errorf("output = %q, want -", got)
	}
	if got, want := m.Entries[1].JSFile, "gh:simonw/shot-scraper/clean.js"; got != want {
		t.Errorf("js_file = %q, want %q", got, want)
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
	if got, want := m.Entries[0].Output, filepath.Join(dir, "out.png"); got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

// An --output filter has to spell an entry's output the way the entry does, so
// it resolves the same way.
func TestResolveMatchesEntryOutputs(t *testing.T) {
	m, err := config.ParseMulti([]byte("- url: https://example.com/\n  output: shots/a.png\n"), "/tmp/project")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := m.Resolve("shots/a.png"), m.Entries[0].Output; got != want {
		t.Errorf("Resolve = %q, want %q", got, want)
	}
	if got := m.Resolve("-"); got != "-" {
		t.Errorf("Resolve(-) = %q, want -", got)
	}
	empty := &config.Multi{}
	if got := empty.Resolve("a.png"); got != "a.png" {
		t.Errorf("Resolve without a base directory = %q, want it left alone", got)
	}
}

func TestLoadMultiMissingFile(t *testing.T) {
	if _, err := config.LoadMulti(filepath.Join(t.TempDir(), "nope.yml")); err == nil {
		t.Fatal("expected an error")
	}
}
