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
	"slices"
	"time"

	"github.com/mostlyserious/shots/internal/artifact"
	"github.com/mostlyserious/shots/internal/browser"
	"github.com/mostlyserious/shots/internal/config"
	"github.com/mostlyserious/shots/internal/har"
	"github.com/mostlyserious/shots/internal/process"
)

type multiCmd struct {
	Config string `arg:"" help:"YAML configuration file, or - for standard input."`

	Output      []string `short:"o" sep:"none" placeholder:"FILE" help:"Take only the shots writing to these outputs; repeatable."`
	NoClobber   bool     `short:"n" help:"Fail instead of overwriting an existing output file."`
	FailOnError bool     `help:"Stop at the first shot that fails, instead of reporting it and carrying on."`
	LeaveServer bool     `help:"Leave the servers the configuration starts running."`

	scaleFlags
	harFlags
	sessionFlags
}

func (c *multiCmd) Run(ctx context.Context, e *env) error {
	if err := c.validate(); err != nil {
		return err
	}
	cfg, err := c.load(e)
	if err != nil {
		return err
	}
	// An empty configuration is a successful no-op: no browser, no HAR, no file.
	if len(cfg.Entries) == 0 {
		return nil
	}
	harPath, harZip, err := c.harFlags.path()
	if err != nil {
		return err
	}
	state, err := loadState(c.Auth)
	if err != nil {
		return err
	}
	b := &batch{cmd: c, env: e, cfg: cfg, state: state, harPath: harPath, harZip: harZip}
	if harPath != "" {
		b.har = har.NewRecorder(Version)
	}
	for _, output := range c.Output {
		b.outputs = append(b.outputs, cfg.Resolve(output))
	}
	// Runs on every path out, including a cancelled context: servers are
	// stopped and Chrome is closed whether the batch succeeded or not.
	defer b.close()
	return b.run(ctx)
}

func (c *multiCmd) validate() error {
	if err := c.sessionFlags.validate(); err != nil {
		return err
	}
	return c.scaleFlags.validate()
}

// load reads the configuration, from standard input when it is "-". A malformed
// or invalid configuration is a usage error, so it exits 2 with its field path,
// as PLAN.md Section 10 requires; an unreadable file is an ordinary failure.
func (c *multiCmd) load(e *env) (*config.Multi, error) {
	cfg, err := c.read(e)
	var invalid *config.Error
	if errors.As(err, &invalid) {
		return nil, usageError{err}
	}
	return cfg, err
}

func (c *multiCmd) read(e *env) (*config.Multi, error) {
	if c.Config != artifact.Stdout {
		return config.LoadMulti(c.Config)
	}
	data, err := io.ReadAll(e.stdin)
	if err != nil {
		return nil, err
	}
	// A configuration on standard input resolves its paths against the current
	// directory, which is what an empty base directory leaves them at.
	return config.ParseMulti(data, "")
}

// batch is one run of a multi-shot configuration: the state that outlives a
// single entry. One Chrome process serves every shot, and it is only started
// when the first shot needs it.
type batch struct {
	cmd   *multiCmd
	env   *env
	cfg   *config.Multi
	state *browser.State

	// outputs are the --output filters, resolved the way the configuration's
	// own paths are. Empty takes every shot.
	outputs []string

	har     *har.Recorder
	harPath string
	harZip  bool

	browser *browser.Browser
	servers []*process.Server
	failed  int
}

// run executes the entries in source order. A failed step stops the batch,
// because everything after it was written expecting it to have run; a failed
// shot is reported and the batch carries on, unless --fail-on-error.
func (b *batch) run(ctx context.Context) error {
	for i, entry := range b.cfg.Entries {
		if err := b.steps(ctx, entry, b.cfg.Entries[i:]); err != nil {
			return err
		}
		if entry.URL == "" || !b.wanted(entry) {
			continue
		}
		switch err := b.shot(ctx, entry); {
		case err == nil, errors.Is(err, errSkipped):
		case b.cmd.FailOnError, ctx.Err() != nil:
			return err
		default:
			fmt.Fprintln(b.env.stderr, "shots:", err)
			b.failed++
		}
	}
	if b.har != nil {
		if err := b.cmd.writeHAR(b.env, b.har, b.harPath, b.harZip); err != nil {
			return err
		}
	}
	if b.failed > 0 {
		return fmt.Errorf("%d of %d entries failed", b.failed, len(b.cfg.Entries))
	}
	return nil
}

// steps runs an entry's sh, python and server parts, in that order. rest is the
// entry and the ones after it, which is where a server's readiness address
// comes from.
func (b *batch) steps(ctx context.Context, entry config.Entry, rest []config.Entry) error {
	if !entry.Sh.Empty() {
		if err := process.Run(ctx, b.command(entry.Sh)); err != nil {
			return err
		}
	}
	if entry.Python != "" {
		python, err := process.Python()
		if err != nil {
			return err
		}
		cmd := b.command(config.Command{Args: []string{python, "-c", entry.Python}})
		if err := process.Run(ctx, cmd); err != nil {
			return err
		}
	}
	if !entry.Server.Empty() {
		return b.serve(ctx, entry.Server, serverAddress(rest))
	}
	return nil
}

// serve starts a server and waits for it to answer, so that the shots after it
// do not race its startup. A configuration that names no address to dial gets
// no wait.
func (b *batch) serve(ctx context.Context, cmd config.Command, addr string) error {
	started := b.command(cmd)
	if b.cmd.LeaveServer {
		// A server that outlives shots cannot write to anything shots owns: the
		// pipe's read end closes at exit, and the next line the server logs
		// kills it. It gets shots' own standard error instead, which stays open
		// for as long as whoever opened it keeps it.
		started.Output = os.Stderr
	}
	server, err := process.Start(started)
	if err != nil {
		return err
	}
	b.servers = append(b.servers, server)
	if addr == "" {
		return nil
	}
	b.env.progress(b.cmd.Silent, "Waiting for %s", addr)
	return server.Ready(ctx, addr, process.ReadyTimeout)
}

// command turns a configured command into one to run. It inherits the
// configuration file's directory, like every relative path in the file, and
// everything it prints goes to standard error, which keeps standard output
// reserved for artifacts.
func (b *batch) command(c config.Command) process.Cmd {
	return process.Cmd{Shell: c.Shell, Args: c.Args, Dir: b.cfg.BaseDir, Output: b.env.stderr}
}

// wanted reports whether --output asked for this entry's shot.
func (b *batch) wanted(entry config.Entry) bool {
	return len(b.outputs) == 0 || slices.Contains(b.outputs, entry.Output)
}

// shot takes one entry's screenshot on a fresh page. When a HAR is being
// recorded, an entry with no output still loads its page — the traffic is the
// point — but writes no image, which is what upstream's injected skip_shot key
// does.
func (b *batch) shot(ctx context.Context, entry config.Entry) error {
	target, err := NormalizeURL(entry.URL)
	if err != nil {
		return err
	}
	script, err := b.script(ctx, entry)
	if err != nil {
		return err
	}
	jpeg := entry.Quality > 0 || isJPEGPath(entry.Output)
	output := ""
	if entry.Output != "" || b.har == nil {
		ext := "png"
		if jpeg {
			ext = "jpg"
		}
		if output, err = artifact.Path(entry.Output, target, ext); err != nil {
			return err
		}
	}

	page, err := b.page(ctx, entry)
	if err != nil {
		return err
	}
	defer func() {
		if b.har != nil {
			page.FlushHAR()
		}
		page.Close()
	}()

	status, err := page.Navigate(ctx, target)
	if err != nil {
		return err
	}
	if err := b.cmd.check(b.env, status, target); err != nil {
		return err
	}
	if err := prepare(ctx, page, time.Duration(entry.Wait)*time.Millisecond, script, entry.WaitFor); err != nil {
		return err
	}
	if output == "" {
		return nil
	}

	shot := browser.ShotOptions{
		JPEG:           jpeg,
		Quality:        entry.Quality,
		OmitBackground: entry.OmitBackground,
		FullPage:       entry.Height == 0,
	}
	selectors := browser.Selectors{
		CSS:     entry.Selectors,
		CSSAll:  entry.SelectorsAll,
		JS:      entry.JSSelectors,
		JSAll:   entry.JSSelectorsAll,
		Padding: entry.Padding,
	}
	if !selectors.Empty() {
		region, err := page.Region(ctx, selectors)
		if err != nil {
			return err
		}
		shot.Clip = &region
	}
	data, err := page.Screenshot(ctx, shot)
	if err != nil {
		return err
	}
	return b.cmd.emit(b.env, "Screenshot", target, output, bytes.NewReader(data), artifact.Options{NoClobber: b.cmd.NoClobber})
}

// page opens a fresh page for one entry, connecting to Chrome on the first one.
func (b *batch) page(ctx context.Context, entry config.Entry) (*browser.Page, error) {
	if b.browser == nil {
		connected, err := browser.Connect(ctx, b.cmd.browserOptions())
		if err != nil {
			return nil, err
		}
		b.browser = connected
	}
	width, height := entry.Width, entry.Height
	if width == 0 {
		width = defaultWidth
	}
	if height == 0 {
		height = defaultHeight
	}
	return b.cmd.newPage(ctx, b.env, b.browser, b.state, browser.PageOptions{
		Width:       width,
		Height:      height,
		ScaleFactor: b.cmd.factor(),
		HAR:         b.har,
	})
}

// script resolves an entry's JavaScript, which the configuration gives either
// inline or as a file.
func (b *batch) script(ctx context.Context, entry config.Entry) (string, error) {
	if entry.JSFile == "" {
		return entry.JavaScript, nil
	}
	return readScript(ctx, b.env.stdin, entry.JSFile)
}

// close stops everything the batch started. Cleanup errors are reported rather
// than returned: they must not hide whatever ended the run.
func (b *batch) close() {
	if b.browser != nil {
		b.browser.Close()
	}
	for _, server := range slices.Backward(b.servers) {
		stop := server.Stop
		if b.cmd.LeaveServer {
			stop = server.Detach
		}
		if err := stop(); err != nil {
			fmt.Fprintln(b.env.stderr, "shots:", err)
		}
	}
}

// serverAddress is the address a server configured in entries[0] is expected to
// answer on: the host and port of the first http or https URL between there and
// the next server: entry, which is the page this server was started for. A URL
// is normalized first, so the bare form the rest of the tool accepts is dialed
// too. Nothing to dial means nothing to wait for.
func serverAddress(entries []config.Entry) string {
	for i, entry := range entries {
		// The URLs after the next server: belong to that server, which has not
		// been started yet; waiting for one of them here could never succeed.
		if i > 0 && !entry.Server.Empty() {
			break
		}
		if entry.URL == "" {
			continue
		}
		target, err := NormalizeURL(entry.URL)
		if err != nil {
			continue
		}
		u, err := url.Parse(target)
		if err != nil || u.Host == "" {
			continue
		}
		port := u.Port()
		switch {
		case port != "":
		case u.Scheme == "http":
			port = "80"
		case u.Scheme == "https":
			port = "443"
		default:
			continue
		}
		return net.JoinHostPort(u.Hostname(), port)
	}
	return ""
}
