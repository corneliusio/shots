package command

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPageLifecycle checks that the steps every page command shares behave the
// same way whichever command drives them.
func TestPageLifecycle(t *testing.T) {
	srv := site(t)
	commands := []string{"javascript", "html", "pdf", "accessibility"}

	t.Run("skip exits successfully without output", func(t *testing.T) {
		for _, name := range commands {
			out := filepath.Join(t.TempDir(), "artifact")
			got := run(t, name, srv.URL+"/status/404", "-j", "document.title", "--skip", "-o", out)
			if got.code != exitOK {
				t.Errorf("%s: exit %d, stderr:\n%s", name, got.code, got.stderr)
			}
			if !strings.Contains(got.stderr, "404 error") {
				t.Errorf("%s: stderr = %q, want the skipped status", name, got.stderr)
			}
			if _, err := os.Stat(out); err == nil {
				t.Errorf("%s: a skipped run left an output file behind", name)
			}
		}
	})

	t.Run("fail reports the status", func(t *testing.T) {
		for _, name := range commands {
			out := filepath.Join(t.TempDir(), "artifact")
			got := run(t, name, srv.URL+"/status/500", "-j", "document.title", "--fail", "-o", out)
			if got.code != exitFailure || !strings.Contains(got.stderr, "500 error") {
				t.Errorf("%s: exit %d, stderr:\n%s", name, got.code, got.stderr)
			}
			if _, err := os.Stat(out); err == nil {
				t.Errorf("%s: a failed run left an output file behind", name)
			}
		}
	})

	t.Run("skip and fail together are a usage error", func(t *testing.T) {
		for _, name := range commands {
			got := run(t, name, srv.URL+"/index.html", "-j", "document.title", "--skip", "--fail")
			if got.code != exitUsage {
				t.Errorf("%s: exit %d, want %d", name, got.code, exitUsage)
			}
		}
	})
}

func TestPageScriptFile(t *testing.T) {
	srv := site(t)
	script := filepath.Join(t.TempDir(), "mark.js")
	if err := os.WriteFile(script, []byte("() => { document.querySelector('#box-a').dataset.seen = 'yes'; }"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("a screenshot runs the file", func(t *testing.T) {
		out := shot(t, srv.URL+"/geometry.html", "--js-file", script, "-s", "[data-seen]")
		if w, h := bounds(t, out); w != 100 || h != 50 {
			t.Errorf("got %dx%d, want the marked element at 100x50", w, h)
		}
	})

	t.Run("--javascript and --js-file conflict", func(t *testing.T) {
		got := runTake(t, srv.URL+"/geometry.html", "-j", "1", "--js-file", script, "-o", "-")
		if got.code != exitUsage || !strings.Contains(got.stderr, "cannot be used together") {
			t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
		}
	})
}
