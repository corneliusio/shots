package har

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// entry is one finished request with a body, ready to be extracted.
func entry(url, mimeType, text, encoding string) *Entry {
	return &Entry{
		StartedDateTime: time.Unix(1700000000, 0).UTC(),
		Request:         Request{Method: "GET", URL: url},
		Response:        Response{Status: 200, Content: Content{Size: int64(len(text)), MimeType: mimeType, Text: text, Encoding: encoding}},
	}
}

func TestWriteZipHoldsTheLog(t *testing.T) {
	var buf bytes.Buffer
	log := Log{Version: Version, Creator: Creator{Name: "shots"}, Entries: []*Entry{entry("http://example.com/", "text/html", "<h1>hi</h1>", "")}}
	if err := WriteZip(&buf, log); err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(reader.File) != 1 || reader.File[0].Name != archiveMember {
		t.Fatalf("archive holds %d members, want one %s", len(reader.File), archiveMember)
	}
	member, err := reader.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	defer member.Close()
	data, err := io.ReadAll(member)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Log Log `json:"log"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("%v in:\n%s", err, data)
	}
	if doc.Log.Version != Version || len(doc.Log.Entries) != 1 {
		t.Errorf("decoded log = %+v", doc.Log)
	}
}

func TestExtract(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 0xff}
	log := Log{Entries: []*Entry{
		entry("http://example.com/", "text/html", "<h1>hi</h1>", ""),
		entry("http://example.com/style.css", "text/css", "h1 { color: red }", ""),
		// A second resource with the same derived name gets a suffix.
		entry("http://example.com/style.css", "text/css", "h1 { color: blue }", ""),
		entry("http://example.com/logo.png", "image/png", base64.StdEncoding.EncodeToString(png), "base64"),
		// An entry whose body was never captured is not a file.
		entry("http://example.com/evicted.js", "text/javascript", "", ""),
	}}
	dir := filepath.Join(t.TempDir(), "resources")
	written, err := Extract(log, dir)
	if err != nil {
		t.Fatal(err)
	}
	if written != 4 {
		t.Errorf("extracted %d resources, want 4", written)
	}
	want := map[string]string{
		"example-com.html":        "<h1>hi</h1>",
		"example-com-style.css":   "h1 { color: red }",
		"example-com-style.1.css": "h1 { color: blue }",
		"example-com-logo.png":    string(png),
	}
	for name, content := range want {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if string(got) != content {
			t.Errorf("%s = %q, want %q", name, got, content)
		}
	}
	names, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != len(want) {
		t.Errorf("extracted %d files, want %d", len(names), len(want))
	}
}

func TestResourceName(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		mimeType string
		want     string
	}{
		{"the content type names the file", "http://example.com/", "text/html; charset=utf-8", "example-com.html"},
		{"a matching extension is not repeated", "http://example.com/a/style.css", "text/css", "example-com-a-style.css"},
		{"jpg and jpeg are the same extension", "http://example.com/photo.jpeg", "image/jpeg", "example-com-photo.jpeg"},
		{"a mismatched extension stays in the name", "http://example.com/page.php", "text/html", "example-com-page-php.html"},
		{"an unknown type keeps the URL extension", "http://example.com/data.bson", "application/x-bson", "example-com-data-bson.bson"},
		{"neither one leaves .bin", "http://example.com/stream", "application/octet-stream", "example-com-stream.bin"},
		{"the query is not part of the name", "http://example.com/q?a=1", "application/json", "example-com-q.json"},
		{"a traversal in the path cannot escape", "http://example.com/../../etc/passwd", "text/plain", "example-com-------etc-passwd.txt"},
		{"an encoded traversal cannot escape either", "http://example.com/%2e%2e%2f%2e%2e%2fetc%2fpasswd", "text/plain", "example-com-------etc-passwd.txt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stem, ext, err := resourceName(tt.url, tt.mimeType)
			if err != nil {
				t.Fatal(err)
			}
			if got := stem + "." + ext; got != tt.want {
				t.Errorf("resourceName(%q, %q) = %q, want %q", tt.url, tt.mimeType, got, tt.want)
			}
		})
	}
}

// Extracted names come from URLs a page chose, so the guard against one that
// would escape the directory is the last line of defence.
func TestSafeNameRejectsEscapes(t *testing.T) {
	for _, name := range []string{"", ".", "..", "/etc/passwd", "../secrets", "a/b.txt", `..\secrets`, `C:\Windows\x`, "sub/../../x"} {
		if err := safeName(name); err == nil {
			t.Errorf("safeName(%q) allowed it", name)
		}
	}
	for _, name := range []string{"example-com.html", "a.1.css", "_-.bin"} {
		if err := safeName(name); err != nil {
			t.Errorf("safeName(%q) = %v", name, err)
		}
	}
}

func TestWriteDoesNotEscapeMarkup(t *testing.T) {
	var buf bytes.Buffer
	log := Log{Entries: []*Entry{entry("http://example.com/", "text/html", "<h1>hi & bye</h1>", "")}}
	if err := Write(&buf, log); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"<h1>hi & bye</h1>"`) {
		t.Errorf("body was escaped:\n%s", buf.String())
	}
}
