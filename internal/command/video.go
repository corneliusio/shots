package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mostlyserious/shots/internal/artifact"
	"github.com/mostlyserious/shots/internal/browser"
	"github.com/mostlyserious/shots/internal/config"
	"github.com/mostlyserious/shots/internal/process"
	"github.com/mostlyserious/shots/internal/video"
)

type videoCmd struct {
	Storyboard string `arg:"" help:"YAML storyboard file, or - for standard input."`

	Output      string `short:"o" placeholder:"FILE" help:"Write the WebM here, overriding the storyboard's output:."`
	MP4         bool   `name:"mp4" help:"Also write an MP4 beside the WebM."`
	KeepFrames  bool   `help:"Keep the captured frames after a successful encode."`
	LeaveServer bool   `help:"Leave the server the storyboard starts running."`

	sessionFlags
}

func (c *videoCmd) Run(ctx context.Context, e *env) error {
	if err := c.sessionFlags.validate(); err != nil {
		return err
	}
	if c.Output == artifact.Stdout {
		return usagef("--output - cannot be used: a video is written to a file")
	}
	story, err := c.load(e)
	if err != nil {
		return err
	}
	output := story.Output
	if c.Output != "" {
		output = c.Output
	}
	if output == "" {
		return usagef("the storyboard must set output:, or --output must name a file")
	}

	r := &recording{cmd: c, env: e, story: story, output: output}
	// Runs on every path out, including a cancelled context: the server is
	// stopped, Chrome is closed, and the spool is dealt with either way.
	defer r.close()
	return r.run(ctx)
}

// load reads the storyboard, from standard input when it is "-". An invalid one
// is a usage error, so it exits 2 with its field path.
func (c *videoCmd) load(e *env) (*config.Storyboard, error) {
	story, err := c.read(e)
	var invalid *config.Error
	if errors.As(err, &invalid) {
		return nil, usageError{err}
	}
	return story, err
}

func (c *videoCmd) read(e *env) (*config.Storyboard, error) {
	if c.Storyboard != artifact.Stdout {
		return config.LoadStoryboard(c.Storyboard)
	}
	data, err := io.ReadAll(e.stdin)
	if err != nil {
		return nil, err
	}
	// A storyboard on standard input resolves its paths against the current
	// directory, which is what an empty base directory leaves them at.
	return config.ParseStoryboard(data, "")
}

// recording is one run of a storyboard: the state that outlives a single scene.
type recording struct {
	cmd    *videoCmd
	env    *env
	story  *config.Storyboard
	output string

	browser *browser.Browser
	bctx    *browser.Context
	page    *browser.Page
	server  *process.Server
	frames  *video.Recorder
	// width and height are the size the window settled at, which is what the
	// screencast captures and therefore what the video is.
	width, height int
	// keep preserves the spool for diagnosis, which a failed encode asks for.
	keep bool
}

func (r *recording) run(ctx context.Context) error {
	if err := r.setup(ctx); err != nil {
		return err
	}
	if err := r.open(ctx); err != nil {
		return err
	}
	if err := r.start(ctx); err != nil {
		return err
	}
	for i, scene := range r.story.Scenes {
		if err := r.scene(ctx, i, scene); err != nil {
			return err
		}
	}
	return r.finish(ctx)
}

// setup runs the storyboard's own preparation: a shell step, a Python step, and
// a server that runs for as long as the recording does.
func (r *recording) setup(ctx context.Context) error {
	if err := r.steps(ctx, r.story.Sh, r.story.Python); err != nil {
		return err
	}
	if r.story.Server.Empty() {
		return nil
	}
	started := r.command(r.story.Server)
	if r.cmd.LeaveServer {
		// A server that outlives shots cannot write to a pipe shots owns: the
		// read end closes at exit and the next line the server logs kills it.
		started.Output = os.Stderr
	}
	server, err := process.Start(started)
	if err != nil {
		return err
	}
	r.server = server
	// The page the server was started for is the one to dial. A storyboard
	// that names no address gets no wait, as a multi-shot configuration does:
	// there is nothing to dial, and a fixed sleep would be a worse promise.
	addr := hostPort(r.startURL())
	if addr == "" {
		return nil
	}
	r.env.progress(r.cmd.Silent, "Waiting for %s", addr)
	return server.Ready(ctx, addr, process.ReadyTimeout)
}

// startURL is the first page the recording loads, which a storyboard may name
// either at the top level or in its first scene.
func (r *recording) startURL() string {
	if r.story.URL != "" {
		return r.story.URL
	}
	return r.story.Scenes[0].Open
}

// steps runs a shell and a Python command, in that order.
func (r *recording) steps(ctx context.Context, sh config.Command, python string) error {
	if !sh.Empty() {
		if err := process.Run(ctx, r.command(sh)); err != nil {
			return err
		}
	}
	if python == "" {
		return nil
	}
	interpreter, err := process.Python()
	if err != nil {
		return err
	}
	return process.Run(ctx, r.command(config.Command{Args: []string{interpreter, "-c", python}}))
}

// command turns a configured command into one to run: in the storyboard's own
// directory, with everything it prints going to standard error.
func (r *recording) command(c config.Command) process.Cmd {
	return process.Cmd{Shell: c.Shell, Args: c.Args, Dir: r.story.BaseDir, Output: r.env.stderr}
}

// open launches Chrome, sizes the window to the storyboard's viewport, and
// loads the starting page.
func (r *recording) open(ctx context.Context) error {
	state, err := loadState(r.cmd.Auth)
	if err != nil {
		return err
	}
	width, height := r.story.Size()
	launch := r.cmd.browserOptions()
	// A screencast captures the window, so the window is what has to be the
	// storyboard's size. Chrome is asked for it here and trimmed to it below.
	launch.WindowWidth, launch.WindowHeight = width, height
	if r.browser, err = browser.Connect(ctx, launch); err != nil {
		return err
	}
	if r.bctx, err = r.browser.NewContext(ctx, state); err != nil {
		return err
	}
	opts := browser.PageOptions{}
	if cursor := r.story.Cursor; cursor.Enabled() {
		opts.InitScript = video.CursorScript(video.Cursor{
			Visible:   cursor.Visible,
			Clicks:    cursor.Clicks,
			Color:     cursor.Color,
			Size:      cursor.Size,
			ClickSize: cursor.ClickSize,
		})
	}
	if r.page, err = r.cmd.newPage(ctx, r.env, r.bctx, opts); err != nil {
		return err
	}
	// The window is what the screencast captures, and Chrome will not make one
	// arbitrarily small, so the size reached is the size recorded.
	if r.width, r.height, err = r.page.Fit(ctx, width, height); err != nil {
		return err
	}
	if r.width != width || r.height != height {
		r.env.progress(r.cmd.Silent, "Recording at %dx%d: the browser would not size its window to %dx%d",
			r.width, r.height, width, height)
	}
	if r.story.URL != "" {
		if err := r.goTo(ctx, r.story.URL); err != nil {
			return err
		}
	}
	if err := r.page.Wait(ctx, seconds(r.story.Wait)); err != nil {
		return err
	}
	if err := r.waitFor(ctx, r.story.WaitFor, r.story.WaitForURL); err != nil {
		return err
	}
	return r.page.Exec(ctx, r.story.JavaScript)
}

// start opens the frame spool and begins the screencast. Recording starts after
// the page is ready, so the video does not open on a blank tab.
func (r *recording) start(ctx context.Context) error {
	frames, err := video.New(filepath.Dir(r.output))
	if err != nil {
		return err
	}
	r.frames = frames
	r.env.progress(r.cmd.Silent, "Recording video to %q", r.output)
	return r.page.Record(ctx, r.width, r.height, func(f browser.Frame) { frames.Add(f.Data, f.At) })
}

func (r *recording) scene(ctx context.Context, i int, scene config.Scene) error {
	name := scene.Name
	if name == "" {
		name = fmt.Sprintf("Scene %d", i+1)
	}
	r.env.progress(r.cmd.Silent, "Scene %d: %s", i+1, name)
	if err := r.steps(ctx, scene.Sh, scene.Python); err != nil {
		return err
	}
	if scene.Open != "" {
		if err := r.goTo(ctx, scene.Open); err != nil {
			return err
		}
	}
	if err := r.waitFor(ctx, scene.WaitFor, scene.WaitForURL); err != nil {
		return err
	}
	for _, action := range scene.Do {
		if err := r.act(ctx, action); err != nil {
			return fmt.Errorf("scene %d (%s): %s: %w", i+1, name, action.Kind, err)
		}
	}
	return nil
}

func (r *recording) act(ctx context.Context, a config.Action) error {
	switch a.Kind {
	case "click":
		return r.page.Click(ctx, a.Selector, a.Button, a.Count)
	case "type":
		return r.page.Type(ctx, a.Selector, a.Text, time.Duration(a.DelayMS)*time.Millisecond)
	case "fill":
		return r.page.Fill(ctx, a.Selector, a.Text)
	case "press":
		return r.page.Press(ctx, a.Selector, a.Key)
	case "scroll":
		return r.page.Scroll(ctx, a.To, a.X, a.Y, a.Duration)
	case "pause":
		return r.page.Wait(ctx, seconds(a.Seconds))
	case "wait_for":
		return r.page.WaitForSelector(ctx, a.Selector)
	case "wait_for_url":
		return r.page.WaitForURL(ctx, a.URL)
	case "open":
		return r.goTo(ctx, a.URL)
	case "javascript":
		return r.page.Exec(ctx, a.Code)
	case "screenshot":
		return r.screenshot(ctx, a)
	case "sh":
		return process.Run(ctx, r.command(a.Command))
	case "python":
		return r.steps(ctx, config.Command{}, a.Code)
	}
	return fmt.Errorf("unknown action %q", a.Kind)
}

// goTo navigates, resolving a relative URL against the page's current one, and
// applies the --skip and --fail policy.
func (r *recording) goTo(ctx context.Context, raw string) error {
	current, err := r.page.URL(ctx)
	if err != nil {
		return err
	}
	target, err := resolveURL(raw, current)
	if err != nil {
		return err
	}
	status, err := r.page.Navigate(ctx, target)
	if err != nil {
		return err
	}
	return r.cmd.check(r.env, status, target)
}

func (r *recording) waitFor(ctx context.Context, selector, pattern string) error {
	if selector != "" {
		if err := r.page.WaitForSelector(ctx, selector); err != nil {
			return err
		}
	}
	if pattern == "" {
		return nil
	}
	return r.page.WaitForURL(ctx, pattern)
}

func (r *recording) screenshot(ctx context.Context, a config.Action) error {
	shot := browser.ShotOptions{FullPage: a.FullPage}
	if a.Selector != "" {
		region, err := r.page.Region(ctx, browser.Selectors{CSS: []string{a.Selector}})
		if err != nil {
			return err
		}
		shot.Clip = &region
	}
	data, err := r.page.Screenshot(ctx, shot)
	if err != nil {
		return err
	}
	target, err := r.page.URL(ctx)
	if err != nil {
		return err
	}
	return r.cmd.emit(r.env, "Screenshot", target, a.Output, bytes.NewReader(data), artifact.Options{})
}

// finish stops the screencast and encodes what it captured. The frames are kept
// when the encode fails, so the failure can be looked at.
func (r *recording) finish(ctx context.Context) error {
	if err := r.page.StopRecording(ctx); err != nil {
		return err
	}
	end := time.Now()
	if err := r.frames.Close(); err != nil {
		r.keep = true
		return err
	}
	opts := video.Options{WebM: r.output, End: end}
	if r.cmd.MP4 {
		opts.MP4 = strings.TrimSuffix(r.output, filepath.Ext(r.output)) + ".mp4"
	}
	if err := r.frames.Encode(ctx, opts); err != nil {
		r.keep = true
		return fmt.Errorf("%w (frames kept in %q)", err, r.frames.Dir())
	}
	r.env.progress(r.cmd.Silent, "Video written to %q (%d frames)", r.output, r.frames.Frames())
	if opts.MP4 != "" {
		r.env.progress(r.cmd.Silent, "MP4 written to %q", opts.MP4)
	}
	return nil
}

// close stops everything the recording started. Cleanup errors are reported
// rather than returned: they must not hide whatever ended the run.
func (r *recording) close() {
	if r.page != nil {
		r.page.Close()
	}
	if r.bctx != nil {
		r.bctx.Close()
	}
	if r.browser != nil {
		r.browser.Close()
	}
	if r.server != nil {
		stop := r.server.Stop
		if r.cmd.LeaveServer {
			stop = r.server.Detach
		}
		if err := stop(); err != nil {
			fmt.Fprintln(r.env.stderr, "shots:", err)
		}
	}
	if r.frames == nil {
		return
	}
	r.frames.Close()
	if r.keep || r.cmd.KeepFrames {
		r.env.progress(r.cmd.Silent, "Frames kept in %q", r.frames.Dir())
		return
	}
	if err := r.frames.Remove(); err != nil {
		fmt.Fprintln(r.env.stderr, "shots:", err)
	}
}

// resolveURL turns a storyboard's URL into one to navigate to. A relative URL
// resolves against the page's current address, so a scene can open "/pricing";
// anything else goes through the same normalization the CLI arguments do.
func resolveURL(raw, base string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", usagef("a URL or path is required")
	}
	if _, err := os.Stat(raw); err == nil {
		return NormalizeURL(raw)
	}
	if u, err := url.Parse(raw); err == nil && u.Scheme != "" && u.Host != "" {
		return raw, nil
	}
	if b, err := url.Parse(base); err == nil && b.Scheme != "" && b.Host != "" {
		if ref, err := url.Parse(raw); err == nil {
			return b.ResolveReference(ref).String(), nil
		}
	}
	return NormalizeURL(raw)
}

// hostPort is the address to dial to know a server is answering: the host and
// port of an http or https URL. Anything else has nothing to dial.
func hostPort(rawURL string) string {
	target, err := NormalizeURL(rawURL)
	if err != nil {
		return ""
	}
	u, err := url.Parse(target)
	if err != nil || u.Host == "" {
		return ""
	}
	port := u.Port()
	switch {
	case port != "":
	case u.Scheme == "http":
		port = "80"
	case u.Scheme == "https":
		port = "443"
	default:
		return ""
	}
	return net.JoinHostPort(u.Hostname(), port)
}

// seconds converts a storyboard's fractional seconds to a duration.
func seconds(f float64) time.Duration {
	if f <= 0 {
		return 0
	}
	return time.Duration(f * float64(time.Second))
}
