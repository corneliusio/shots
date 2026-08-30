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

// Run runs c to completion and then stops whatever it left behind: a step that
// backgrounded a process keeps that process in the step's group, and leaving it
// running is the leak process groups exist to prevent. Cancelling ctx stops the
// group early and returns ctx's error, with any cleanup error joined to it
// rather than hiding either one.
func Run(ctx context.Context, c Cmd) error {
	s, err := Start(c)
	if err != nil {
		return err
	}
	select {
	case <-s.exited:
		stopped := s.Stop()
		if s.err != nil {
			return fmt.Errorf("%s: %w", s.label, s.err)
		}
		return stopped
	case <-ctx.Done():
		return errors.Join(ctx.Err(), s.Stop())
	}
}

// Server is a command running in the background. The caller must call
// [Server.Stop] or [Server.Detach] exactly once.
type Server struct {
	label string
	tree  *tree
	// exited is closed once the child itself has been reaped; done once
	// everything in its group has released its output too.
	exited chan struct{}
	done   chan struct{}
	// err is the child's exit status, valid once exited is closed.
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

	s := &Server{label: c.label(), exited: make(chan struct{}), done: make(chan struct{})}
	// The child writes into a pipe this process owns rather than one os/exec
	// owns, because os/exec's Wait also waits for every inherited copy of the
	// write end to close: a step that backgrounds a process would not be
	// reaped until that process exited too. One writer for both streams keeps
	// the child's output from interleaving with itself. copied is closed once
	// everything holding the write end has let go.
	var w *os.File
	copied := make(chan struct{})
	close(copied)
	if c.Output != nil {
		r, pipe, err := os.Pipe()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", s.label, err)
		}
		w, copied = pipe, make(chan struct{})
		cmd.Stdout, cmd.Stderr = w, w
		go func() {
			defer close(copied)
			defer r.Close()
			io.Copy(c.Output, r)
		}()
	}
	// fail releases the pipe and drains the copier, so a caller that gets only
	// an error still sees what the child managed to write, and no goroutine
	// outlives the failure.
	fail := func(err error) (*Server, error) {
		if w != nil {
			w.Close()
		}
		<-copied
		return nil, fmt.Errorf("%s: %w", s.label, err)
	}

	t, err := newTree(cmd)
	if err != nil {
		return fail(err)
	}
	s.tree = t
	if err := cmd.Start(); err != nil {
		t.close()
		return fail(err)
	}
	// The child owns the write end now.
	if w != nil {
		w.Close()
		w = nil
	}
	go func() {
		s.err = cmd.Wait()
		close(s.exited)
		<-copied
		close(s.done)
	}()
	if err := t.attach(cmd); err != nil {
		// The child is running but outside the tree, so it has to be killed
		// directly; nothing else will find it later.
		cmd.Process.Kill()
		<-s.done
		t.close()
		return fail(err)
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
	if s.settled(Grace) {
		return err
	}
	err = errors.Join(err, s.tree.kill())
	s.settled(Grace)
	return err
}

// Detach gives up the server without stopping it, which is what --leave-server
// asks for. On Windows it also has to clear the job object's kill-on-close
// limit, or the server would die with shots.
func (s *Server) Detach() error { return s.tree.detach() }

// settled reports whether the group has exited and released the child's output
// within d. The wait is bounded: something that escaped both signals must not
// hold up the rest of the cleanup.
func (s *Server) settled(d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-s.done:
		return true
	case <-timer.C:
		return false
	}
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
		case <-s.exited:
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
