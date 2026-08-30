package command

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// writeStoryboard writes a storyboard into dir and returns its path.
func writeStoryboard(t *testing.T, dir, yaml string) string {
	t.Helper()
	path := filepath.Join(dir, "storyboard.yml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// probe is what ffprobe reports about an encoded file.
type probe struct {
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
	Streams []struct {
		CodecName string `json:"codec_name"`
		Width     int    `json:"width"`
		Height    int    `json:"height"`
	} `json:"streams"`
}

func ffprobe(t *testing.T, path string) probe {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-show_format", "-show_streams",
		"-print_format", "json", path).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	var p probe
	if err := json.Unmarshal(out, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func (p probe) seconds(t *testing.T) float64 {
	t.Helper()
	d, err := strconv.ParseFloat(p.Format.Duration, 64)
	if err != nil {
		t.Fatalf("duration %q: %v", p.Format.Duration, err)
	}
	return d
}

// needsFFmpeg skips a test on a machine that cannot encode.
func needsFFmpeg(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
}

// A whole storyboard against the test site: the browser actions run, a
// screenshot lands, and the video is playable at the viewport's size.
func TestVideoRecordsAStoryboard(t *testing.T) {
	needsFFmpeg(t)
	srv := site(t)
	dir := t.TempDir()
	story := writeStoryboard(t, dir, `output: demo.webm
url: `+srv.URL+`/storyboard.html
viewport:
  width: 640
  height: 480
cursor: true
wait_for: "#open"
javascript: document.title = "recording"

scenes:
- name: Reveal
  do:
  - click: "#open"
  - wait_for: "#revealed:not([hidden])"
  - screenshot: revealed.png
  - pause: 0.2
- name: Search
  do:
  - type:
      into: "#query"
      text: hello
      delay_ms: 10
  - press:
      selector: "#query"
      key: Enter
  - wait_for_url: "**#searched"
  - js: document.body.dataset.done = "1"
  - scroll: 300
  - pause: 0.2
`)
	got := run(t, "video", story)
	if got.code != exitOK {
		t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
	}
	webm := filepath.Join(dir, "demo.webm")
	p := ffprobe(t, webm)
	if len(p.Streams) != 1 {
		t.Fatalf("%d streams, want 1", len(p.Streams))
	}
	if s := p.Streams[0]; s.CodecName != "vp9" || s.Width != 640 || s.Height != 480 {
		t.Errorf("stream = %s %dx%d, want vp9 640x480", s.CodecName, s.Width, s.Height)
	}
	if d := p.seconds(t); d <= 0 {
		t.Errorf("duration = %gs, want more than zero", d)
	}
	if !exists(t, dir, "revealed.png") {
		t.Error("the screenshot action wrote nothing")
	}
	// The frames are gone, and only the storyboard, the video and the
	// screenshot are left.
	names, _ := filepath.Glob(filepath.Join(dir, "*"))
	if len(names) != 3 {
		t.Errorf("the run left %v behind, want three files", names)
	}
	for _, want := range []string{"Recording video to", "Scene 1: Reveal", "Scene 2: Search", "Video written to"} {
		if !strings.Contains(got.stderr, want) {
			t.Errorf("stderr is missing %q:\n%s", want, got.stderr)
		}
	}
}

// --mp4 writes a second file from the same frames, and --keep-frames leaves the
// spool behind.
func TestVideoMP4AndKeepFrames(t *testing.T) {
	needsFFmpeg(t)
	srv := site(t)
	dir := t.TempDir()
	story := writeStoryboard(t, dir, `output: demo.webm
url: `+srv.URL+`/storyboard.html
viewport:
  width: 640
  height: 480
scenes:
- do:
  - pause: 0.2
`)
	got := run(t, "video", story, "--mp4", "--keep-frames")
	if got.code != exitOK {
		t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
	}
	if s := ffprobe(t, filepath.Join(dir, "demo.mp4")).Streams[0]; s.CodecName != "h264" || s.Width != 640 || s.Height != 480 {
		t.Errorf("mp4 stream = %s %dx%d, want h264 640x480", s.CodecName, s.Width, s.Height)
	}
	spools, _ := filepath.Glob(filepath.Join(dir, "shots-frames-*"))
	if len(spools) != 1 {
		t.Fatalf("--keep-frames left %v, want one spool", spools)
	}
	frames, _ := filepath.Glob(filepath.Join(spools[0], "*.png"))
	if len(frames) == 0 {
		t.Error("the spool holds no frames")
	}
}

// Shell steps run at all three levels a storyboard offers them — before the
// browser opens, before a scene, and between actions — in the storyboard's own
// directory.
func TestVideoShellSteps(t *testing.T) {
	needsFFmpeg(t)
	site(t) // for the browser check; this storyboard serves itself from disk
	dir := t.TempDir()
	t.Chdir(dir)
	story := writeStoryboard(t, dir, `output: demo.webm
sh: printf '<h1 id=one>one</h1>' > page.html
url: page.html
viewport:
  width: 640
  height: 480
wait_for: "#one"
scenes:
- name: Rewrite
  sh: printf '<h1 id=two>two</h1>' > page.html
  do:
  - open: page.html
  - wait_for: "#two"
  - sh: printf '<h1 id=three>three</h1>' > page.html
  - open: page.html
  - wait_for: "#three"
`)
	got := run(t, "video", story)
	if got.code != exitOK {
		t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
	}
	if !exists(t, dir, "demo.webm") {
		t.Error("no video was written")
	}
}

// A failing step stops the recording, and nothing is left behind.
func TestVideoFailingStep(t *testing.T) {
	site(t)
	dir := t.TempDir()
	story := writeStoryboard(t, dir, `output: demo.webm
url: http://example.invalid/
viewport:
  width: 640
  height: 480
sh: exit 3
scenes:
- do:
  - pause: 0.1
`)
	got := run(t, "video", story)
	if got.code != exitFailure {
		t.Fatalf("exit %d, want %d; stderr:\n%s", got.code, exitFailure, got.stderr)
	}
	if exists(t, dir, "demo.webm") {
		t.Error("a failed run wrote a video")
	}
}

// A storyboard problem is status 2 with a field path, per PLAN.md Section 10.
func TestVideoStoryboardErrors(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{"an empty file", "", "storyboard is empty"},
		{"no scenes", "output: a.webm\nurl: http://example.com/\n", "scenes"},
		{"no url", "output: a.webm\nscenes:\n- do: []\n", "url (line 1): required"},
		{"an unknown key", "output: a.webm\nurl: http://x/\nnope: 1\nscenes:\n- do: []\n", "nope (line 3): unknown field"},
		{
			"an unknown action",
			"output: a.webm\nurl: http://x/\nscenes:\n- do:\n  - dance: now\n",
			"scenes[0].do[0].dance (line 5): unknown action",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			got := run(t, "video", writeStoryboard(t, dir, tt.yaml))
			if got.code != exitUsage {
				t.Fatalf("exit %d, want %d; stderr:\n%s", got.code, exitUsage, got.stderr)
			}
			if !strings.Contains(got.stderr, tt.want) {
				t.Errorf("stderr = %q, want it to mention %q", got.stderr, tt.want)
			}
		})
	}
}

// A storyboard with nowhere to write is a usage error, not a run that records
// and then discovers it.
func TestVideoNeedsAnOutput(t *testing.T) {
	dir := t.TempDir()
	story := writeStoryboard(t, dir, "url: http://example.com/\nscenes:\n- do: []\n")
	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{"no output at all", []string{"video", story}, "output:"},
		{"standard output", []string{"video", story, "-o", "-"}, "a video is written to a file"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := run(t, tt.args...)
			if got.code != exitUsage {
				t.Fatalf("exit %d, want %d; stderr:\n%s", got.code, exitUsage, got.stderr)
			}
			if !strings.Contains(got.stderr, tt.want) {
				t.Errorf("stderr = %q, want it to mention %q", got.stderr, tt.want)
			}
		})
	}
}

func TestResolveURL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		base string
		want string
	}{
		{"absolute", "https://example.com/a", "http://localhost/b", "https://example.com/a"},
		{"relative to the page", "/pricing", "http://localhost:8000/a/b.html", "http://localhost:8000/pricing"},
		{"relative sibling", "c.html", "http://localhost:8000/a/b.html", "http://localhost:8000/a/c.html"},
		{"a fragment", "#top", "http://localhost:8000/a.html", "http://localhost:8000/a.html#top"},
		{"no base", "example.com", "about:blank", "http://example.com"},
		{"bare host with a base", "example.com", "http://localhost:8000/", "http://localhost:8000/example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveURL(tt.raw, tt.base)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("resolveURL(%q, %q) = %q, want %q", tt.raw, tt.base, got, tt.want)
			}
		})
	}
}
