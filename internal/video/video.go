// Package video spools captured frames and encodes them into a video file.
//
// Frames arrive at whatever rate the browser produces them — a screencast sends
// one only when the page changes — so each carries the moment it was
// composited, and encoding turns those moments into the per-frame durations
// ffmpeg's concat demuxer takes. A page that sits still for a second is one
// frame held for a second rather than thirty copies of it.
package video

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// FPS is the constant frame rate of the encoded video. The spool is timed, not
// paced, so the encoder resamples it to something every player can seek.
const FPS = 30

// minDuration is the shortest a frame is shown. Two frames composited in the
// same millisecond would otherwise ask ffmpeg to display one for no time at
// all.
const minDuration = time.Millisecond

// buffered frames absorb a burst without blocking the browser's event
// goroutine, which is what delivers them.
const buffered = 64

// listFile is the concat demuxer's playlist, written beside the frames.
const listFile = "frames.txt"

// Recorder spools frames to a directory and encodes them.
//
// The zero value is not usable; call [New].
type Recorder struct {
	dir  string
	done chan struct{}

	// mu guards the channel against a frame that arrives while the recorder is
	// closing: the browser delivers frames from its own goroutine, and a send
	// on a closed channel would take the process down.
	mu     sync.Mutex
	closed bool
	frames chan frame

	// Written by the spool goroutine and read after done is closed.
	stamps []time.Time
	err    error
}

type frame struct {
	data []byte
	at   time.Time
}

// New starts a frame spool in a new directory under dir. The caller must call
// [Recorder.Close], and then either [Recorder.Remove] or keep the directory for
// diagnosis.
func New(dir string) (*Recorder, error) {
	spool, err := os.MkdirTemp(dir, "shots-frames-*")
	if err != nil {
		return nil, fmt.Errorf("creating a frame directory: %w", err)
	}
	r := &Recorder{dir: spool, frames: make(chan frame, buffered), done: make(chan struct{})}
	go r.spool()
	return r, nil
}

// Dir is the directory the frames are in.
func (r *Recorder) Dir() string { return r.dir }

// Frames is the number spooled so far. It is meaningful after [Recorder.Close].
func (r *Recorder) Frames() int { return len(r.stamps) }

// Add spools one frame. It is called from the browser's event goroutine, so it
// only hands the frame to the writer. A frame that arrives after
// [Recorder.Close] is dropped.
func (r *Recorder) Add(data []byte, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.closed {
		r.frames <- frame{data, at}
	}
}

// Close stops spooling and reports the first write that failed. It may be
// called more than once.
func (r *Recorder) Close() error {
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		close(r.frames)
	}
	r.mu.Unlock()
	<-r.done
	return r.err
}

// Remove deletes the spool.
func (r *Recorder) Remove() error { return os.RemoveAll(r.dir) }

// spool writes each frame to its own file. It keeps draining after a failure so
// that Add never blocks on a writer that has given up.
func (r *Recorder) spool() {
	defer close(r.done)
	for f := range r.frames {
		if r.err != nil {
			continue
		}
		name := filepath.Join(r.dir, fmt.Sprintf("%06d.png", len(r.stamps)))
		if err := os.WriteFile(name, f.data, 0o644); err != nil {
			r.err = fmt.Errorf("writing a frame: %w", err)
			continue
		}
		r.stamps = append(r.stamps, f.at)
	}
}

// RequireFFmpeg reports that ffmpeg is missing. Encoding is the whole output
// path, so a recording run asks before it launches Chrome: a storyboard that
// cannot be encoded must not be played first.
func RequireFFmpeg() error {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return errors.New("ffmpeg is required to encode a video, and is not on PATH")
	}
	return nil
}

// Options configures [Recorder.Encode].
type Options struct {
	// WebM is where the video is written.
	WebM string
	// MP4, when set, receives an H.264 copy of the same frames. It is encoded
	// from the frames rather than from the WebM, so it costs no second
	// generation of loss.
	MP4 string
	// End is when recording stopped, which is what gives the last frame its
	// duration. The zero value uses the last frame's own timestamp.
	End time.Time
}

// Encode turns the spooled frames into a video.
//
// Both outputs come out of one ffmpeg run, and each is written to a temporary
// file in its destination directory and renamed into place, so a failure leaves
// no misleading file behind.
func (r *Recorder) Encode(ctx context.Context, opts Options) error {
	if len(r.stamps) == 0 {
		return errors.New("no frames were captured")
	}
	list := filepath.Join(r.dir, listFile)
	if err := os.WriteFile(list, []byte(r.playlist(opts.End)), 0o644); err != nil {
		return fmt.Errorf("writing the frame list: %w", err)
	}

	// -xerror, because ffmpeg otherwise skips a frame it cannot read and
	// reports success: a video missing half its frames must fail, not ship.
	args := []string{"-hide_banner", "-loglevel", "error", "-xerror", "-y", "-f", "concat", "-safe", "0", "-i", list}
	var temps []*tempFile
	defer func() {
		for _, t := range temps {
			t.cleanup()
		}
	}()
	for _, out := range []struct {
		path  string
		codec []string
	}{
		{opts.WebM, []string{"-f", "webm", "-c:v", "libvpx-vp9", "-b:v", "0", "-crf", "32", "-row-mt", "1"}},
		{opts.MP4, []string{"-f", "mp4", "-c:v", "libx264", "-crf", "23", "-preset", "veryfast", "-movflags", "+faststart"}},
	} {
		if out.path == "" {
			continue
		}
		t, err := newTemp(out.path)
		if err != nil {
			return err
		}
		temps = append(temps, t)
		// yuv420p needs even dimensions, which a viewport is not obliged to
		// have; padding by a pixel keeps every frame's own pixels intact.
		args = append(args, "-vf", "pad=ceil(iw/2)*2:ceil(ih/2)*2", "-pix_fmt", "yuv420p", "-r", fmt.Sprint(FPS))
		args = append(args, out.codec...)
		args = append(args, t.name)
	}

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg: %w%s", err, reason(stderr.String()))
	}
	for _, t := range temps {
		if err := t.commit(); err != nil {
			return err
		}
	}
	return nil
}

// playlist is the concat demuxer's input: every frame, with the time until the
// next one. The last frame is named twice, because the demuxer applies a
// duration to the file before it.
func (r *Recorder) playlist(end time.Time) string {
	var b strings.Builder
	b.WriteString("ffconcat version 1.0\n")
	for i, at := range r.stamps {
		next := end
		if i+1 < len(r.stamps) {
			next = r.stamps[i+1]
		}
		d := next.Sub(at)
		if d < minDuration {
			d = minDuration
		}
		fmt.Fprintf(&b, "file %06d.png\nduration %.6f\n", i, d.Seconds())
	}
	fmt.Fprintf(&b, "file %06d.png\n", len(r.stamps)-1)
	return b.String()
}

// tempFile is an output ffmpeg writes before it is renamed into place.
type tempFile struct {
	name string
	dest string
}

func newTemp(dest string) (*tempFile, error) {
	// ffmpeg needs the file to exist so it can be opened for writing, and the
	// name has to be in the destination directory for the rename to be atomic.
	f, err := os.CreateTemp(filepath.Dir(dest), ".shots-*")
	if err != nil {
		return nil, err
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		return nil, err
	}
	return &tempFile{name: name, dest: dest}, nil
}

func (t *tempFile) commit() error {
	if err := os.Chmod(t.name, 0o644); err != nil {
		return err
	}
	if err := os.Rename(t.name, t.dest); err != nil {
		return err
	}
	t.name = ""
	return nil
}

// cleanup removes a temporary file that was never committed.
func (t *tempFile) cleanup() {
	if t.name != "" {
		os.Remove(t.name)
	}
}

// reason is the tail of ffmpeg's diagnostics, which is where it says what it
// could not do.
func reason(stderr string) string {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	if len(lines) == 0 || lines[0] == "" {
		return ""
	}
	return ": " + lines[len(lines)-1]
}
