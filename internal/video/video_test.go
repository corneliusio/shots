package video

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// probe is what ffprobe reports about an encoded file.
type probe struct {
	Format struct {
		Duration   string `json:"duration"`
		FormatName string `json:"format_name"`
	} `json:"format"`
	Streams []struct {
		CodecName string `json:"codec_name"`
		Width     int    `json:"width"`
		Height    int    `json:"height"`
	} `json:"streams"`
}

// ffprobe reports what ffprobe sees in path.
func ffprobe(t *testing.T, path string) probe {
	t.Helper()
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe is not installed")
	}
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

// frames spools count frames of the given size, one every 40ms.
func frames(t *testing.T, r *Recorder, count, width, height int) time.Time {
	t.Helper()
	start := time.Now()
	for i := range count {
		img := image.NewRGBA(image.Rect(0, 0, width, height))
		// A moving band, so the encoder has something to compress.
		for y := (i * 7) % height; y < min((i*7)%height+10, height); y++ {
			for x := range width {
				img.Set(x, y, color.RGBA{R: 255, A: 255})
			}
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			t.Fatal(err)
		}
		r.Add(buf.Bytes(), start.Add(time.Duration(i)*40*time.Millisecond))
	}
	return start.Add(time.Duration(count) * 40 * time.Millisecond)
}

func TestEncode(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	dir := t.TempDir()
	r, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	end := frames(t, r, 25, 320, 240)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if r.Frames() != 25 {
		t.Fatalf("Frames() = %d, want 25", r.Frames())
	}

	webm := filepath.Join(dir, "out.webm")
	mp4 := filepath.Join(dir, "out.mp4")
	if err := r.Encode(t.Context(), Options{WebM: webm, MP4: mp4, End: end}); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		path   string
		format string
		codec  string
	}{
		{webm, "matroska,webm", "vp9"},
		{mp4, "mov,mp4,m4a,3gp,3g2,mj2", "h264"},
	} {
		p := ffprobe(t, tt.path)
		if p.Format.FormatName != tt.format {
			t.Errorf("%s: format = %q, want %q", tt.path, p.Format.FormatName, tt.format)
		}
		if got := p.seconds(t); got < 0.8 || got > 1.2 {
			t.Errorf("%s: duration = %gs, want about 1s", tt.path, got)
		}
		if len(p.Streams) != 1 {
			t.Fatalf("%s: %d streams, want 1", tt.path, len(p.Streams))
		}
		s := p.Streams[0]
		if s.CodecName != tt.codec || s.Width != 320 || s.Height != 240 {
			t.Errorf("%s: %s %dx%d, want %s 320x240", tt.path, s.CodecName, s.Width, s.Height, tt.codec)
		}
	}
	if _, err := os.Stat(filepath.Join(r.Dir(), "000000.png")); err != nil {
		t.Errorf("frames were not preserved: %v", err)
	}
	if err := r.Remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(r.Dir()); !os.IsNotExist(err) {
		t.Errorf("Remove left %s behind", r.Dir())
	}
}

// An odd viewport is padded to even dimensions, because yuv420p has no other
// option, and the padding is a pixel rather than a rescale.
func TestEncodeOddSize(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	dir := t.TempDir()
	r, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	end := frames(t, r, 10, 101, 51)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	webm := filepath.Join(dir, "odd.webm")
	if err := r.Encode(t.Context(), Options{WebM: webm, End: end}); err != nil {
		t.Fatal(err)
	}
	if s := ffprobe(t, webm).Streams[0]; s.Width != 102 || s.Height != 52 {
		t.Errorf("%dx%d, want 102x52", s.Width, s.Height)
	}
}

func TestEncodeNoFrames(t *testing.T) {
	dir := t.TempDir()
	r, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	err = r.Encode(t.Context(), Options{WebM: filepath.Join(dir, "x.webm")})
	if err == nil || err.Error() != "no frames were captured" {
		t.Fatalf("Encode() = %v, want no frames", err)
	}
}

// A failed encode leaves no output file, and every temporary file it wrote is
// gone too.
func TestEncodeFailureLeavesNothing(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	dir := t.TempDir()
	r, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	frames(t, r, 3, 32, 32)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	// A frame that is gone is something only ffmpeg can discover.
	if err := os.Remove(filepath.Join(r.Dir(), "000001.png")); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "broken.webm")
	if err := r.Encode(context.Background(), Options{WebM: out}); err == nil {
		t.Fatal("Encode() succeeded on a broken frame")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("a failed encode left %s behind", out)
	}
	temps, err := filepath.Glob(filepath.Join(dir, ".shots-*"))
	if err != nil {
		t.Fatal(err)
	}
	for _, temp := range temps {
		t.Errorf("a failed encode left %q behind", filepath.Base(temp))
	}
}

func TestPlaylist(t *testing.T) {
	start := time.Unix(0, 0)
	r := &Recorder{stamps: []time.Time{
		start,
		start.Add(100 * time.Millisecond),
		start.Add(100 * time.Millisecond), // two frames in the same instant
	}}
	want := "ffconcat version 1.0\n" +
		"file 000000.png\nduration 0.100000\n" +
		"file 000001.png\nduration 0.001000\n" +
		"file 000002.png\nduration 0.400000\n" +
		"file 000002.png\n"
	if got := r.playlist(start.Add(500 * time.Millisecond)); got != want {
		t.Errorf("playlist() =\n%s\nwant\n%s", got, want)
	}
}
