package command

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestHTML(t *testing.T) {
	srv := site(t)
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"the rendered document keeps its doctype", []string{srv.URL + "/redirected.html"}, "<!DOCTYPE html>\n<html lang=\"en\">"},
		{"a selector gives the element's outer HTML", []string{srv.URL + "/geometry.html", "-s", "#box-a"}, "<div class=\"box\" id=\"box-a\"></div>\n"},
		{"JavaScript runs before the snapshot", []string{srv.URL + "/geometry.html", "-j", "document.querySelector('#box-a').dataset.seen = 'yes'", "-s", "#box-a"}, "data-seen=\"yes\""},
		{"a wait lets late content in", []string{srv.URL + "/delayed.html", "--wait", "800", "-s", "#delayed"}, "<h1 id=\"delayed\">delayed</h1>\n"},
		{"a condition waits for late content", []string{srv.URL + "/delayed.html", "--wait-for", "window.shotsDelayedDone", "-s", "#delayed"}, "<h1 id=\"delayed\">delayed</h1>\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := run(t, append([]string{"html"}, tt.args...)...)
			if got.code != exitOK {
				t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
			}
			if !strings.Contains(string(got.stdout), tt.want) {
				t.Errorf("stdout = %q, want it to contain %q", got.stdout, tt.want)
			}
			if got.stderr != "" {
				t.Errorf("stderr = %q, want nothing", got.stderr)
			}
		})
	}
}

func TestHTMLMissingSelector(t *testing.T) {
	srv := site(t)
	got := run(t, "html", srv.URL+"/geometry.html", "-s", "#absent")
	if got.code != exitFailure {
		t.Fatalf("exit %d, want %d; stderr:\n%s", got.code, exitFailure, got.stderr)
	}
	if !strings.Contains(got.stderr, "no element matches #absent") {
		t.Errorf("stderr = %q", got.stderr)
	}
	if len(got.stdout) > 0 {
		t.Errorf("stdout = %q, want nothing", got.stdout)
	}
}

func TestHTMLFileOutput(t *testing.T) {
	srv := site(t)
	out := filepath.Join(t.TempDir(), "page.html")
	got := run(t, "html", srv.URL+"/redirected.html", "-o", out)
	if got.code != exitOK {
		t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "<h1 id=\"redirected\">redirected</h1>") {
		t.Errorf("file = %q", data)
	}
	if len(got.stdout) > 0 || !strings.Contains(got.stderr, strconv.Quote(out)) {
		t.Errorf("stdout = %q, stderr = %q", got.stdout, got.stderr)
	}
}
