package browser

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestOptionsValidate(t *testing.T) {
	tests := []struct {
		name string
		opts Options
		want string
	}{
		{"launching is unconstrained", Options{Path: "/bin/chrome", Args: []string{"a"}, Headed: true}, ""},
		{"connecting alone is fine", Options{CDPURL: "http://localhost:9222"}, ""},
		{"a path conflicts", Options{CDPURL: "x", Path: "/bin/chrome"}, "--browser-path"},
		{"an argument conflicts", Options{CDPURL: "x", Args: []string{"a"}}, "--browser-arg"},
		{"headed conflicts", Options{CDPURL: "x", Headed: true}, "--interactive"},
		{"devtools conflicts", Options{CDPURL: "x", DevTools: true}, "--devtools"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.opts.validate()
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
				t.Fatalf("error = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestSplitArg(t *testing.T) {
	tests := []struct {
		arg      string
		name     string
		value    string
		hasValue bool
	}{
		{"--disable-gpu", "disable-gpu", "", false},
		{"-disable-gpu", "disable-gpu", "", false},
		{"disable-gpu", "disable-gpu", "", false},
		{"--lang=fr", "lang", "fr", true},
		{"--proxy-server=http://x:1", "proxy-server", "http://x:1", true},
		{"--empty=", "empty", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.arg, func(t *testing.T) {
			name, value, hasValue := splitArg(tt.arg)
			if name != tt.name || value != tt.value || hasValue != tt.hasValue {
				t.Errorf("splitArg(%q) = %q, %q, %v; want %q, %q, %v", tt.arg, name, value, hasValue, tt.name, tt.value, tt.hasValue)
			}
		})
	}
}

func TestDiscoverExplicitPath(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "chrome")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(plain, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("an explicit path is used", func(t *testing.T) {
		got, err := Discover(exe)
		if err != nil || got != exe {
			t.Fatalf("Discover(%q) = %q, %v", exe, got, err)
		}
	})
	t.Run("a missing explicit path is reported", func(t *testing.T) {
		_, err := Discover(filepath.Join(dir, "absent"))
		if err == nil || !strings.Contains(err.Error(), "--browser-path") {
			t.Fatalf("error = %v, want it to name --browser-path", err)
		}
	})
	if runtime.GOOS != "windows" {
		t.Run("a non-executable explicit path is reported", func(t *testing.T) {
			_, err := Discover(plain)
			if err == nil || !strings.Contains(err.Error(), "not executable") {
				t.Fatalf("error = %v, want it to say the file is not executable", err)
			}
		})
	}
	t.Run("the environment is consulted next", func(t *testing.T) {
		t.Setenv("SHOTS_BROWSER_PATH", exe)
		got, err := Discover("")
		if err != nil || got != exe {
			t.Fatalf("Discover(\"\") = %q, %v", got, err)
		}
	})
}

func TestNotFoundErrorListsLocations(t *testing.T) {
	err := &NotFoundError{Checked: []string{"/one (a)", "/two (b)"}}
	for _, want := range []string{"/one (a)", "/two (b)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q is missing %q", err, want)
		}
	}
}

func TestDefaultTimeout(t *testing.T) {
	if DefaultTimeout != 30*time.Second {
		t.Errorf("DefaultTimeout = %s, want 30s", DefaultTimeout)
	}
}
