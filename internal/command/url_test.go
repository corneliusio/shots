package command

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fileURL(path string) string {
	slashed := filepath.ToSlash(path)
	if !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed
	}
	return (&url.URL{Scheme: "file", Path: slashed}).String()
}

func TestNormalizeURL(t *testing.T) {
	dir := t.TempDir()
	page := filepath.Join(dir, "index.html")
	if err := os.WriteFile(page, []byte("<h1>hi</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	spaced := filepath.Join(dir, "a page.html")
	if err := os.WriteFile(spaced, []byte("<h1>hi</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		target string
		want   string
	}{
		{"a bare domain gets http", "example.com", "http://example.com"},
		{"a host and path get http", "example.com/a/b", "http://example.com/a/b"},
		{"a host with a port gets http", "localhost:8000/x", "http://localhost:8000/x"},
		{"http is left alone", "http://example.com/x", "http://example.com/x"},
		{"https is left alone", "https://example.com/x", "https://example.com/x"},
		{"file is left alone", "file:///tmp/x.html", "file:///tmp/x.html"},
		{"about is left alone", "about:blank", "about:blank"},
		{"an existing file becomes a file URL", page, fileURL(page)},
		{"a file URL escapes spaces", spaced, fileURL(spaced)},
		{"a missing path is treated as a host", "not-a-file.html", "http://not-a-file.html"},
		{"a directory is treated as a host", dir, "http://" + dir},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeURL(tt.target)
			if err != nil {
				t.Fatalf("NormalizeURL(%q): %v", tt.target, err)
			}
			if got != tt.want {
				t.Errorf("NormalizeURL(%q) = %q, want %q", tt.target, got, tt.want)
			}
		})
	}
}

func TestNormalizeURLEmpty(t *testing.T) {
	_, err := NormalizeURL("  ")
	var usage usageError
	if !errors.As(err, &usage) {
		t.Fatalf("NormalizeURL(\"  \") error = %v, want a usage error", err)
	}
}
