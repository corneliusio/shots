package browser

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
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

func TestUnresolvedLibraries(t *testing.T) {
	const ldd = "\tlinux-vdso.so.1 (0x00007fff)\n" +
		"\tlibgbm.so.1 => not found\n" +
		"\tlibc.so.6 => /lib/x86_64-linux-gnu/libc.so.6 (0x00007f1a)\n" +
		"\tlibnss3.so => not found\n"
	got := unresolved(ldd)
	want := []string{"libgbm.so.1", "libnss3.so"}
	if !slices.Equal(got, want) {
		t.Errorf("unresolved(...) = %q, want %q", got, want)
	}
	if got := unresolved("\tnot a dynamic executable\n"); got != nil {
		t.Errorf("unresolved(...) = %q, want none", got)
	}
}

func TestNotFoundErrorListsLocations(t *testing.T) {
	err := &NotFoundError{Checked: []string{"/one (a)", "/two (b)"}}
	for _, want := range []string{"/one (a)", "/two (b)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q is missing %q", err, want)
		}
	}
}

func TestPathNamesPreferHeadlessShell(t *testing.T) {
	headless := pathNames(false)
	if len(headless) == 0 || !strings.Contains(headless[0], "headless") {
		t.Fatalf("pathNames(false) = %q, want a headless shell first", headless)
	}
	for _, name := range pathNames(true) {
		if strings.Contains(name, "headless") {
			t.Fatalf("pathNames(true) = %q, want only headed-capable browsers", pathNames(true))
		}
	}
}

func TestHeadedDiscoverySkipsInstalledShell(t *testing.T) {
	// Discovery uses a local fixture, independent of vendor download availability.
	originalPinned := pinned
	pinned = Manifest{
		Version: "discovery-fixture",
		Platforms: map[string]Build{
			Platform(): {URL: "https://example.invalid/fixture.zip", Binary: "chrome-headless-shell"},
		},
	}
	t.Cleanup(func() { pinned = originalPinned })

	root := t.TempDir()
	t.Setenv("SHOTS_BROWSER_PATH", "")
	switch runtime.GOOS {
	case "darwin":
		t.Setenv("HOME", root)
	case "windows":
		t.Setenv("LocalAppData", filepath.Join(root, "cache"))
	default:
		t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	}

	installed, err := InstalledPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(installed), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installed, []byte("shell"), 0o755); err != nil {
		t.Fatal(err)
	}

	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	fullChrome := filepath.Join(bin, pathNames(true)[0])
	if err := os.WriteFile(fullChrome, []byte("chrome"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	got, err := discover("", false)
	if err != nil || got != installed {
		t.Fatalf("discover headless = %q, %v; want installed shell %q", got, err, installed)
	}
	got, err = discover("", true)
	if err != nil || got != fullChrome {
		t.Fatalf("discover headed = %q, %v; want full Chrome %q", got, err, fullChrome)
	}
}

func TestHeadedNotFoundExplainsInstalledBrowser(t *testing.T) {
	err := (&NotFoundError{Headed: true, Checked: []string{"/one"}}).Error()
	for _, want := range []string{"headed Chrome", "shots install", ProductName} {
		if !strings.Contains(err, want) {
			t.Errorf("error %q is missing %q", err, want)
		}
	}
}

func TestDefaultTimeout(t *testing.T) {
	if DefaultTimeout != 30*time.Second {
		t.Errorf("DefaultTimeout = %s, want 30s", DefaultTimeout)
	}
}
