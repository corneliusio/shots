package command

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	_ "image/jpeg"
	"image/png"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mostlyserious/shots/internal/browser"
	"github.com/mostlyserious/shots/internal/process"
	"github.com/mostlyserious/shots/internal/testsite"
)

// result is one CLI invocation.
type result struct {
	code   int
	stdout []byte
	stderr string
}

// run invokes the whole CLI, exactly as main does.
func run(t *testing.T, args ...string) result {
	t.Helper()
	return runStdin(t, "", args...)
}

// runStdin is [run] with something on standard input.
func runStdin(t *testing.T, stdin string, args ...string) result {
	t.Helper()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Main(context.Background(), args, strings.NewReader(stdin), &stdout, &stderr)
	return result{code: code, stdout: stdout.Bytes(), stderr: stderr.String()}
}

// site starts the deterministic test site, skipping the test when no browser is
// available to point at it.
func site(t *testing.T) *httptest.Server {
	t.Helper()
	if _, err := browser.Discover(""); err != nil {
		t.Skipf("no browser: %v", err)
	}
	srv := testsite.New()
	t.Cleanup(srv.Close)
	return srv
}

// bounds decodes an image file and returns its pixel dimensions.
func bounds(t *testing.T, path string) (int, int) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cfg, format, err := image.DecodeConfig(f)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	t.Logf("%s is %s %dx%d", filepath.Base(path), format, cfg.Width, cfg.Height)
	return cfg.Width, cfg.Height
}

func shot(t *testing.T, args ...string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "shot.png")
	got := run(t, append(args, "-o", out)...)
	if got.code != 0 {
		t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
	}
	return out
}

func TestShotGeometry(t *testing.T) {
	srv := site(t)
	tests := []struct {
		name          string
		args          []string
		width, height int
	}{
		// geometry.html is a 1280x2000 page of exactly positioned boxes.
		{"full page when no height is given", nil, 1280, 2000},
		{"a height fixes the viewport", []string{"--height", "400"}, 1280, 400},
		{"width narrows the viewport", []string{"-w", "600", "--height", "400"}, 600, 400},
		{"retina doubles every pixel", []string{"--retina", "--height", "400"}, 2560, 800},
		{"scale factor multiplies", []string{"--scale-factor", "1.5", "--height", "400"}, 1920, 600},
		{"a selector crops to the element", []string{"-s", "#box-a"}, 100, 50},
		{"padding grows the crop on all sides", []string{"-s", "#box-a", "-p", "10"}, 120, 70},
		// #box-a is 10,20 100x50 and #box-b is 200,300 80x40, so together they
		// span x 10..280 and y 20..340.
		{"repeated selectors combine", []string{"-s", "#box-a", "-s", "#box-b"}, 270, 320},
		// The three .item boxes are 30x30 at x 400, 440, 480 and y 100.
		{"selector-all takes every match", []string{"--selector-all", ".item"}, 110, 30},
		{"a js selector picks one element", []string{"--js-selector", "el.id === 'box-b'"}, 80, 40},
		{"a js selector-all takes every match", []string{"--js-selector-all", "el.className === 'item'"}, 110, 30},
		// #box-c is at y 1900, below the 720px viewport.
		{"a selector below the fold still crops", []string{"-s", "#box-c"}, 60, 60},
		{"a selector is captured at the scale factor", []string{"--retina", "-s", "#box-a"}, 200, 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := shot(t, append([]string{srv.URL + "/geometry.html"}, tt.args...)...)
			w, h := bounds(t, out)
			if w != tt.width || h != tt.height {
				t.Errorf("got %dx%d, want %dx%d", w, h, tt.width, tt.height)
			}
		})
	}
}

func TestShotTransparency(t *testing.T) {
	srv := site(t)
	for _, tt := range []struct {
		name       string
		args       []string
		wantOpaque bool
	}{
		{"the page background is white by default", nil, true},
		{"omit-background leaves it transparent", []string{"--omit-background"}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out := shot(t, append([]string{srv.URL + "/transparent.html", "--height", "200"}, tt.args...)...)
			f, err := os.Open(out)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			img, err := png.Decode(f)
			if err != nil {
				t.Fatal(err)
			}
			// #square is 100x100 in the corner; (200,150) is bare background.
			_, _, _, background := img.At(200, 150).RGBA()
			if opaque := background == 0xffff; opaque != tt.wantOpaque {
				t.Errorf("background alpha %d, opaque = %v, want %v", background, opaque, tt.wantOpaque)
			}
			// The square itself is magenta either way.
			r, g, b, a := img.At(50, 50).RGBA()
			if a == 0 || r == 0 || b == 0 || g != 0 {
				t.Errorf("square pixel = %d,%d,%d,%d, want opaque magenta", r, g, b, a)
			}
		})
	}
}

func TestShotWaits(t *testing.T) {
	srv := site(t)
	// #delayed only appears 300ms after load, so cropping to it fails unless
	// the shot waited.
	url := srv.URL + "/delayed.html"
	tests := []struct {
		name string
		args []string
		want int
	}{
		{"without a wait the element is missing", nil, exitFailure},
		{"a fixed wait is long enough", []string{"--wait", "800"}, exitOK},
		{"a condition waits for the element", []string{"--wait-for", "document.querySelector('#delayed')"}, exitOK},
		{"a condition may be a function", []string{"--wait-for", "() => window.shotsDelayedDone"}, exitOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "shot.png")
			got := run(t, append([]string{url, "-s", "#delayed", "-o", out}, tt.args...)...)
			if got.code != tt.want {
				t.Fatalf("exit %d, want %d; stderr:\n%s", got.code, tt.want, got.stderr)
			}
			if tt.want != exitOK {
				if _, err := os.Stat(out); err == nil {
					t.Error("a failed shot left an output file behind")
				}
			}
		})
	}
}

func TestShotSkipAndFail(t *testing.T) {
	srv := site(t)
	tests := []struct {
		name      string
		path      string
		args      []string
		want      int
		wantShot  bool
		wantError string
	}{
		{"an error page is captured by default", "/status/404", nil, exitOK, true, ""},
		{"skip exits successfully without a shot", "/status/404", []string{"--skip"}, exitOK, false, "skipping"},
		{"fail reports the status", "/status/500", []string{"--fail"}, exitFailure, false, "500 error"},
		{"a good page is unaffected by skip", "/index.html", []string{"--skip"}, exitOK, true, ""},
		{"a good page is unaffected by fail", "/index.html", []string{"--fail"}, exitOK, true, ""},
		{"skip and fail together are a usage error", "/index.html", []string{"--skip", "--fail"}, exitUsage, false, "cannot be used together"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "shot.png")
			got := run(t, append([]string{srv.URL + tt.path, "-o", out}, tt.args...)...)
			if got.code != tt.want {
				t.Fatalf("exit %d, want %d; stderr:\n%s", got.code, tt.want, got.stderr)
			}
			if _, err := os.Stat(out); (err == nil) != tt.wantShot {
				t.Errorf("output file exists = %v, want %v", err == nil, tt.wantShot)
			}
			if tt.wantError != "" && !strings.Contains(got.stderr, tt.wantError) {
				t.Errorf("stderr = %q, want it to mention %q", got.stderr, tt.wantError)
			}
		})
	}
}

func TestShotUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"retina with a scale factor", []string{"x.html", "--retina", "--scale-factor", "2"}},
		{"a negative scale factor", []string{"x.html", "--scale-factor", "-1"}},
		{"a zero width", []string{"x.html", "-w", "0"}},
		{"a quality above the range", []string{"x.html", "--quality", "101"}},
		{"a negative quality", []string{"x.html", "--quality", "-1"}},
		{"a negative wait", []string{"x.html", "--wait", "-1"}},
		{"an unknown flag", []string{"x.html", "--nope"}},
		{"no arguments at all", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := run(t, tt.args...); got.code != exitUsage {
				t.Errorf("exit %d, want %d; stderr:\n%s", got.code, exitUsage, got.stderr)
			}
		})
	}
}

func TestShotOutput(t *testing.T) {
	srv := site(t)

	t.Run("stdout carries the image alone", func(t *testing.T) {
		got := run(t, srv.URL+"/geometry.html", "--height", "100", "-o", "-")
		if got.code != 0 {
			t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
		}
		if !bytes.HasPrefix(got.stdout, []byte("\x89PNG\r\n\x1a\n")) {
			t.Errorf("stdout is not a PNG: %q", got.stdout[:min(16, len(got.stdout))])
		}
		if _, _, err := image.DecodeConfig(bytes.NewReader(got.stdout)); err != nil {
			t.Errorf("stdout does not decode: %v", err)
		}
	})

	t.Run("quality selects JPEG", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "shot.jpg")
		if got := run(t, srv.URL+"/geometry.html", "--height", "100", "--quality", "60", "-o", out); got.code != 0 {
			t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
		}
		f, err := os.Open(out)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, format, err := image.DecodeConfig(f); err != nil || format != "jpeg" {
			t.Errorf("format = %q (%v), want jpeg", format, err)
		}
	})

	t.Run("a jpg extension selects JPEG", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "shot.jpg")
		if got := run(t, srv.URL+"/geometry.html", "--height", "100", "-o", out); got.code != 0 {
			t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
		}
		f, err := os.Open(out)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, format, err := image.DecodeConfig(f); err != nil || format != "jpeg" {
			t.Errorf("format = %q (%v), want jpeg", format, err)
		}
	})

	t.Run("an omitted output is named after the URL", func(t *testing.T) {
		t.Chdir(t.TempDir())
		if got := run(t, srv.URL+"/geometry.html", "--height", "100"); got.code != 0 {
			t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
		}
		matches, err := filepath.Glob("*-geometry-html.png")
		if err != nil || len(matches) != 1 {
			names, _ := filepath.Glob("*")
			t.Fatalf("want one *-geometry-html.png, found %v", names)
		}
	})

	t.Run("no-clobber refuses to replace a file", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "shot.png")
		if err := os.WriteFile(out, []byte("keep me"), 0o644); err != nil {
			t.Fatal(err)
		}
		got := run(t, srv.URL+"/geometry.html", "--height", "100", "-o", out, "--no-clobber")
		if got.code != exitFailure {
			t.Fatalf("exit %d, want %d", got.code, exitFailure)
		}
		if data, _ := os.ReadFile(out); string(data) != "keep me" {
			t.Error("the existing file was overwritten")
		}
	})
}

func TestShotLocalFile(t *testing.T) {
	site(t) // only for the browser check
	dir := t.TempDir()
	page := filepath.Join(dir, "local.html")
	body := "<!doctype html><style>html,body{margin:0}#b{width:120px;height:60px;background:#0a0}</style><div id=b></div>"
	if err := os.WriteFile(page, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	out := shot(t, page, "-s", "#b")
	if w, h := bounds(t, out); w != 120 || h != 60 {
		t.Errorf("got %dx%d, want 120x60", w, h)
	}
}

func TestShotJavaScript(t *testing.T) {
	srv := site(t)
	tests := []struct {
		name   string
		source string
		want   int
	}{
		{"an expression runs", "document.querySelector('#box-a').dataset.shot = '1'", exitOK},
		{"a function is called", "() => { document.querySelector('#box-a').dataset.shot = '1'; }", exitOK},
		// The value of a before script is never wanted, so a script ending in
		// something JSON cannot hold must not fail the shot.
		{"a value JSON cannot hold is discarded", "document.querySelector('#box-a').dataset.shot = '1'; window", exitOK},
		{"an exception fails the run", "throw new Error('boom')", exitFailure},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "shot.png")
			got := run(t, srv.URL+"/geometry.html", "-j", tt.source, "-s", "[data-shot]", "-o", out)
			if got.code != tt.want {
				t.Fatalf("exit %d, want %d; stderr:\n%s", got.code, tt.want, got.stderr)
			}
			if tt.want == exitFailure && !strings.Contains(got.stderr, "boom") {
				t.Errorf("stderr = %q, want it to mention the exception", got.stderr)
			}
		})
	}
}

func TestShotBasicAuth(t *testing.T) {
	srv := site(t)
	t.Run("credentials answer the challenge", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "shot.png")
		got := run(t, srv.URL+"/basic-auth", "--height", "100", "--fail", "-o", out,
			"--auth-username", testsite.BasicAuthUser, "--auth-password", testsite.BasicAuthPassword)
		if got.code != exitOK {
			t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
		}
	})
	t.Run("without credentials the page is unauthorized", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "shot.png")
		got := run(t, srv.URL+"/basic-auth", "--height", "100", "--fail", "-o", out)
		if got.code != exitFailure || !strings.Contains(got.stderr, "401") {
			t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
		}
	})
}

func TestShotLogging(t *testing.T) {
	srv := site(t)

	t.Run("console messages reach stderr", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "shot.png")
		got := run(t, srv.URL+"/console.html", "--height", "100", "--log-console", "-o", out)
		if got.code != 0 {
			t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
		}
		for _, want := range []string{"log: log message", "warning: warn message", "error: error message"} {
			if !strings.Contains(got.stderr, want) {
				t.Errorf("stderr = %q, want it to contain %q", got.stderr, want)
			}
		}
	})

	t.Run("requests are logged as JSON lines", func(t *testing.T) {
		dir := t.TempDir()
		log := filepath.Join(dir, "requests.json")
		got := run(t, srv.URL+"/geometry.html", "--height", "100", "--log-requests", log, "-o", filepath.Join(dir, "shot.png"))
		if got.code != 0 {
			t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
		}
		data, err := os.ReadFile(log)
		if err != nil {
			t.Fatal(err)
		}
		var found bool
		for line := range strings.Lines(strings.TrimSpace(string(data))) {
			var r browser.Request
			if err := json.Unmarshal([]byte(line), &r); err != nil {
				t.Fatalf("%q: %v", line, err)
			}
			if strings.HasSuffix(r.URL, "/geometry.html") {
				found = true
				if r.Method != "GET" || r.Status != 200 {
					t.Errorf("document entry = %+v, want GET 200", r)
				}
			}
		}
		if !found {
			t.Errorf("no entry for the document in:\n%s", data)
		}
	})

	// /busy.html keeps logging and fetching, so events land while the main
	// goroutine is writing the progress line and the image. Under -race these
	// catch a callback that writes to a stream without synchronization.
	t.Run("console logging does not race the progress line", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "shot.png")
		got := run(t, srv.URL+"/busy.html", "--height", "100", "--log-console", "-o", out)
		if got.code != 0 {
			t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
		}
	})

	t.Run("the request log and the image share standard output", func(t *testing.T) {
		got := run(t, srv.URL+"/busy.html", "--height", "100", "--log-requests", "-", "-o", "-")
		if got.code != 0 {
			t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
		}
		if !bytes.Contains(got.stdout, []byte("\x89PNG")) || !bytes.Contains(got.stdout, []byte(`"method":"GET"`)) {
			t.Errorf("stdout = %q, want both the image and request lines", got.stdout)
		}
	})
}

func TestShotRedirect(t *testing.T) {
	srv := site(t)
	// A redirect must not leave the status of the redirect itself behind.
	out := filepath.Join(t.TempDir(), "shot.png")
	got := run(t, srv.URL+"/redirect", "--height", "100", "--fail", "-o", out)
	if got.code != exitOK {
		t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
	}
}

func TestCDPURLIsExclusive(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"a browser argument conflicts", []string{"--browser-arg=--foo"}, "--browser-arg"},
		{"a browser path conflicts", []string{"--browser-path", "/bin/chrome"}, "--browser-path"},
		{"interactive conflicts", []string{"-i"}, "--interactive"},
		{"devtools conflicts", []string{"--devtools"}, "--devtools"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := append([]string{"example.com", "--cdp-url", "http://localhost:9222"}, tt.args...)
			got := run(t, args...)
			if got.code != exitUsage || !strings.Contains(got.stderr, "--cdp-url cannot be combined with "+tt.want) {
				t.Fatalf("exit %d, want %d; stderr:\n%s", got.code, exitUsage, got.stderr)
			}
		})
	}
	t.Run("doctor rejects the same combination", func(t *testing.T) {
		got := run(t, "doctor", "--cdp-url", "http://localhost:9222", "--browser-path", "/bin/chrome")
		if got.code != exitUsage {
			t.Fatalf("exit %d, want %d; stderr:\n%s", got.code, exitUsage, got.stderr)
		}
	})
	// The documented environment variable belongs to discovery, so it must not
	// count as a --browser-path that conflicts with --cdp-url.
	t.Run("the environment does not conflict", func(t *testing.T) {
		t.Setenv("SHOTS_BROWSER_PATH", filepath.Join(t.TempDir(), "absent"))
		got := run(t, "example.com", "--cdp-url", "http://127.0.0.1:1", "--timeout", "2000")
		if got.code == exitUsage || strings.Contains(got.stderr, "cannot be combined") {
			t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
		}
	})
}

func TestDoctor(t *testing.T) {
	got := run(t, "doctor")
	for _, want := range []string{"shots", "platform", "pinned", "cache"} {
		if !strings.Contains(string(got.stdout), want) {
			t.Errorf("stdout is missing %q:\n%s", want, got.stdout)
		}
	}

	// The report names the interpreter a python: step would run, rather than
	// applying PLAN.md Section 9's order a second time: a $SHOTS_PYTHON that is
	// not there falls through in both places, or in neither.
	t.Run("the python line is what a python: step would run", func(t *testing.T) {
		t.Setenv("SHOTS_PYTHON", filepath.Join(t.TempDir(), "python-that-moved"))
		want, err := process.Python()
		if err != nil {
			t.Skip("no Python interpreter")
		}
		got := run(t, "doctor")
		if !strings.Contains(string(got.stdout), "python  ") || !strings.Contains(string(got.stdout), want) {
			t.Errorf("stdout does not report %q:\n%s", want, got.stdout)
		}
	})
}

// TestShotPageOptions checks the emulation flags by making --wait-for assert
// them in the page: a condition that never becomes true fails the run.
func TestShotPageOptions(t *testing.T) {
	srv := site(t)
	tests := []struct {
		name      string
		args      []string
		condition string
		want      int
	}{
		{"the user agent is overridden", []string{"--user-agent", "shots-test/1"}, "navigator.userAgent === 'shots-test/1'", exitOK},
		{"the user agent is Chrome by default", nil, "navigator.userAgent.includes('Chrome')", exitOK},
		{"reduced motion is emulated", []string{"--reduced-motion"}, "matchMedia('(prefers-reduced-motion: reduce)').matches", exitOK},
		{"reduced motion is off by default", nil, "matchMedia('(prefers-reduced-motion: reduce)').matches", exitFailure},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "shot.png")
			args := append([]string{srv.URL + "/index.html", "--height", "100", "--timeout", "3000", "--wait-for", tt.condition, "-o", out}, tt.args...)
			if got := run(t, args...); got.code != tt.want {
				t.Fatalf("exit %d, want %d; stderr:\n%s", got.code, tt.want, got.stderr)
			}
		})
	}
}
