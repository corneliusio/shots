// Package process runs the external commands a configuration asks for: shell
// steps, Python steps, and background servers.
//
// Every child starts in its own process group — a job object on Windows — so
// that stopping it stops whatever it started, and not only the direct child.
// Cleanup asks politely first, waits, then insists, and always reaps the child.
package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Grace is how long a child has to exit after being asked to.
const Grace = 5 * time.Second

// ReadyTimeout is how long [Server.Ready] waits for a server to answer, per
// PLAN.md Section 7.
const ReadyTimeout = 30 * time.Second

// Cmd is one external command. Shell is a command line for the platform shell;
// Args is an argument vector executed directly. Args wins when both are set.
type Cmd struct {
	Shell string
	Args  []string
	// Dir is the working directory. Empty means the current one.
	Dir string
	// Output receives the child's standard output and standard error. Nil
	// discards both. The child never inherits standard input.
	Output io.Writer
}

// label is the command as it appears in an error message.
func (c Cmd) label() string {
	if len(c.Args) > 0 {
		return strings.Join(c.Args, " ")
	}
	line, _, _ := strings.Cut(strings.TrimSpace(c.Shell), "\n")
	return line
}

// Run runs c to completion. Cancelling ctx stops the child's whole process
// group and returns ctx's error, with any cleanup error joined to it rather
// than hiding either one.
func Run(ctx context.Context, c Cmd) error {
	s, err := Start(c)
	if err != nil {
		return err
	}
	select {
	case <-s.done:
		s.tree.close()
		if s.err != nil {
			return fmt.Errorf("%s: %w", s.label, s.err)
		}
		return nil
	case <-ctx.Done():
		return errors.Join(ctx.Err(), s.Stop())
	}
}

// Server is a command running in the background. The caller must call
// [Server.Stop] exactly once.
type Server struct {
	label string
	tree  *tree
	done  chan struct{}
	// err is the child's exit status, valid once done is closed.
	err error
}

// Start runs c in the background.
func Start(c Cmd) (*Server, error) {
	var cmd *exec.Cmd
	switch {
	case len(c.Args) > 0:
		cmd = exec.Command(c.Args[0], c.Args[1:]...)
	case strings.TrimSpace(c.Shell) != "":
		cmd = shellCmd(c.Shell)
	default:
		return nil, errors.New("empty command")
	}
	cmd.Dir = c.Dir
	// One writer for both streams: os/exec gives them the same pipe, so the
	// child's output cannot interleave mid-line with itself.
	cmd.Stdout, cmd.Stderr = c.Output, c.Output

	s := &Server{label: c.label(), done: make(chan struct{})}
	t, err := newTree(cmd)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", s.label, err)
	}
	s.tree = t
	if err := cmd.Start(); err != nil {
		t.close()
		return nil, fmt.Errorf("%s: %w", s.label, err)
	}
	// Anything the child leaves running inherits the pipe its output goes
	// through, so reaping can outlast the child itself. WaitDelay bounds that
	// wait; the ErrWaitDelay it then reports means "output left open", not a
	// command that failed.
	cmd.WaitDelay = Grace
	go func() {
		if err := cmd.Wait(); !errors.Is(err, exec.ErrWaitDelay) {
			s.err = err
		}
		close(s.done)
	}()
	if err := t.attach(cmd); err != nil {
		// The child is running but outside the tree, so it has to be killed
		// directly; nothing else will find it later.
		cmd.Process.Kill()
		<-s.done
		t.close()
		return nil, fmt.Errorf("%s: %w", s.label, err)
	}
	return s, nil
}

// Stop terminates the server's process group: a termination signal, [Grace] to
// act on it, then a kill, then reaping. Errors are returned, not logged, so the
// caller decides how loudly to report them.
func (s *Server) Stop() error {
	defer s.tree.close()
	// The group is signalled even when the direct child has already exited:
	// what it started keeps the group, and may still be running.
	err := s.tree.terminate()
	select {
	case <-s.done:
		return err
	case <-time.After(Grace):
	}
	err = errors.Join(err, s.tree.kill())
	<-s.done
	return err
}

// Ready waits until addr accepts a TCP connection, which is as close as a
// server's own port gets to reporting that it is up. It gives up when the
// server exits first, when ctx is cancelled, or after timeout.
func (s *Server) Ready(ctx context.Context, addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			conn.Close()
			return nil
		}
		select {
		case <-s.done:
			return fmt.Errorf("%s: exited before %s answered%s", s.label, addr, exit(s.err))
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s: %s did not answer within %s", s.label, addr, timeout)
		}
	}
}

func exit(err error) string {
	if err == nil {
		return ""
	}
	return " (" + err.Error() + ")"
}

// Python resolves the interpreter for a python: step, in PLAN.md Section 9's
// order: $SHOTS_PYTHON, python3, python. It is called only when a configuration
// uses one, so a machine without Python runs everything else unaffected.
func Python() (string, error) {
	candidates := []string{os.Getenv("SHOTS_PYTHON"), "python3", "python"}
	var tried []string
	for _, name := range candidates {
		if name == "" {
			continue
		}
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
		tried = append(tried, name)
	}
	return "", fmt.Errorf("no Python interpreter found (tried %s); set $SHOTS_PYTHON", strings.Join(tried, ", "))
}
