//go:build !windows

package process

import (
	"fmt"
	"os/exec"
	"syscall"
)

// tree is the child's own process group. Signalling the negative process id
// signals every member, so a shell that started its own children takes them
// with it.
type tree struct{ pid int }

func newTree(cmd *exec.Cmd) (*tree, error) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// Setpgid with no pgid makes the child a group leader, so its group id is
	// its process id. It also means the child does not receive the terminal's
	// interrupt: shots stops it deliberately instead.
	cmd.SysProcAttr.Setpgid = true
	return &tree{}, nil
}

func (t *tree) attach(cmd *exec.Cmd) error {
	t.pid = cmd.Process.Pid
	return nil
}

func (t *tree) terminate() error { return t.signal(syscall.SIGTERM) }
func (t *tree) kill() error      { return t.signal(syscall.SIGKILL) }
func (t *tree) close()           {}

// detach costs nothing here: a process group outlives the process that made it,
// so simply not signalling it leaves it running. What it writes has to outlive
// shots too, which is decided at [Start] by the kind of [Cmd].Output it is
// given, not here.
func (t *tree) detach() error { return nil }

// signal signals the whole group. An empty group is not an error: it is what
// success looks like once everything has exited.
func (t *tree) signal(sig syscall.Signal) error {
	if t.pid <= 0 {
		return nil
	}
	switch err := syscall.Kill(-t.pid, sig); err {
	case nil, syscall.ESRCH:
		return nil
	default:
		return fmt.Errorf("sending %s to process group %d: %w", sig, t.pid, err)
	}
}

// shellCmd runs a command line through the platform shell, as PLAN.md Section 9
// requires.
func shellCmd(line string) *exec.Cmd { return exec.Command("/bin/sh", "-c", line) }
