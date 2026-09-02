package browser

import (
	"archive/zip"
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPinnedManifest(t *testing.T) {
	m := Pinned()
	if m.Version == "" {
		t.Fatal("chrome.json has no version")
	}
	// Every platform tools/pin-chrome knows about must be installable.
	for _, platform := range []string{"linux64", "mac-arm64", "mac-x64", "win32", "win64"} {
		build, ok := m.Platforms[platform]
		if !ok {
			t.Errorf("%s: not pinned", platform)
			continue
		}
		switch {
		case !strings.Contains(build.URL, m.Version):
			t.Errorf("%s: URL %q does not name version %s", platform, build.URL, m.Version)
		case !strings.Contains(build.URL, "/chrome-headless-shell-"):
			t.Errorf("%s: URL %q is not a Chrome Headless Shell archive", platform, build.URL)
		case len(build.SHA256) != 64:
			t.Errorf("%s: SHA256 %q is not a hex digest", platform, build.SHA256)
		case !strings.Contains(build.Binary, "chrome-headless-shell"):
			t.Errorf("%s: executable %q is not Chrome Headless Shell", platform, build.Binary)
		}
	}
}

func TestSafeJoin(t *testing.T) {
	dir := filepath.Join(string(filepath.Separator), "tmp", "dest")
	tests := []struct {
		name    string
		entry   string
		wantErr bool
	}{
		{"a plain file", "chrome-linux64/chrome", false},
		{"a nested file", "a/b/c/d.so", false},
		{"traversal", "../escape", true},
		{"traversal in the middle", "a/../../escape", true},
		{"an absolute path", "/etc/passwd", true},
		{"a sneaky prefix", "../destination/x", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := safeJoin(dir, tt.entry)
			if (err != nil) != tt.wantErr {
				t.Fatalf("safeJoin(%q, %q) error = %v, want error = %v", dir, tt.entry, err, tt.wantErr)
			}
		})
	}
}

// zipEntry is one file to put into a test archive.
type zipEntry struct {
	name string
	mode fs.FileMode
	body string
}

func buildZip(t *testing.T, entries ...zipEntry) ([]byte, int64) {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, e := range entries {
		header := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		header.SetMode(e.mode)
		f, err := w.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), int64(buf.Len())
}

func TestUnzip(t *testing.T) {
	t.Run("files keep their mode and links are recreated", func(t *testing.T) {
		data, size := buildZip(t,
			zipEntry{name: "app/chrome", mode: 0o755, body: "binary"},
			zipEntry{name: "app/readme.txt", mode: 0o644, body: "notes"},
			zipEntry{name: "app/current", mode: 0o777 | fs.ModeSymlink, body: "chrome"},
		)
		dir := t.TempDir()
		if err := unzip(bytes.NewReader(data), size, dir); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(filepath.Join(dir, "app", "chrome"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Errorf("chrome mode %s, want it executable", info.Mode())
		}
		target, err := os.Readlink(filepath.Join(dir, "app", "current"))
		if err != nil || target != "chrome" {
			t.Errorf("readlink = %q, %v; want \"chrome\"", target, err)
		}
	})

	for _, tt := range []struct {
		name  string
		entry zipEntry
		want  string
	}{
		{"traversal is rejected", zipEntry{name: "../escape", mode: 0o644, body: "x"}, "escapes"},
		{"an absolute entry is rejected", zipEntry{name: "/etc/passwd", mode: 0o644, body: "x"}, "absolute"},
		{"an escaping symlink is rejected", zipEntry{name: "app/out", mode: 0o777 | fs.ModeSymlink, body: "../../etc/passwd"}, "escapes"},
		{"an absolute symlink is rejected", zipEntry{name: "app/out", mode: 0o777 | fs.ModeSymlink, body: "/etc/passwd"}, "absolute"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data, size := buildZip(t, tt.entry)
			err := unzip(bytes.NewReader(data), size, t.TempDir())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}
