package command

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// passthrough are the URL schemes a target may already carry. Anything else is
// treated as a bare host, because "example.com:8080/x" parses as a URL with the
// scheme "example.com".
var passthrough = []string{"http://", "https://", "file://", "about:", "data:"}

// NormalizeURL turns a command-line target into a URL: a path that exists
// becomes an absolute file:// URL, a target that already names a supported
// scheme is left alone, and anything else gets http://.
func NormalizeURL(target string) (string, error) {
	if strings.TrimSpace(target) == "" {
		return "", usagef("a URL or path is required")
	}
	if info, err := os.Stat(target); err == nil && !info.IsDir() {
		abs, err := filepath.Abs(target)
		if err != nil {
			return "", err
		}
		// Windows paths ("C:/x.html") need the leading slash a file URL wants.
		slashed := filepath.ToSlash(abs)
		if !strings.HasPrefix(slashed, "/") {
			slashed = "/" + slashed
		}
		return (&url.URL{Scheme: "file", Path: slashed}).String(), nil
	}
	for _, scheme := range passthrough {
		if strings.HasPrefix(strings.ToLower(target), scheme) {
			return target, nil
		}
	}
	return "http://" + target, nil
}
