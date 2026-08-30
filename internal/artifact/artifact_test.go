package artifact_test

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mostlyserious/shots/internal/artifact"
)

func TestFilenameFor(t *testing.T) {
	tests := []struct {
		url  string
		ext  string
		want string
	}{
		{"https://example.com/", "png", "example-com.png"},
		{"https://example.com/", "har.zip", "example-com.har.zip"},
		{"https://example.com", "png", "example-com.png"},
		{"http://www.example.com/foo/bar", "jpg", "www-example-com-foo-bar.jpg"},
		{"https://example.com/search?q=hello world", "png", "example-com-search.png"},
		{"https://example.com/a_b-c.d", "png", "example-com-a_b-c-d.png"},
		{"file:///tmp/page.html", "png", "tmp-page-html.png"},
		{"./page.html", "png", "page-html.png"},
		{"https://example.com:8000/", "png", "example-com8000.png"},
		{"", "png", "shot.png"},
		{"///", "png", "shot.png"},
		{"https://éxample.com/", "png", "xample-com.png"},
		// Fragments and queries are outside netloc+path, like upstream.
		{"https://example.com/a#b", "png", "example-com-a.png"},
		{"?q=1", "png", "shot.png"},
		{"https://example.com/../..", "png", "example-com.png"},
	}
	for _, tt := range tests {
		if got := artifact.FilenameFor(tt.url, tt.ext); got != tt.want {
			t.Errorf("FilenameFor(%q, %q) = %q, want %q", tt.url, tt.ext, got, tt.want)
		}
	}
}

func TestFilenameForNeverEscapesDirectory(t *testing.T) {
	for _, u := range []string{"../../etc/passwd", "https://example.com/../..", "/absolute"} {
		got := artifact.FilenameFor(u, "png")
		if strings.ContainsRune(got, filepath.Separator) || strings.HasPrefix(got, ".") {
			t.Errorf("FilenameFor(%q) = %q, want a plain relative filename", u, got)
		}
	}
}

func TestUniqueAddsCollisionSuffixes(t *testing.T) {
	tests := []struct {
		name string
		stem string
		ext  string
		want []string
	}{
		{"extension", "example-com", "png", []string{"example-com.png", "example-com.1.png", "example-com.2.png"}},
		// A multi-part extension stays whole: the suffix goes in front of it.
		{"multi-part extension", "trace", "har.zip", []string{"trace.har.zip", "trace.1.har.zip"}},
		{"no extension", "shot", "", []string{"shot", "shot.1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for i, want := range tt.want {
				got, err := artifact.Unique(filepath.Join(dir, tt.stem), tt.ext)
				if err != nil {
					t.Fatalf("Unique: %v", err)
				}
				if want := filepath.Join(dir, want); got != want {
					t.Fatalf("Unique #%d = %q, want %q", i, got, want)
				}
				if err := os.WriteFile(got, []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestPath(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	got, err := artifact.Path("", "https://example.com/", "png")
	if err != nil {
		t.Fatal(err)
	}
	if got != "example-com.png" {
		t.Fatalf("Path = %q, want example-com.png", got)
	}
	if err := os.WriteFile(got, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err = artifact.Path("", "https://example.com/", "png")
	if err != nil {
		t.Fatal(err)
	}
	if got != "example-com.1.png" {
		t.Fatalf("Path = %q, want example-com.1.png", got)
	}

	if err := os.WriteFile("example-com.har.zip", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err = artifact.Path("", "https://example.com/", "har.zip")
	if err != nil {
		t.Fatal(err)
	}
	if got != "example-com.1.har.zip" {
		t.Fatalf("Path = %q, want example-com.1.har.zip", got)
	}

	// An explicit path is used verbatim, even when it exists.
	if err := os.WriteFile("out.png", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err = artifact.Path("out.png", "https://example.com/", "png")
	if err != nil {
		t.Fatal(err)
	}
	if got != "out.png" {
		t.Fatalf("Path = %q, want out.png", got)
	}
}

func TestWriteCreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.png")

	if err := artifact.Write(path, []byte("data"), artifact.Options{}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "data" {
		t.Errorf("content = %q, want %q", got, "data")
	}
	assertOnly(t, dir, "out.png")

	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o644 {
			t.Errorf("mode = %v, want 0644", info.Mode().Perm())
		}
	}
}

func TestWriteMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permissions")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	if err := artifact.Write(path, []byte("{}"), artifact.Options{Mode: 0o600}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestWriteReplacesExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.png")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := artifact.Write(path, []byte("new"), artifact.Options{}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "new" {
		t.Errorf("content = %q, want new", got)
	}
	assertOnly(t, dir, "out.png")
}

func TestWriteNoClobber(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.png")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := artifact.Write(path, []byte("new"), artifact.Options{NoClobber: true})
	if !errors.Is(err, artifact.ErrExists) {
		t.Fatalf("err = %v, want ErrExists", err)
	}
	if !strings.Contains(err.Error(), "out.png") {
		t.Errorf("err = %v, want the path in the message", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "old" {
		t.Errorf("content = %q, want old", got)
	}
	assertOnly(t, dir, "out.png")

	// No-clobber on a free path still writes.
	fresh := filepath.Join(dir, "fresh.png")
	if err := artifact.Write(fresh, []byte("new"), artifact.Options{NoClobber: true}); err != nil {
		t.Fatal(err)
	}
}

func TestWriteStdout(t *testing.T) {
	var buf bytes.Buffer
	if err := artifact.Write(artifact.Stdout, []byte("binary"), artifact.Options{Stdout: &buf}); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "binary" {
		t.Errorf("stdout = %q, want binary", buf.String())
	}
}

// failingReader yields some bytes and then fails, like a truncated CDP stream.
type failingReader struct{ read bool }

func (r *failingReader) Read(p []byte) (int, error) {
	if r.read {
		return 0, errors.New("boom")
	}
	r.read = true
	return copy(p, "partial"), nil
}

func TestWriteStreamFailureLeavesNothingBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.png")

	if err := artifact.WriteStream(path, &failingReader{}, artifact.Options{}); err == nil {
		t.Fatal("expected an error")
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("stat: %v, want the file to be absent", err)
	}
	assertOnly(t, dir)
}

func TestWriteStreamFailureKeepsPreviousFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.png")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := artifact.WriteStream(path, &failingReader{}, artifact.Options{}); err == nil {
		t.Fatal("expected an error")
	}
	got, _ := os.ReadFile(path)
	if string(got) != "old" {
		t.Errorf("content = %q, want the untouched original", got)
	}
	assertOnly(t, dir, "out.png")
}

func TestWriteMissingDirectory(t *testing.T) {
	dir := t.TempDir()
	err := artifact.Write(filepath.Join(dir, "nope", "out.png"), []byte("x"), artifact.Options{})
	if err == nil {
		t.Fatal("expected an error; parent directories must not be created")
	}
	assertOnly(t, dir)
}

func TestWriteStreamLarge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.bin")
	data := bytes.Repeat([]byte("shots"), 200_000)

	if err := artifact.WriteStream(path, bytes.NewReader(data), artifact.Options{}); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("content differs: got %d bytes, want %d", len(got), len(data))
	}
}

// assertOnly checks that dir contains exactly the named entries, catching
// leftover temporary files.
func assertOnly(t *testing.T, dir string, want ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if len(got) != len(want) {
		t.Fatalf("directory contains %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("directory contains %v, want %v", got, want)
		}
	}
}
