package command

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestJavaScriptResults(t *testing.T) {
	srv := site(t)
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"a string is JSON", []string{"document.title"}, "\"Promise\"\n"},
		{"a number is JSON", []string{"window.shotsValue"}, "42\n"},
		{"undefined is null", []string{"undefined"}, "null\n"},
		{"an object is indented, in page order", []string{"({b: 1, a: [2, 3]})"}, "{\n    \"b\": 1,\n    \"a\": [\n        2,\n        3\n    ]\n}\n"},
		{"a promise is awaited", []string{"window.shotsPromise"}, "{\n    \"ok\": true,\n    \"value\": 42\n}\n"},
		{"a function is called", []string{"() => window.shotsValue"}, "42\n"},
		{"a function returning a promise is awaited", []string{"() => window.shotsPromise"}, "{\n    \"ok\": true,\n    \"value\": 42\n}\n"},
		{"raw prints a string as text", []string{"document.title", "--raw"}, "Promise\n"},
		{"raw leaves a non-string as JSON", []string{"window.shotsValue", "--raw"}, "42\n"},
		{"a multi-statement script returns its last value", []string{"var n = 1; n + 1"}, "2\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := run(t, append([]string{"javascript", srv.URL + "/promise.html"}, tt.args...)...)
			if got.code != exitOK {
				t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
			}
			if string(got.stdout) != tt.want {
				t.Errorf("stdout = %q, want %q", got.stdout, tt.want)
			}
		})
	}
}

func TestJavaScriptErrors(t *testing.T) {
	srv := site(t)
	tests := []struct {
		name string
		args []string
		want int
		says string
	}{
		{"an exception is reported without a stack", []string{"throw new Error('boom')"}, exitFailure, "Error: boom"},
		{"a rejected promise is reported", []string{"window.shotsRejected()"}, exitFailure, "rejected on purpose"},
		{"a syntax error is reported", []string{"return"}, exitFailure, "SyntaxError"},
		{"a missing script is a usage error", nil, exitUsage, "no JavaScript to run"},
		{"a script argument and --javascript conflict", []string{"1", "-j", "1"}, exitUsage, "cannot be combined"},
		{"a negative width is a usage error", []string{"1", "-w", "0"}, exitUsage, "--width must be positive"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := run(t, append([]string{"javascript", srv.URL + "/promise.html"}, tt.args...)...)
			if got.code != tt.want {
				t.Fatalf("exit %d, want %d; stderr:\n%s", got.code, tt.want, got.stderr)
			}
			if !strings.Contains(got.stderr, tt.says) {
				t.Errorf("stderr = %q, want it to mention %q", got.stderr, tt.says)
			}
			if len(got.stdout) > 0 {
				t.Errorf("stdout = %q, want nothing", got.stdout)
			}
			// A stack trace would name the wrapper shots evaluates through.
			if strings.Contains(got.stderr, "at ") {
				t.Errorf("stderr = %q, want no stack trace", got.stderr)
			}
		})
	}
}

func TestJavaScriptSources(t *testing.T) {
	srv := site(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "title.js")
	if err := os.WriteFile(script, []byte("() => document.title"), 0o644); err != nil {
		t.Fatal(err)
	}
	url := srv.URL + "/promise.html"

	t.Run("standard input", func(t *testing.T) {
		got := runStdin(t, "document.title", "javascript", url, "--raw")
		if got.code != exitOK || string(got.stdout) != "Promise\n" {
			t.Fatalf("exit %d, stdout %q, stderr:\n%s", got.code, got.stdout, got.stderr)
		}
	})
	t.Run("a file", func(t *testing.T) {
		got := run(t, "javascript", url, "--js-file", script, "--raw")
		if got.code != exitOK || string(got.stdout) != "Promise\n" {
			t.Fatalf("exit %d, stdout %q, stderr:\n%s", got.code, got.stdout, got.stderr)
		}
	})
	t.Run("a file named on standard input", func(t *testing.T) {
		got := runStdin(t, "document.title", "javascript", url, "--js-file", "-", "--raw")
		if got.code != exitOK || string(got.stdout) != "Promise\n" {
			t.Fatalf("exit %d, stdout %q, stderr:\n%s", got.code, got.stdout, got.stderr)
		}
	})
	t.Run("a missing file fails", func(t *testing.T) {
		got := run(t, "javascript", url, "--js-file", filepath.Join(dir, "absent.js"))
		if got.code != exitFailure {
			t.Fatalf("exit %d, want %d; stderr:\n%s", got.code, exitFailure, got.stderr)
		}
	})
}

func TestJavaScriptOutput(t *testing.T) {
	srv := site(t)
	url := srv.URL + "/promise.html"

	t.Run("the viewport follows width and height", func(t *testing.T) {
		got := run(t, "javascript", url, "[innerWidth, innerHeight]", "-w", "600", "--height", "400")
		if got.code != exitOK {
			t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
		}
		if want := "[\n    600,\n    400\n]\n"; string(got.stdout) != want {
			t.Errorf("stdout = %q, want %q", got.stdout, want)
		}
	})

	t.Run("console logging leaves standard output alone", func(t *testing.T) {
		got := run(t, "javascript", srv.URL+"/console.html", "document.title", "--raw", "--log-console")
		if got.code != exitOK {
			t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
		}
		if string(got.stdout) != "Console\n" {
			t.Errorf("stdout = %q, want the result alone", got.stdout)
		}
		if !strings.Contains(got.stderr, "log: log message") {
			t.Errorf("stderr = %q, want the console messages", got.stderr)
		}
	})

	t.Run("a file destination is reported on standard error", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "result.json")
		got := run(t, "javascript", url, "document.title", "-o", out)
		if got.code != exitOK {
			t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
		}
		if data, err := os.ReadFile(out); err != nil || string(data) != "\"Promise\"\n" {
			t.Fatalf("file = %q (%v)", data, err)
		}
		if len(got.stdout) > 0 || !strings.Contains(got.stderr, strconv.Quote(out)) {
			t.Errorf("stdout = %q, stderr = %q", got.stdout, got.stderr)
		}
	})

	t.Run("silent suppresses the report but not the file", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "result.json")
		got := run(t, "javascript", url, "document.title", "-o", out, "--silent")
		if got.code != exitOK || got.stderr != "" {
			t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
		}
		if _, err := os.Stat(out); err != nil {
			t.Fatal(err)
		}
	})
}

func TestJavaScriptWaits(t *testing.T) {
	srv := site(t)
	url := srv.URL + "/delayed.html"
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"without a wait the element is missing", nil, "false\n"},
		{"a fixed wait is long enough", []string{"--wait", "800"}, "true\n"},
		{"a condition waits for it", []string{"--wait-for", "window.shotsDelayedDone"}, "true\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := append([]string{"javascript", url, "!!document.querySelector('#delayed')"}, tt.args...)
			got := run(t, args...)
			if got.code != exitOK {
				t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
			}
			if string(got.stdout) != tt.want {
				t.Errorf("stdout = %q, want %q", got.stdout, tt.want)
			}
		})
	}
}

func TestGitHubScriptURL(t *testing.T) {
	tests := []struct {
		ref  string
		want string
	}{
		{"gh:octocat/scripts/readme-title.js", "https://raw.githubusercontent.com/octocat/scripts/main/readme-title.js"},
		{"gh:octocat/scripts/readme-title", "https://raw.githubusercontent.com/octocat/scripts/main/readme-title.js"},
		{"gh:octocat/repo/scripts/title.js", "https://raw.githubusercontent.com/octocat/repo/main/scripts/title.js"},
		{"gh:octocat/readme-title.js", ""},
		{"gh:octocat", ""},
		{"gh:", ""},
		{"gh:octocat/", ""},
		{"gh:/title.js", ""},
	}
	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			got, err := githubURL(tt.ref)
			switch {
			case tt.want == "" && err == nil:
				t.Errorf("githubURL(%q) = %q, want an error", tt.ref, got)
			case tt.want == "":
			case err != nil:
				t.Errorf("githubURL(%q): %v", tt.ref, err)
			case got != tt.want:
				t.Errorf("githubURL(%q) = %q, want %q", tt.ref, got, tt.want)
			}
		})
	}
}
