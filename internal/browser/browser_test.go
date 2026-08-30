package browser

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

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
	t.Run("a missing environment path falls through", func(t *testing.T) {
		absent := filepath.Join(dir, "absent")
		t.Setenv("SHOTS_BROWSER_PATH", absent)
		got, err := Discover("")
		// Whether the rest of the order finds a browser depends on the machine,
		// so the only failure allowed here is "nothing anywhere".
		var notFound *NotFoundError
		if err != nil && !errors.As(err, &notFound) {
			t.Fatalf("Discover(\"\") = %q, %v; want the variable to be skipped, not to fail", got, err)
		}
		if got == absent {
			t.Fatal("a nonexistent browser was accepted")
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
