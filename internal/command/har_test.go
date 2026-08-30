package command

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mostlyserious/shots/internal/har"
)

// readHAR reads a HAR file, checks it against the 1.2 specification and
// returns both the decoded log and the raw bytes.
func readHAR(t *testing.T, path string) har.Log {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	validateHAR(t, data)
	var document struct {
		Log har.Log `json:"log"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return document.Log
}

// validateHAR checks the fields HAR 1.2 requires, against the bytes on disk
// rather than against the structs that wrote them.
func validateHAR(t *testing.T, data []byte) {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("%v in:\n%s", err, data)
	}
	log := object(t, document, "log")
	if log["version"] != "1.2" {
		t.Errorf("log version = %v, want 1.2", log["version"])
	}
	creator := object(t, log, "creator")
	for _, field := range []string{"name", "version"} {
		if _, ok := creator[field].(string); !ok {
			t.Errorf("creator %s = %v, want a string", field, creator[field])
		}
	}
	entries, ok := log["entries"].([]any)
	if !ok {
		t.Fatalf("log entries = %v, want a list", log["entries"])
	}
	for i, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("entry %d = %v, want an object", i, raw)
		}
		if _, err := time.Parse(time.RFC3339, str(t, entry, "startedDateTime")); err != nil {
			t.Errorf("entry %d startedDateTime: %v", i, err)
		}
		object(t, entry, "cache")
		request := object(t, entry, "request")
		for _, field := range []string{"method", "url", "httpVersion"} {
			str(t, request, field)
		}
		for _, field := range []string{"cookies", "headers", "queryString"} {
			list(t, request, field)
		}
		response := object(t, entry, "response")
		for _, field := range []string{"statusText", "httpVersion", "redirectURL"} {
			str(t, response, field)
		}
		for _, field := range []string{"cookies", "headers"} {
			list(t, response, field)
		}
		number(t, response, "status")
		content := object(t, response, "content")
		number(t, content, "size")
		str(t, content, "mimeType")
		for _, field := range []string{"headersSize", "bodySize"} {
			number(t, request, field)
			number(t, response, field)
		}
		timings := object(t, entry, "timings")
		var sum float64
		for _, field := range []string{"blocked", "dns", "connect", "send", "wait", "receive"} {
			if value := number(t, timings, field); value > 0 {
				sum += value
			}
		}
		for _, field := range []string{"send", "wait", "receive"} {
			if _, ok := timings[field]; !ok {
				t.Errorf("entry %d timings are missing %s", i, field)
			}
		}
		// The specification defines an entry's time as the sum of its
		// non-negative timings.
		if got := number(t, entry, "time"); got != sum {
			t.Errorf("entry %d time = %v, want %v", i, got, sum)
		}
	}
}

func object(t *testing.T, in map[string]any, field string) map[string]any {
	t.Helper()
	value, ok := in[field].(map[string]any)
	if !ok {
		t.Fatalf("%s = %v, want an object", field, in[field])
	}
	return value
}

func list(t *testing.T, in map[string]any, field string) []any {
	t.Helper()
	value, ok := in[field].([]any)
	if !ok {
		t.Fatalf("%s = %v, want a list", field, in[field])
	}
	return value
}

func str(t *testing.T, in map[string]any, field string) string {
	t.Helper()
	value, ok := in[field].(string)
	if !ok {
		t.Fatalf("%s = %v, want a string", field, in[field])
	}
	return value
}

func number(t *testing.T, in map[string]any, field string) float64 {
	t.Helper()
	value, ok := in[field].(float64)
	if !ok {
		t.Fatalf("%s = %v, want a number", field, in[field])
	}
	return value
}

// entryFor returns the entry for one request URL.
func entryFor(t *testing.T, log har.Log, url string) *har.Entry {
	t.Helper()
	var urls []string
	for _, entry := range log.Entries {
		if entry.Request.URL == url {
			return entry
		}
		urls = append(urls, entry.Request.URL)
	}
	t.Fatalf("no entry for %q in:\n%s", url, strings.Join(urls, "\n"))
	return nil
}

func TestHARRecordsTestsiteTraffic(t *testing.T) {
	srv := site(t)
	output := filepath.Join(t.TempDir(), "trace.har")
	got := run(t, "har", srv.URL+"/network.html", "-o", output, "--wait-for", "window.done")
	if got.code != exitOK {
		t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
	}
	log := readHAR(t, output)

	document := entryFor(t, log, srv.URL+"/network.html")
	if document.Response.Status != 200 || !strings.Contains(document.Response.Content.Text, `id="network"`) {
		t.Errorf("document entry = %d, body %q", document.Response.Status, document.Response.Content.Text)
	}
	if document.Request.Method != "GET" || document.Timings.Wait < 0 {
		t.Errorf("document request = %+v, timings %+v", document.Request, document.Timings)
	}

	// A subresource body is fetched eagerly, so it survives everything the
	// page does afterwards.
	style := entryFor(t, log, srv.URL+"/network.css")
	if !strings.Contains(style.Response.Content.Text, "#network") {
		t.Errorf("stylesheet body = %q", style.Response.Content.Text)
	}
	if style.Response.Content.Size != int64(len(style.Response.Content.Text)) {
		t.Errorf("stylesheet size = %d, body is %d bytes", style.Response.Content.Size, len(style.Response.Content.Text))
	}

	redirect := entryFor(t, log, srv.URL+"/redirect?to=/redirected.html")
	if redirect.Response.Status != 302 || !strings.HasSuffix(redirect.Response.RedirectURL, "/redirected.html") {
		t.Errorf("redirect entry = %d to %q", redirect.Response.Status, redirect.Response.RedirectURL)
	}
	if entryFor(t, log, srv.URL+"/redirected.html").Response.Status != 200 {
		t.Error("the redirect target is missing its own entry")
	}
	if status := entryFor(t, log, srv.URL+"/status/404").Response.Status; status != 404 {
		t.Errorf("error entry status = %d, want 404", status)
	}
	if failed := entryFor(t, log, srv.URL+"/reset"); failed.FailureText == "" || failed.Response.Status != 0 {
		t.Errorf("failed entry = %+v", failed)
	}
}

func TestHARZip(t *testing.T) {
	srv := site(t)
	output := filepath.Join(t.TempDir(), "trace.har.zip")
	got := run(t, "har", srv.URL+"/", "-z", "-o", output)
	if got.code != exitOK {
		t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
	}
	archive, err := zip.OpenReader(output)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	if len(archive.File) != 1 || archive.File[0].Name != "har.har" {
		t.Fatalf("archive holds %d members, want one har.har", len(archive.File))
	}
}

func TestHARExtract(t *testing.T) {
	srv := site(t)
	base := filepath.Join(t.TempDir(), "site")
	// A base path without a HAR extension names both the file and the
	// directory, as upstream's -x does.
	got := run(t, "har", srv.URL+"/network.html", "-x", "-o", base, "--wait-for", "window.done")
	if got.code != exitOK {
		t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
	}
	readHAR(t, base+".har")
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	for _, want := range []string{".html", ".css"} {
		found := false
		for _, name := range names {
			found = found || strings.HasSuffix(name, want)
		}
		if !found {
			t.Errorf("no %s resource in %v", want, names)
		}
	}
	// Resources are named after their URL, not numbered.
	found := false
	for _, name := range names {
		found = found || strings.HasSuffix(name, "-network.css")
	}
	if !found {
		t.Errorf("no resource named after /network.css in %v", names)
	}
}

func TestHARFlagsOnShot(t *testing.T) {
	srv := site(t)
	dir := t.TempDir()
	t.Chdir(dir)
	got := run(t, "shot", srv.URL+"/network.html", "-o", "shot.png", "--har")
	if got.code != exitOK {
		t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "shot.png")); err != nil {
		t.Error(err)
	}
	// --har alone writes trace.har, as upstream's does.
	log := readHAR(t, filepath.Join(dir, "trace.har"))
	if len(log.Entries) == 0 {
		t.Error("trace.har has no entries")
	}
	if !strings.Contains(got.stderr, "Wrote to HAR file") {
		t.Errorf("stderr does not report the HAR file:\n%s", got.stderr)
	}
}

func TestHARFileZipsByExtension(t *testing.T) {
	srv := site(t)
	output := filepath.Join(t.TempDir(), "trace.har.zip")
	got := run(t, "shot", srv.URL+"/", "-o", filepath.Join(t.TempDir(), "shot.png"), "--har-file", output)
	if got.code != exitOK {
		t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
	}
	archive, err := zip.OpenReader(output)
	if err != nil {
		t.Fatal(err)
	}
	archive.Close()
}

func TestHARFlagValidation(t *testing.T) {
	got := run(t, "har", "example.com", "-x", "-o", "-")
	if got.code != exitUsage || !strings.Contains(got.stderr, "--extract") {
		t.Errorf("exit %d, stderr:\n%s", got.code, got.stderr)
	}
}
