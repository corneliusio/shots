package har

import (
	"archive/zip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/mostlyserious/shots/internal/artifact"
)

// archiveMember is the name a zipped HAR keeps its log under, as Playwright's
// .har.zip does, so an archive shots wrote opens in the same tools.
const archiveMember = "har.har"

// document is the top level of a HAR file: everything sits under "log".
type document struct {
	Log Log `json:"log"`
}

// Write encodes log as a HAR document.
func Write(w io.Writer, log Log) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	// HTML in a captured body is data, not markup to be escaped.
	enc.SetEscapeHTML(false)
	return enc.Encode(document{Log: log})
}

// WriteZip encodes log as a .har.zip archive holding one har.har member.
func WriteZip(w io.Writer, log Log) error {
	zw := zip.NewWriter(w)
	member, err := zw.Create(archiveMember)
	if err != nil {
		return err
	}
	if err := Write(member, log); err != nil {
		return err
	}
	return zw.Close()
}

// Extract writes the captured response bodies into dir, one file per entry
// that has one, named after the request URL and the response's content type.
// It returns the number of files written.
//
// dir is created when it does not exist; its parents are not. Names are
// derived from URLs the page chose, so each one is checked for a path
// separator, a parent reference or an absolute path before it is joined to
// dir.
func Extract(log Log, dir string) (int, error) {
	if err := os.Mkdir(dir, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
		return 0, err
	}
	written := 0
	for _, entry := range log.Entries {
		body, err := entry.body()
		if err != nil {
			return written, fmt.Errorf("%s: %w", entry.Request.URL, err)
		}
		if len(body) == 0 {
			continue
		}
		stem, ext, err := resourceName(entry.Request.URL, entry.Response.Content.MimeType)
		if err != nil {
			return written, err
		}
		name, err := artifact.Unique(filepath.Join(dir, stem), ext)
		if err != nil {
			return written, err
		}
		if err := artifact.Write(name, body, artifact.Options{}); err != nil {
			return written, err
		}
		written++
	}
	return written, nil
}

// body decodes an entry's captured response body, or returns nil when the
// entry has none.
func (e *Entry) body() ([]byte, error) {
	content := e.Response.Content
	switch {
	case content.Text == "":
		return nil, nil
	case content.Encoding == "base64":
		return base64.StdEncoding.DecodeString(content.Text)
	default:
		return []byte(content.Text), nil
	}
}

// resourceName derives the filename for an extracted body: the URL's host and
// path folded the way automatic screenshot names are, plus the extension the
// content type implies. A URL extension that already agrees with the content
// type is not repeated in the stem.
func resourceName(rawURL, mimeType string) (stem, ext string, err error) {
	stem, urlExt := artifact.Stem(rawURL), pathExt(rawURL)
	ext = extensionForType(mimeType)
	switch {
	case ext == "" && urlExt == "":
		ext = "bin"
	case ext == "":
		ext = urlExt
	case ext == urlExt, isJPEG(ext) && isJPEG(urlExt):
		stem, ext = strings.TrimSuffix(stem, "-"+urlExt), urlExt
	}
	if stem == "" {
		stem = "resource"
	}
	if err := safeName(stem + "." + ext); err != nil {
		return "", "", err
	}
	return stem, ext, nil
}

// safeName rejects a name that would escape the extraction directory.
func safeName(name string) error {
	switch {
	case name == "", name == ".", name == "..",
		strings.ContainsAny(name, `/\`),
		filepath.IsAbs(name),
		filepath.Base(name) != name:
		return fmt.Errorf("unsafe resource name %q", name)
	}
	return nil
}

// pathExt is the lowercased extension of a URL's path, without the dot, and
// empty unless it is alphanumeric.
func pathExt(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(parsed.Path), "."))
	if ext == "" {
		return ""
	}
	for _, r := range ext {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			return ""
		}
	}
	return ext
}

func isJPEG(ext string) bool { return ext == "jpg" || ext == "jpeg" }

// extensions maps a content type to the file extension an extracted body gets,
// reproducing shot-scraper's table.
var extensions = map[string]string{
	"application/font-woff":  "woff",
	"application/javascript": "js",
	"application/json":       "json",
	"application/pdf":        "pdf",
	"application/xml":        "xml",
	"font/woff":              "woff",
	"font/woff2":             "woff2",
	"image/gif":              "gif",
	"image/jpeg":             "jpg",
	"image/png":              "png",
	"image/svg+xml":          "svg",
	"image/webp":             "webp",
	"text/css":               "css",
	"text/html":              "html",
	"text/javascript":        "js",
	"text/plain":             "txt",
	"text/xml":               "xml",
}

// extensionForType maps a content type, with any parameters, to a file
// extension, or to "" when the type is unknown.
func extensionForType(mimeType string) string {
	base, _, _ := strings.Cut(mimeType, ";")
	return extensions[strings.ToLower(strings.TrimSpace(base))]
}
