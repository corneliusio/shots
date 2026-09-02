package command

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg"
	"image/png"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/corneliusio/shots/internal/browser"
	"github.com/corneliusio/shots/internal/testsite"
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

// centerPixel decodes a PNG and returns the color at its middle.
func centerPixel(t *testing.T, path string) color.Color {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	b := img.Bounds()
	return color.RGBAModel.Convert(img.At(b.Min.X+b.Dx()/2, b.Min.Y+b.Dy()/2))
}

func readShotReport(t *testing.T, data []byte) shotReport {
	t.Helper()
	var report shotReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("decoding report %q: %v", data, err)
	}
	return report
}

func runTake(t *testing.T, args ...string) result {
	t.Helper()
	return run(t, append([]string{"take"}, args...)...)
}

func shot(t *testing.T, args ...string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "shot.png")
	got := runTake(t, append(args, "-o", out)...)
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
		// center, when set, is the color expected at the middle of the image.
		center color.Color
	}{
		// geometry.html is a 1280x2000 page of exactly positioned boxes.
		{"full page when no height is given", nil, 1280, 2000, nil},
		{"a height fixes the viewport", []string{"--height", "400"}, 1280, 400, nil},
		{"width narrows the viewport", []string{"-w", "600", "--height", "400"}, 600, 400, nil},
		{"retina doubles every pixel", []string{"--retina", "--height", "400"}, 2560, 800, nil},
		{"scale factor multiplies", []string{"--scale-factor", "1.5", "--height", "400"}, 1920, 600, nil},
		{"a selector crops to the element", []string{"-s", "#box-a"}, 100, 50, nil},
		{"padding grows the crop on all sides", []string{"-s", "#box-a", "-p", "10"}, 120, 70, nil},
		// #box-a is 10,20 100x50 and #box-b is 200,300 80x40, so together they
		// span x 10..280 and y 20..340.
		{"repeated selectors combine", []string{"-s", "#box-a", "-s", "#box-b"}, 270, 320, nil},
		// The three .item boxes are 30x30 at x 400, 440, 480 and y 100.
		{"selector-all takes every match", []string{"--selector-all", ".item"}, 110, 30, nil},
		{"a js selector picks one element", []string{"--js-selector", "el.id === 'box-b'"}, 80, 40, nil},
		{"a js selector-all takes every match", []string{"--js-selector-all", "el.className === 'item'"}, 110, 30, nil},
		// #box-c is at y 1900, below the 720px viewport.
		{"a selector below the fold still crops", []string{"-s", "#box-c"}, 60, 60, color.RGBA{0, 0xff, 0, 0xff}},
		// #edge is 0,1960 1280x40, so padding runs past the left, right and
		// bottom document edges and is clamped there.
		{"padding on an edge-flush element is clamped to the page", []string{"-s", "#edge", "-p", "10"}, 1280, 50, color.RGBA{0xff, 0, 0xff, 0xff}},
		// #corner is 1180,0 100x30, so the padded origin would be negative and
		// the clip position lost; retina keeps the clamp in CSS pixels.
		{"a negative padded origin is clamped", []string{"-s", "#corner", "-p", "10"}, 110, 40, color.RGBA{0, 0xff, 0xff, 0xff}},
		{"a clamped region scales like any other", []string{"--retina", "-s", "#corner", "-p", "10"}, 220, 80, color.RGBA{0, 0xff, 0xff, 0xff}},
		{"a selector is captured at the scale factor", []string{"--retina", "-s", "#box-a"}, 200, 100, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := shot(t, append([]string{srv.URL + "/geometry.html"}, tt.args...)...)
			w, h := bounds(t, out)
			if w != tt.width || h != tt.height {
				t.Errorf("got %dx%d, want %dx%d", w, h, tt.width, tt.height)
			}
			if tt.center != nil {
				if got := centerPixel(t, out); got != tt.center {
					t.Errorf("center pixel = %v, want %v", got, tt.center)
				}
			}
		})
	}
}

func TestShotExcludesFixedOverlayOutsideSelectedRegion(t *testing.T) {
	srv := site(t)
	out := shot(t, srv.URL+"/fixed-overlap.html", "-s", "#target")
	w, h := bounds(t, out)
	if w != 400 || h != 620 {
		t.Fatalf("got %dx%d, want 400x620", w, h)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, _ := img.At(w/2, 10).RGBA()
	if r != 0 || g != 0xffff || b != 0 {
		t.Errorf("top pixel = %d,%d,%d, want green without external fixed or sticky layers", r, g, b)
	}
	r, g, b, _ = img.At(75, 135).RGBA()
	if r != 0 || g != 0 || b != 0xffff {
		t.Errorf("selected fixed child pixel = %d,%d,%d, want blue", r, g, b)
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
			got := runTake(t, append([]string{url, "-s", "#delayed", "-o", out}, tt.args...)...)
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

func TestShotStable(t *testing.T) {
	srv := site(t)
	out := filepath.Join(t.TempDir(), "shot.png")
	script := `() => {
		document.documentElement.style.background = '#fff';
		document.body.style.background = '#fff';
		const image = new Image();
		image.onload = () => {
			document.documentElement.style.background = '#f00';
			document.body.style.background = '#f00';
		};
		image.loading = 'lazy';
		image.src = '/slow-image?ms=300';
		document.body.append(image);
	}`
	got := runTake(t, srv.URL+"/index.html", "--stable", "-j", script, "--width", "200", "--height", "100", "-o", out)
	if got.code != exitOK {
		t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, _ := img.At(190, 90).RGBA()
	if r == 0 || g != 0 || b != 0 {
		t.Errorf("background pixel = %d,%d,%d, want red after the slow image loaded", r, g, b)
	}
}

func TestShotReport(t *testing.T) {
	srv := site(t)

	t.Run("success is JSON on stdout", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "shot.png")
		url := srv.URL + "/redirect?to=/console.html"
		got := runTake(t, url, "--height", "100", "--report", "-", "-o", out)
		if got.code != exitOK {
			t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
		}
		report := readShotReport(t, got.stdout)
		if report.Schema != shotReportSchema || !report.OK || report.RequestedURL != url || report.FinalURL != srv.URL+"/console.html" || report.Status != 200 || report.Output != out {
			t.Errorf("report = %+v", report)
		}
		if !slices.ContainsFunc(report.Console, func(message browser.Message) bool {
			return message.Type == "log" && message.Text == "log message"
		}) {
			t.Errorf("console = %+v, want log message", report.Console)
		}
	})

	t.Run("capture failures still write a report", func(t *testing.T) {
		dir := t.TempDir()
		out := filepath.Join(dir, "shot.png")
		reportPath := filepath.Join(dir, "report.json")
		got := runTake(t, srv.URL+"/index.html", "-s", "#missing", "--report", reportPath, "-o", out)
		if got.code != exitFailure {
			t.Fatalf("exit %d, want %d; stderr:\n%s", got.code, exitFailure, got.stderr)
		}
		data, err := os.ReadFile(reportPath)
		if err != nil {
			t.Fatal(err)
		}
		report := readShotReport(t, data)
		if report.OK || report.Error == "" || report.Status != 200 {
			t.Errorf("report = %+v, want a failed capture with status 200", report)
		}
	})

	t.Run("page errors are collected", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "shot.png")
		got := runTake(t, srv.URL+"/jserror.html", "--height", "100", "--report", "-", "-o", out)
		if got.code != exitOK {
			t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
		}
		report := readShotReport(t, got.stdout)
		if !slices.ContainsFunc(report.PageErrors, func(message string) bool {
			return strings.Contains(message, "uncaught on purpose")
		}) {
			t.Errorf("page_errors = %q, want the uncaught exception", report.PageErrors)
		}
	})

	t.Run("failed requests include HTTP and network failures", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "shot.png")
		got := runTake(t, srv.URL+"/network.html", "--height", "100", "--wait-for", "window.done", "--report", "-", "-o", out)
		if got.code != exitOK {
			t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
		}
		report := readShotReport(t, got.stdout)
		hasHTTPError := slices.ContainsFunc(report.FailedRequests, func(request browser.Request) bool {
			return strings.HasSuffix(request.URL, "/status/404") && request.Status == 404
		})
		hasNetworkError := slices.ContainsFunc(report.FailedRequests, func(request browser.Request) bool {
			return strings.HasSuffix(request.URL, "/reset") && request.Error != ""
		})
		if !hasHTTPError || !hasNetworkError {
			t.Errorf("failed_requests = %+v, want HTTP and network failures", report.FailedRequests)
		}
		if !slices.ContainsFunc(report.Console, func(message browser.Message) bool {
			return message.Type == "error" && strings.Contains(message.Text, "Failed to load resource")
		}) {
			t.Errorf("console = %+v, want Chrome's failed-resource error", report.Console)
		}
	})
}

func TestShotReportBoundsDiagnostics(t *testing.T) {
	diagnostics := newShotDiagnostics(true)
	diagnostics.reset()
	for i := 0; i < reportItemLimit+1; i++ {
		diagnostics.addConsole(browser.Message{Type: "log", Text: "message"})
		diagnostics.addPageError("error")
		diagnostics.addFailedRequest(browser.Request{URL: "https://example.com", Error: "failed"})
	}
	report := diagnostics.snapshot("https://example.com", "", "shot.png", 200, time.Now(), nil)
	if len(report.Console) != reportItemLimit || len(report.PageErrors) != reportItemLimit || len(report.FailedRequests) != reportItemLimit {
		t.Fatalf("diagnostic lengths = %d, %d, %d", len(report.Console), len(report.PageErrors), len(report.FailedRequests))
	}
	if report.Omitted.Console != 1 || report.Omitted.PageErrors != 1 || report.Omitted.FailedRequests != 1 {
		t.Errorf("omitted = %+v, want one in every category", report.Omitted)
	}
}

func TestShotRepeat(t *testing.T) {
	srv := site(t)
	out := filepath.Join(t.TempDir(), "shot.png")
	script := `() => {
		const count = Number(sessionStorage.getItem('shots-count') || 0) + 1;
		sessionStorage.setItem('shots-count', count);
		console.log('capture ' + count);
	}`
	got := runStdin(t, "\n\n", "take", srv.URL+"/index.html", "--repeat", "--report", "-", "-j", script, "--height", "100", "-o", out)
	if got.code != exitOK {
		t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
	}
	lines := slices.Collect(strings.Lines(strings.TrimSpace(string(got.stdout))))
	if len(lines) != 3 {
		t.Fatalf("reports = %d, want 3:\n%s", len(lines), got.stdout)
	}
	for i, line := range lines {
		report := readShotReport(t, []byte(line))
		want := fmt.Sprintf("capture %d", i+1)
		if !report.OK || !slices.ContainsFunc(report.Console, func(message browser.Message) bool { return message.Text == want }) {
			t.Errorf("report %d console = %+v, want %q", i+1, report.Console, want)
		}
	}
	if _, err := os.Stat(out); err != nil {
		t.Errorf("final screenshot: %v", err)
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
			got := runTake(t, append([]string{srv.URL + tt.path, "-o", out}, tt.args...)...)
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
		{"an unknown image format", []string{"x.html", "--format", "webp"}},
		{"PNG quality", []string{"x.html", "--format", "png", "--quality", "80"}},
		{"a format conflicting with the extension", []string{"x.html", "--format", "jpeg", "-o", "x.png"}},
		{"a transparent JPEG", []string{"x.html", "--format", "jpeg", "--omit-background"}},
		{"a negative wait", []string{"x.html", "--wait", "-1"}},
		{"an invalid color scheme", []string{"x.html", "--color-scheme", "sepia"}},
		{"report and image on stdout", []string{"x.html", "--report", "-", "-o", "-"}},
		{"report and request log on stdout", []string{"x.html", "--report", "-", "--log-requests", "-"}},
		{"report and HAR in one file", []string{"x.html", "--report", "trace.har", "--har-file", "trace.har"}},
		{"repeat without an output", []string{"x.html", "--repeat"}},
		{"repeat to stdout", []string{"x.html", "--repeat", "-o", "-"}},
		{"repeat with no-clobber", []string{"x.html", "--repeat", "--no-clobber", "-o", "x.png"}},
		{"repeat with stdin script", []string{"x.html", "--repeat", "--js-file", "-", "-o", "x.png"}},
		{"repeat with interactive mode", []string{"x.html", "--repeat", "--interactive", "-o", "x.png"}},
		{"repeat with devtools", []string{"x.html", "--repeat", "--devtools", "-o", "x.png"}},
		{"repeat with HAR", []string{"x.html", "--repeat", "--har", "-o", "x.png"}},
		{"an unknown flag", []string{"x.html", "--nope"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := runTake(t, tt.args...); got.code != exitUsage {
				t.Errorf("exit %d, want %d; stderr:\n%s", got.code, exitUsage, got.stderr)
			}
		})
	}
}

func TestShotOutput(t *testing.T) {
	srv := site(t)

	t.Run("stdout carries a JPEG by default", func(t *testing.T) {
		got := runTake(t, srv.URL+"/geometry.html", "--height", "100", "-o", "-")
		if got.code != 0 {
			t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
		}
		if !bytes.HasPrefix(got.stdout, []byte("\xff\xd8\xff")) {
			t.Errorf("stdout is not a JPEG: %q", got.stdout[:min(16, len(got.stdout))])
		}
		if _, _, err := image.DecodeConfig(bytes.NewReader(got.stdout)); err != nil {
			t.Errorf("stdout does not decode: %v", err)
		}
	})

	t.Run("format selects PNG on stdout", func(t *testing.T) {
		got := runTake(t, srv.URL+"/geometry.html", "--height", "100", "--format", "png", "-o", "-")
		if got.code != 0 {
			t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
		}
		if !bytes.HasPrefix(got.stdout, []byte("\x89PNG\r\n\x1a\n")) {
			t.Errorf("stdout is not a PNG: %q", got.stdout[:min(16, len(got.stdout))])
		}
	})

	t.Run("quality selects JPEG", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "shot.jpg")
		if got := runTake(t, srv.URL+"/geometry.html", "--height", "100", "--quality", "60", "-o", out); got.code != 0 {
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
		if got := runTake(t, srv.URL+"/geometry.html", "--height", "100", "-o", out); got.code != 0 {
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

	t.Run("a png extension selects PNG", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "shot.png")
		if got := runTake(t, srv.URL+"/geometry.html", "--height", "100", "-o", out); got.code != 0 {
			t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
		}
		f, err := os.Open(out)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, format, err := image.DecodeConfig(f); err != nil || format != "png" {
			t.Errorf("format = %q (%v), want png", format, err)
		}
	})

	t.Run("an omitted output is named after the URL", func(t *testing.T) {
		t.Chdir(t.TempDir())
		if got := runTake(t, srv.URL+"/geometry.html", "--height", "100"); got.code != 0 {
			t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
		}
		matches, err := filepath.Glob("*-geometry-html.jpg")
		if err != nil || len(matches) != 1 {
			names, _ := filepath.Glob("*")
			t.Fatalf("want one *-geometry-html.jpg, found %v", names)
		}
	})

	t.Run("no-clobber refuses to replace a file", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "shot.png")
		if err := os.WriteFile(out, []byte("keep me"), 0o644); err != nil {
			t.Fatal(err)
		}
		got := runTake(t, srv.URL+"/geometry.html", "--height", "100", "-o", out, "--no-clobber")
		if got.code != exitFailure {
			t.Fatalf("exit %d, want %d", got.code, exitFailure)
		}
		if data, _ := os.ReadFile(out); string(data) != "keep me" {
			t.Error("the existing file was overwritten")
		}
	})
}

func TestScreenshotEncoding(t *testing.T) {
	tests := []struct {
		name           string
		output, format string
		quality        int
		omitBackground bool
		wantJPEG       bool
		wantQuality    int
		wantExtension  string
	}{
		{name: "JPEG by default", wantJPEG: true, wantQuality: 80, wantExtension: "jpg"},
		{name: "PNG extension", output: "shot.png", wantExtension: "png"},
		{name: "explicit PNG", output: "-", format: "png", wantExtension: "png"},
		{name: "transparent output", omitBackground: true, wantExtension: "png"},
		{name: "custom JPEG quality", quality: 60, wantJPEG: true, wantQuality: 60, wantExtension: "jpg"},
		{name: "jpg format alias", format: "jpg", wantJPEG: true, wantQuality: 80, wantExtension: "jpg"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := screenshotEncodingFor(tt.output, tt.format, tt.quality, tt.omitBackground)
			if err != nil {
				t.Fatal(err)
			}
			if got.jpeg != tt.wantJPEG || got.quality != tt.wantQuality || got.extension != tt.wantExtension {
				t.Errorf("encoding = %+v, want jpeg=%v quality=%d extension=%q", got, tt.wantJPEG, tt.wantQuality, tt.wantExtension)
			}
		})
	}
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
			got := runTake(t, srv.URL+"/geometry.html", "-j", tt.source, "-s", "[data-shot]", "-o", out)
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
		got := runTake(t, srv.URL+"/basic-auth", "--height", "100", "--fail", "-o", out,
			"--auth-username", testsite.BasicAuthUser, "--auth-password", testsite.BasicAuthPassword)
		if got.code != exitOK {
			t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
		}
	})
	t.Run("without credentials the page is unauthorized", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "shot.png")
		got := runTake(t, srv.URL+"/basic-auth", "--height", "100", "--fail", "-o", out)
		if got.code != exitFailure || !strings.Contains(got.stderr, "401") {
			t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
		}
	})
}

func TestShotLogging(t *testing.T) {
	srv := site(t)

	t.Run("console messages reach stderr", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "shot.png")
		got := runTake(t, srv.URL+"/console.html", "--height", "100", "--log-console", "-o", out)
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
		got := runTake(t, srv.URL+"/geometry.html", "--height", "100", "--log-requests", log, "-o", filepath.Join(dir, "shot.png"))
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
		got := runTake(t, srv.URL+"/busy.html", "--height", "100", "--log-console", "-o", out)
		if got.code != 0 {
			t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
		}
	})

	t.Run("the request log and the image share standard output", func(t *testing.T) {
		got := runTake(t, srv.URL+"/busy.html", "--height", "100", "--log-requests", "-", "-o", "-")
		if got.code != 0 {
			t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
		}
		if !bytes.Contains(got.stdout, []byte("\xff\xd8\xff")) || !bytes.Contains(got.stdout, []byte(`"method":"GET"`)) {
			t.Errorf("stdout = %q, want both the image and request lines", got.stdout)
		}
	})
}

func TestShotRedirect(t *testing.T) {
	srv := site(t)
	// A redirect must not leave the status of the redirect itself behind.
	out := filepath.Join(t.TempDir(), "shot.png")
	got := runTake(t, srv.URL+"/redirect", "--height", "100", "--fail", "-o", out)
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
			got := runTake(t, args...)
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
		got := runTake(t, "example.com", "--cdp-url", "http://127.0.0.1:1", "--timeout", "2000")
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
	if strings.Contains(string(got.stdout), "python") {
		t.Errorf("stdout still reports Python:\n%s", got.stdout)
	}
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
		{"stable mode reduces motion", []string{"--stable"}, "matchMedia('(prefers-reduced-motion: reduce)').matches", exitOK},
		{"reduced motion is off by default", nil, "matchMedia('(prefers-reduced-motion: reduce)').matches", exitFailure},
		{"light color scheme is emulated", []string{"--color-scheme", "light"}, "matchMedia('(prefers-color-scheme: light)').matches", exitOK},
		{"dark color scheme is emulated", []string{"--color-scheme", "dark"}, "matchMedia('(prefers-color-scheme: dark)').matches", exitOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "shot.png")
			args := append([]string{srv.URL + "/index.html", "--height", "100", "--timeout", "3000", "--wait-for", tt.condition, "-o", out}, tt.args...)
			if got := runTake(t, args...); got.code != tt.want {
				t.Fatalf("exit %d, want %d; stderr:\n%s", got.code, tt.want, got.stderr)
			}
		})
	}
}

func TestShotRepeatContinuesAfterFailure(t *testing.T) {
	srv := site(t)
	// The first capture runs before any stdin line, so it fails and continues
	// like the rest.
	for _, failing := range []int{1, 2} {
		t.Run(fmt.Sprintf("capture %d fails", failing), func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "shot.png")
			script := fmt.Sprintf(`() => {
				const count = Number(sessionStorage.getItem('shots-count') || 0) + 1;
				sessionStorage.setItem('shots-count', count);
				if (count === %d) throw new Error('boom');
			}`, failing)
			got := runStdin(t, "\n\n", "take", srv.URL+"/index.html", "--repeat", "--report", "-", "-j", script, "--height", "100", "-o", out)
			if got.code != exitFailure {
				t.Errorf("exit %d, want %d", got.code, exitFailure)
			}
			lines := slices.Collect(strings.Lines(strings.TrimSpace(string(got.stdout))))
			if len(lines) != 3 {
				t.Fatalf("reports = %d, want 3:\n%s", len(lines), got.stdout)
			}
			for i, line := range lines {
				if ok := readShotReport(t, []byte(line)).OK; ok != (i+1 != failing) {
					t.Errorf("report %d ok = %v", i+1, ok)
				}
			}
			for _, want := range []string{"boom", "1 of 3 captures failed"} {
				if !strings.Contains(got.stderr, want) {
					t.Errorf("stderr lacks %q:\n%s", want, got.stderr)
				}
			}
		})
	}
}

func TestShotInsecure(t *testing.T) {
	site(t).Close()
	srv := httptest.NewTLSServer(testsite.Handler())
	defer srv.Close()
	if got := runTake(t, srv.URL+"/geometry.html", "-o", filepath.Join(t.TempDir(), "shot.png")); got.code == exitOK {
		t.Fatalf("a self-signed certificate was accepted without --insecure")
	}
	out := shot(t, srv.URL+"/geometry.html", "--insecure", "-s", "#box-a")
	if w, h := bounds(t, out); w != 100 || h != 50 {
		t.Errorf("got %dx%d, want 100x50", w, h)
	}
}
