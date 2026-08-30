// Package command defines the shots command-line interface.
package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/alecthomas/kong"
)

// Version is the reported version, overridden at link time.
var Version = "dev"

// Exit codes, per PLAN.md Section 10.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

// usageError is an error caused by the arguments rather than by the work, and
// so exits with status 2.
type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }
func (e usageError) Unwrap() error { return e.err }

func usagef(format string, args ...any) error {
	return usageError{fmt.Errorf(format, args...)}
}

// errSkipped reports that --skip suppressed the work. It is a success.
var errSkipped = errors.New("skipped")

// cli is the whole command surface.
//
// shot is the default command and accepts arguments, so "shots example.com"
// works, while a first argument that names a subcommand always selects that
// subcommand. Capture a host that collides with one by naming the command
// ("shots shot html") or by using a path ("shots ./html").
type cli struct {
	Shot    shotCmd    `cmd:"" help:"Take a screenshot of a page or part of one."`
	Install installCmd `cmd:"" help:"Download the pinned Chrome for Testing build."`
	Doctor  doctorCmd  `cmd:"" help:"Report the browser and tools shots will use."`

	Version kong.VersionFlag `help:"Print the version and exit."`
	Help    helpFlag         `help:"Show context-sensitive help."`
}

// helpFlag replaces kong's built-in help flag, which claims -h. Upstream uses
// -h for --height, so shots offers only the long spelling.
type helpFlag bool

func (helpFlag) BeforeReset(kctx *kong.Context) error {
	if err := kctx.PrintUsage(false); err != nil {
		return err
	}
	kctx.Kong.Exit(exitOK)
	return nil
}

// env carries the streams a command reads and writes, so tests can drive the
// whole CLI without touching the process.
type env struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
}

// Main parses args and runs the selected command, returning the process exit
// status. It never calls os.Exit.
func Main(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	e := &env{stdin: stdin, stdout: stdout, stderr: stderr}
	// kong reports --help and --version by calling Exit; recording the code
	// instead lets parsing unwind normally.
	exited := -1
	parser, err := kong.New(&cli{},
		kong.Name("shots"),
		kong.Description("Take automated screenshots with Chrome."),
		kong.UsageOnError(),
		kong.Writers(stdout, stderr),
		kong.Exit(func(code int) {
			if exited < 0 {
				exited = code
			}
		}),
		kong.Vars{"version": Version},
		kong.BindTo(ctx, (*context.Context)(nil)),
		kong.Bind(e),
		kong.NoDefaultHelp(),
	)
	if err != nil {
		fmt.Fprintln(stderr, "shots:", err)
		return exitFailure
	}
	kctx, err := parser.Parse(resolve(args, commandNames(parser)))
	if exited >= 0 {
		return exited
	}
	if err != nil {
		parser.Errorf("%s", err)
		return exitUsage
	}
	return report(stderr, kctx.Run())
}

// commandNames lists the top-level subcommands.
func commandNames(parser *kong.Kong) []string {
	names := make([]string, 0, len(parser.Model.Children))
	for _, child := range parser.Model.Children {
		names = append(names, child.Name)
	}
	return names
}

// resolve applies the default-command rule: shot runs when the first argument
// is not a subcommand name, so "shots example.com" and "shots -w 800 x.html"
// both take a screenshot. Subcommand names always win, which is why a host that
// collides with one needs "shots shot html" or "shots ./html".
//
// Flags that belong to the root rather than to a command are left where they
// are, so "shots --help" still lists the commands.
func resolve(args, commands []string) []string {
	if len(args) == 0 {
		return []string{"shot"}
	}
	if args[0] == "--help" || args[0] == "--version" {
		return args
	}
	if slices.Contains(commands, args[0]) {
		return args
	}
	return append([]string{"shot"}, args...)
}

// report maps a command's error to an exit status.
func report(stderr io.Writer, err error) int {
	var usage usageError
	switch {
	case err == nil, errors.Is(err, errSkipped):
		return exitOK
	case errors.Is(err, context.Canceled):
		fmt.Fprintln(stderr, "shots: interrupted")
		return exitFailure
	case errors.As(err, &usage):
		fmt.Fprintln(stderr, "shots:", err)
		return exitUsage
	default:
		fmt.Fprintln(stderr, "shots:", err)
		return exitFailure
	}
}

// progress writes a status line to stderr unless silent.
func (e *env) progress(silent bool, format string, args ...any) {
	if !silent {
		fmt.Fprintf(e.stderr, format+"\n", args...)
	}
}
