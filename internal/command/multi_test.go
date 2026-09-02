package command

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mostlyserious/shots/internal/browser"
	"github.com/mostlyserious/shots/internal/config"
	"github.com/mostlyserious/shots/internal/har"
)

// writeConfig writes a configuration into dir and returns its path.
func writeConfig(t *testing.T, dir, yaml string) string {
	t.Helper()
	path := filepath.Join(dir, "shots.yml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// exists reports whether a path in dir is there.
func exists(t *testing.T, dir, name string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

func TestMultiEmptyConfiguration(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{"an empty file", ""},
		{"an empty list", "[]\n"},
		{"nothing but comments", "# nothing to do\n"},
		{"an explicit null document", "---\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			got := run(t, "multi", writeConfig(t, dir, tt.yaml))
			if got.code != exitOK {
				t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
			}
			names, _ := filepath.Glob(filepath.Join(dir, "*"))
			if len(names) != 1 {
				t.Errorf("a no-op left %v behind", names)
			}
		})
	}
}

// Configuration problems are status 2 with a field path, per PLAN.md Section 10.
func TestMultiConfigurationErrors(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{"an unknown key", "- url: http://example.com\n  nope: 1\n", "entries[0].nope (line 2): unknown field"},
		{"a missing url", "- output: out.png\n", "entries[0].url"},
		{"a bad value", "- url: http://example.com\n  width: -1\n", "entries[0].width"},
		{"an empty step", "- sh:\n", "entries[0].sh"},
		{"a mapping instead of a list", "url: http://example.com\n", "expected a list of entries"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			got := run(t, "multi", writeConfig(t, dir, tt.yaml))
			if got.code != exitUsage {
				t.Fatalf("exit %d, want %d; stderr:\n%s", got.code, exitUsage, got.stderr)
			}
			if !strings.Contains(got.stderr, tt.want) {
				t.Errorf("stderr = %q, want it to mention %q", got.stderr, tt.want)
			}
		})
	}
	t.Run("a configuration file that is not there fails", func(t *testing.T) {
		got := run(t, "multi", filepath.Join(t.TempDir(), "absent.yml"))
		if got.code != exitFailure {
			t.Fatalf("exit %d, want %d; stderr:\n%s", got.code, exitFailure, got.stderr)
		}
	})
	t.Run("a configuration arrives on standard input", func(t *testing.T) {
		got := runStdin(t, "- nope: 1\n", "multi", "-")
		if got.code != exitUsage || !strings.Contains(got.stderr, "entries[0].nope") {
			t.Fatalf("exit %d; stderr:\n%s", got.code, got.stderr)
		}
	})
}

// Steps run in source order, and a failed one stops the batch: everything after
// it was written expecting it to have run.
func TestMultiSteps(t *testing.T) {
	dir := t.TempDir()
	cfg := writeConfig(t, dir, strings.Join([]string{
		"- sh: " + childArgs(t, "write", filepath.Join(dir, "one.txt"), "1"),
		"- sh: " + childArgs(t, "exit", "3"),
		"- sh: " + childArgs(t, "write", filepath.Join(dir, "three.txt"), "3"),
	}, "\n"))
	got := run(t, "multi", cfg)
	if got.code != exitFailure {
		t.Fatalf("exit %d, want %d; stderr:\n%s", got.code, exitFailure, got.stderr)
	}
	if !strings.Contains(got.stderr, "exit status 3") {
		t.Errorf("stderr = %q, want the failing status", got.stderr)
	}
	if !exists(t, dir, "one.txt") {
		t.Error("the first step did not run")
	}
	if exists(t, dir, "three.txt") {
		t.Error("a step after the failing one ran")
	}
}

// A shell string goes through the platform shell, where a redirection means
// something; relative paths in it are the configuration's, not the caller's.
func TestMultiShellStep(t *testing.T) {
	dir := t.TempDir()
	cfg := writeConfig(t, dir, "- sh: echo written > written.txt\n")
	t.Chdir(t.TempDir())
	if got := run(t, "multi", cfg); got.code != exitOK {
		t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
	}
	data, err := os.ReadFile(filepath.Join(dir, "written.txt"))
	if err != nil {
		t.Fatalf("the step did not run beside its configuration: %v", err)
	}
	if strings.TrimSpace(string(data)) != "written" {
		t.Errorf("written.txt = %q", data)
	}
}

func TestMultiShots(t *testing.T) {
	srv := site(t)

	t.Run("every entry is taken in source order", func(t *testing.T) {
		dir := t.TempDir()
		cfg := writeConfig(t, dir, strings.Join([]string{
			"- url: " + srv.URL + "/geometry.html",
			"  output: box.png",
			"  height: 400",
			"- url: " + srv.URL + "/geometry.html",
			"  output: cropped.png",
			"  selector: '#box-a'",
			"",
		}, "\n"))
		if got := run(t, "multi", cfg); got.code != exitOK {
			t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
		}
		if w, h := bounds(t, filepath.Join(dir, "box.png")); w != 1280 || h != 400 {
			t.Errorf("box.png is %dx%d, want 1280x400", w, h)
		}
		if w, h := bounds(t, filepath.Join(dir, "cropped.png")); w != 100 || h != 50 {
			t.Errorf("cropped.png is %dx%d, want 100x50", w, h)
		}
	})

	// One browser context serves the whole run, as upstream's does, so what one
	// entry signs in or stores is there for the next.
	t.Run("state carries from one entry to the next", func(t *testing.T) {
		dir := t.TempDir()
		t.Chdir(dir)
		cfg := writeConfig(t, dir, strings.Join([]string{
			"- url: " + srv.URL + "/index.html",
			"  output: first.png",
			"  height: 100",
			"  javascript: localStorage.setItem('shots', 'carried')",
			"- url: " + srv.URL + "/index.html",
			"  output: second.png",
			"  height: 100",
			"  wait_for: localStorage.getItem('shots') === 'carried'",
			"",
		}, "\n"))
		if got := run(t, "multi", cfg, "--timeout", "5000"); got.code != exitOK {
			t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
		}
	})

	t.Run("output filters choose the entries", func(t *testing.T) {
		dir := t.TempDir()
		cfg := writeConfig(t, dir, strings.Join([]string{
			"- url: " + srv.URL + "/geometry.html",
			"  output: wanted.png",
			"  height: 100",
			"- url: " + srv.URL + "/geometry.html",
			"  output: unwanted.png",
			"  height: 100",
			"",
		}, "\n"))
		if got := run(t, "multi", cfg, "-o", "wanted.png"); got.code != exitOK {
			t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
		}
		if !exists(t, dir, "wanted.png") {
			t.Error("the wanted shot was not taken")
		}
		if exists(t, dir, "unwanted.png") {
			t.Error("a filtered-out shot was taken")
		}
	})

	t.Run("no-clobber refuses to replace a file", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "keep.png"), []byte("keep me"), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg := writeConfig(t, dir, "- url: "+srv.URL+"/geometry.html\n  output: keep.png\n  height: 100\n")
		if got := run(t, "multi", cfg, "-n"); got.code != exitFailure {
			t.Fatalf("exit %d, want %d; stderr:\n%s", got.code, exitFailure, got.stderr)
		}
		if data, _ := os.ReadFile(filepath.Join(dir, "keep.png")); string(data) != "keep me" {
			t.Error("the existing file was overwritten")
		}
	})

	t.Run("a failed shot is reported and the batch carries on", func(t *testing.T) {
		dir := t.TempDir()
		cfg := writeConfig(t, dir, strings.Join([]string{
			"- url: " + srv.URL + "/geometry.html",
			"  output: missing.png",
			"  selector: '#absent'",
			"- url: " + srv.URL + "/geometry.html",
			"  output: taken.png",
			"  height: 100",
			"",
		}, "\n"))
		got := run(t, "multi", cfg)
		if got.code != exitFailure {
			t.Fatalf("exit %d, want %d; stderr:\n%s", got.code, exitFailure, got.stderr)
		}
		if !exists(t, dir, "taken.png") {
			t.Error("the batch stopped at the failing shot")
		}
		if exists(t, dir, "missing.png") {
			t.Error("the failing shot left a file behind")
		}
		if !strings.Contains(got.stderr, "1 of 2 entries failed") {
			t.Errorf("stderr = %q, want a summary", got.stderr)
		}
	})

	t.Run("fail-on-error stops at the first failure", func(t *testing.T) {
		dir := t.TempDir()
		cfg := writeConfig(t, dir, strings.Join([]string{
			"- url: " + srv.URL + "/geometry.html",
			"  output: missing.png",
			"  selector: '#absent'",
			"- url: " + srv.URL + "/geometry.html",
			"  output: taken.png",
			"  height: 100",
			"",
		}, "\n"))
		if got := run(t, "multi", cfg, "--fail-on-error"); got.code != exitFailure {
			t.Fatalf("exit %d, want %d; stderr:\n%s", got.code, exitFailure, got.stderr)
		}
		if exists(t, dir, "taken.png") {
			t.Error("the batch carried on past the failure")
		}
	})

	// The whole batch shares one archive, and an entry with no output loads its
	// page for the traffic without writing an image.
	t.Run("one HAR records the session", func(t *testing.T) {
		dir := t.TempDir()
		t.Chdir(dir)
		cfg := writeConfig(t, dir, strings.Join([]string{
			"- url: " + srv.URL + "/index.html",
			"- url: " + srv.URL + "/geometry.html",
			"  output: shot.png",
			"  height: 100",
			"",
		}, "\n"))
		if got := run(t, "multi", cfg, "--har-file", "trace.har"); got.code != exitOK {
			t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
		}
		data, err := os.ReadFile(filepath.Join(dir, "trace.har"))
		if err != nil {
			t.Fatal(err)
		}
		var archive struct {
			Log har.Log `json:"log"`
		}
		if err := json.Unmarshal(data, &archive); err != nil {
			t.Fatal(err)
		}
		var urls []string
		for _, entry := range archive.Log.Entries {
			urls = append(urls, entry.Request.URL)
		}
		for _, want := range []string{srv.URL + "/index.html", srv.URL + "/geometry.html"} {
			if !slices.Contains(urls, want) {
				t.Errorf("the archive is missing %q, has %v", want, urls)
			}
		}
		names, _ := filepath.Glob(filepath.Join(dir, "*.png"))
		if len(names) != 1 {
			t.Errorf("want only the entry with an output written, found %v", names)
		}
	})
}

func TestMultiStableReports(t *testing.T) {
	srv := site(t)
	dir := t.TempDir()
	cfg := writeConfig(t, dir, strings.Join([]string{
		"- url: " + srv.URL + "/console.html",
		"  output: first.png",
		"  height: 100",
		`  wait_for: "matchMedia('(prefers-reduced-motion: reduce)').matches"`,
		"- url: " + srv.URL + "/geometry.html",
		"  output: missing.png",
		"  selector: '#absent'",
		"",
	}, "\n"))
	got := run(t, "multi", cfg, "--stable", "--report", "-")
	if got.code != exitFailure {
		t.Fatalf("exit %d, want %d; stderr:\n%s", got.code, exitFailure, got.stderr)
	}
	lines := slices.Collect(strings.Lines(strings.TrimSpace(string(got.stdout))))
	if len(lines) != 2 {
		t.Fatalf("reports = %d, want 2:\n%s", len(lines), got.stdout)
	}
	first, second := readShotReport(t, []byte(lines[0])), readShotReport(t, []byte(lines[1]))
	if !first.OK || first.Output != filepath.Join(dir, "first.png") {
		t.Errorf("first report = %+v", first)
	}
	if !slices.ContainsFunc(first.Console, func(message browser.Message) bool {
		return message.Type == "log" && message.Text == "log message"
	}) {
		t.Errorf("first console = %+v, want log message", first.Console)
	}
	if second.OK || second.Error == "" || second.Output != filepath.Join(dir, "missing.png") {
		t.Errorf("second report = %+v", second)
	}
}

// A server is started, waited for, used, and stopped again.
func TestMultiServer(t *testing.T) {
	site(t) // only for the browser check

	// serverConfig is the canonical layout: a server, then the page it serves.
	// The server only starts listening after a delay, so a batch that did not
	// wait for it would fail to navigate.
	serverConfig := func(t *testing.T, dir, addr string) string {
		return writeConfig(t, dir, strings.Join([]string{
			"- server: " + childArgs(t, "serve", addr, "700"),
			"- url: http://" + addr + "/",
			"  output: served.png",
			"  selector: '#served'",
			"",
		}, "\n"))
	}

	t.Run("the batch waits for it and stops it", func(t *testing.T) {
		dir, addr := t.TempDir(), freeAddr(t)
		if got := run(t, "multi", serverConfig(t, dir, addr)); got.code != exitOK {
			t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
		}
		if !exists(t, dir, "served.png") {
			t.Error("the shot of the served page is missing")
		}
		if answers(addr) {
			t.Error("the server is still running after the batch")
		}
	})

	// The real binary, because what is being tested is what happens to the
	// server once the shots process is gone: a server that inherited a pipe
	// shots owned is killed by SIGPIPE the first time it logs a request.
	t.Run("leave-server keeps it running", func(t *testing.T) {
		dir, addr := t.TempDir(), freeAddr(t)
		// Standard error goes to a file, which the left-behind server inherits
		// and can keep writing to.
		log, err := os.Create(filepath.Join(dir, "stderr"))
		if err != nil {
			t.Fatal(err)
		}
		defer log.Close()
		cmd := exec.Command(binary(t), "multi", serverConfig(t, dir, addr), "--leave-server")
		cmd.Stderr = log
		if err := cmd.Run(); err != nil {
			stderr, _ := os.ReadFile(filepath.Join(dir, "stderr"))
			t.Fatalf("%v; stderr:\n%s", err, stderr)
		}
		if err := get(addr); err != nil {
			t.Fatalf("--leave-server stopped the server anyway: %v", err)
		}
		if err := get(addr); err != nil {
			t.Fatalf("the server died after logging a request: %v", err)
		}
	})
}

func TestServerAddress(t *testing.T) {
	// Each entry is "URL" or "server URL"; the first one is the entry holding
	// the server whose address is being looked for.
	tests := []struct {
		name    string
		entries []string
		want    string
	}{
		{"the port of the next entry", []string{"", "http://localhost:8000/x"}, "localhost:8000"},
		{"the entry's own url", []string{"http://127.0.0.1:9999/"}, "127.0.0.1:9999"},
		{"http without a port", []string{"http://example.com/"}, "example.com:80"},
		{"https without a port", []string{"https://example.com/"}, "example.com:443"},
		{"a bare host is normalized first", []string{"", "localhost:8000/"}, "localhost:8000"},
		{"a bare domain gets http's port", []string{"", "example.com/x"}, "example.com:80"},
		{"nothing to dial", []string{"", ""}, ""},
		{"a file url is not an address", []string{"file:///tmp/x.html"}, ""},
		{"the next server's url is not this one's", []string{"", "serve2 http://localhost:8812/"}, ""},
		{"a url of its own still counts", []string{"http://localhost:8811/", "serve2 http://localhost:8812/"}, "localhost:8811"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entries := make([]config.Entry, len(tt.entries))
			for i, spec := range tt.entries {
				if server, target, ok := strings.Cut(spec, " "); ok {
					entries[i].Server = config.Command{Shell: server}
					spec = target
				}
				entries[i].URL = spec
			}
			if got := serverAddress(entries); got != tt.want {
				t.Errorf("serverAddress = %q, want %q", got, tt.want)
			}
		})
	}
}

// The browser is acquired once, before the first entry. An unreachable one ends
// the run with the single real cause, rather than retrying — and orphaning a
// launched Chrome — for every entry; a configuration with no shot in it never
// reaches for one at all.
func TestMultiAcquiresTheBrowserOnce(t *testing.T) {
	t.Run("a failure ends the run", func(t *testing.T) {
		dir := t.TempDir()
		t.Chdir(dir)
		cfg := writeConfig(t, dir, `- url: http://localhost:1/
  output: one.png
- url: http://localhost:1/
  output: two.png
- url: http://localhost:1/
  output: three.png
`)
		got := run(t, "multi", cfg, "--cdp-url", "http://"+freeAddr(t)+"/")
		if got.code != exitFailure {
			t.Fatalf("exit %d, want %d, stderr:\n%s", got.code, exitFailure, got.stderr)
		}
		if n := strings.Count(got.stderr, "connecting to"); n != 1 {
			t.Errorf("tried to connect %d times, want once:\n%s", n, got.stderr)
		}
		if strings.Contains(got.stderr, "entries failed") {
			t.Errorf("the one real cause was buried under per-entry failures:\n%s", got.stderr)
		}
	})
	t.Run("a step-only configuration needs no browser", func(t *testing.T) {
		dir := t.TempDir()
		t.Chdir(dir)
		cfg := writeConfig(t, dir, "- sh: echo ran\n")
		if got := run(t, "multi", cfg, "--cdp-url", "http://"+freeAddr(t)+"/"); got.code != exitOK {
			t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
		}
	})
}
