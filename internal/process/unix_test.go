//go:build !windows

package process

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestStopKillsTheGroup is the reason children get their own process group: a
// shell that starts something of its own must not leave it behind.
func TestStopKillsTheGroup(t *testing.T) {
	dir := t.TempDir()
	pids := filepath.Join(dir, "pids")
	// The shell writes its own process id and its child's, then waits. Killing
	// only the direct child would leave the sleep running.
	server, err := Start(Cmd{Shell: "sleep 60 & echo $$ $! > " + pids + "; while :; do sleep 1; done"})
	if err != nil {
		t.Fatal(err)
	}
	shell, grandchild := waitForPIDs(t, pids)
	if err := server.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	waitForExit(t, "the shell", shell)
	waitForExit(t, "the grandchild", grandchild)
}

// TestRunCancelledKillsTheGroup is the same guarantee for a step that is still
// running when the run is interrupted.
func TestRunCancelledKillsTheGroup(t *testing.T) {
	dir := t.TempDir()
	pids := filepath.Join(dir, "pids")
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Cmd{Shell: "sleep 60 & echo $$ $! > " + pids + "; while :; do sleep 1; done"})
	}()
	shell, grandchild := waitForPIDs(t, pids)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * Grace):
		t.Fatal("Run did not return after its context was cancelled")
	}
	waitForExit(t, "the shell", shell)
	waitForExit(t, "the grandchild", grandchild)
}

// waitForPIDs reads the two process ids the shell reports, once it has written
// them.
func waitForPIDs(t *testing.T, path string) (int, int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if data, err := os.ReadFile(path); err == nil {
			fields := strings.Fields(string(data))
			if len(fields) == 2 {
				shell, err1 := strconv.Atoi(fields[0])
				grandchild, err2 := strconv.Atoi(fields[1])
				if err1 == nil && err2 == nil {
					return shell, grandchild
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s was never written", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitForExit fails unless the process is gone. A signalled process is reaped
// by its parent, or by init once it is orphaned, so the check is a poll.
func waitForExit(t *testing.T, what string, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("%s (%d) is still running", what, pid)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}
