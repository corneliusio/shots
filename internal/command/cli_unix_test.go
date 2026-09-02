//go:build !windows

package command

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestCLIInterrupt verifies that an interrupted batch takes down a server's own
// children, not just the process it started.
func TestCLIInterrupt(t *testing.T) {
	dir := t.TempDir()
	pids := filepath.Join(dir, "pids")
	// The server writes its own process id and the grandchild's, then waits.
	// The step after it keeps the batch running until the interrupt arrives.
	server, err := json.Marshal("sleep 60 & echo $$ $! > " + pids + "; while :; do sleep 1; done")
	if err != nil {
		t.Fatal(err)
	}
	cfg := writeConfig(t, dir, "- server: "+string(server)+"\n- sh: sleep 60\n")

	cmd := exec.Command(binary(t), "multi", cfg)
	// Standard error goes to a file rather than a pipe: a pipe would keep
	// cmd.Wait blocked on whatever inherited it, which is exactly what this
	// test is looking for elsewhere.
	log, err := os.Create(filepath.Join(dir, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	cmd.Stderr = log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	shell, grandchild := waitForPIDs(t, pids)

	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		cmd.Process.Kill()
		t.Fatal("shots did not exit after SIGINT")
	}
	stderr, err := os.ReadFile(filepath.Join(dir, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	if code := cmd.ProcessState.ExitCode(); code != exitFailure {
		t.Errorf("exit %d, want %d; stderr:\n%s", code, exitFailure, stderr)
	}
	if !strings.Contains(string(stderr), "interrupted") {
		t.Errorf("stderr = %q, want it to report the interruption", stderr)
	}
	waitForExit(t, "the server", shell)
	waitForExit(t, "its grandchild", grandchild)
}

// waitForPIDs reads the two process ids the server reports, once it has written
// them.
func waitForPIDs(t *testing.T, path string) (int, int) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if data, err := os.ReadFile(path); err == nil {
			if fields := strings.Fields(string(data)); len(fields) == 2 {
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
	deadline := time.Now().Add(30 * time.Second)
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
