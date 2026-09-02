package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// childFlag makes the test binary stand in for an arbitrary child process, so
// that the tests need no shell and no fixture scripts on any platform.
const childFlag = "-process-child"

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == childFlag {
		os.Exit(child(os.Args[2:]))
	}
	os.Exit(m.Run())
}

// child implements the modes the tests ask for:
//
//	print TEXT        write TEXT and exit
//	exit CODE         exit with CODE
//	sleep             run until it is stopped
//	listen ADDR AFTER accept connections on ADDR after AFTER milliseconds
func child(args []string) int {
	switch args[0] {
	case "print":
		fmt.Println(args[1])
	case "exit":
		code, _ := strconv.Atoi(args[1])
		return code
	case "sleep":
		forever()
	case "listen":
		after, _ := strconv.Atoi(args[2])
		time.Sleep(time.Duration(after) * time.Millisecond)
		l, err := net.Listen("tcp", args[1])
		if err != nil {
			return 1
		}
		defer l.Close()
		forever()
	}
	return 0
}

// forever blocks until the process is killed. It sleeps rather than blocking on
// a channel, which the runtime would report as a deadlock.
func forever() {
	for {
		time.Sleep(time.Hour)
	}
}

// self is a command that runs one of [child]'s modes.
func self(t *testing.T, args ...string) Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Cmd{Args: append([]string{exe, childFlag}, args...)}
}

// freeAddr returns a local address nothing is listening on yet.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func TestRun(t *testing.T) {
	tests := []struct {
		name       string
		cmd        func(*testing.T) Cmd
		wantOutput string
		wantErr    string
	}{
		{
			name:       "an argument vector runs directly",
			cmd:        func(t *testing.T) Cmd { return self(t, "print", "from the child") },
			wantOutput: "from the child",
		},
		{
			name:       "a string runs through the platform shell",
			cmd:        func(*testing.T) Cmd { return Cmd{Shell: "echo from the shell"} },
			wantOutput: "from the shell",
		},
		{
			name:    "a failing command reports its status",
			cmd:     func(t *testing.T) Cmd { return self(t, "exit", "3") },
			wantErr: "exit status 3",
		},
		{
			name:    "an executable that does not exist is an error",
			cmd:     func(*testing.T) Cmd { return Cmd{Args: []string{"shots-no-such-command"}} },
			wantErr: "shots-no-such-command",
		},
		{
			name:    "an empty command is an error",
			cmd:     func(*testing.T) Cmd { return Cmd{Shell: "  "} },
			wantErr: "empty command",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			cmd := tt.cmd(t)
			cmd.Output = &out
			err := Run(t.Context(), cmd)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("Run: %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("error = %v, want it to mention %q", err, tt.wantErr)
			}
			if got := strings.TrimSpace(out.String()); tt.wantOutput != "" && got != tt.wantOutput {
				t.Errorf("output = %q, want %q", got, tt.wantOutput)
			}
		})
	}
}

// TestRunOutputFile covers the output a child can keep: an *os.File becomes the
// child's own standard output and error, with no pipe of shots' in between, so
// what it writes survives the process that started it. That is what
// --leave-server needs.
func TestRunOutputFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cmd := self(t, "print", "into the file")
	cmd.Output = f
	if err := Run(t.Context(), cmd); err != nil {
		t.Fatalf("Run: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(data)); got != "into the file" {
		t.Errorf("the file holds %q, want the child's output", got)
	}
}

func TestRunCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, self(t, "sleep")) }()
	// Nothing observable says the child has started, so the cancellation is
	// given a moment to land on a running process rather than a starting one.
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want it to wrap context.Canceled", err)
		}
	case <-time.After(2 * Grace):
		t.Fatal("Run did not return after its context was cancelled")
	}
}

func TestServerReady(t *testing.T) {
	addr := freeAddr(t)

	t.Run("waits for the port to answer", func(t *testing.T) {
		server, err := Start(self(t, "listen", addr, "300"))
		if err != nil {
			t.Fatal(err)
		}
		defer server.Stop()
		if err := server.Ready(t.Context(), addr, 10*time.Second); err != nil {
			t.Fatalf("Ready: %v", err)
		}
	})

	t.Run("gives up on a server that exits", func(t *testing.T) {
		server, err := Start(self(t, "exit", "1"))
		if err != nil {
			t.Fatal(err)
		}
		defer server.Stop()
		err = server.Ready(t.Context(), addr, 10*time.Second)
		if err == nil || !strings.Contains(err.Error(), "exited before") {
			t.Fatalf("error = %v, want it to report the exit", err)
		}
	})

	t.Run("gives up after the timeout", func(t *testing.T) {
		server, err := Start(self(t, "sleep"))
		if err != nil {
			t.Fatal(err)
		}
		defer server.Stop()
		err = server.Ready(t.Context(), addr, 300*time.Millisecond)
		if err == nil || !strings.Contains(err.Error(), "did not answer") {
			t.Fatalf("error = %v, want a timeout", err)
		}
	})
}

func TestStop(t *testing.T) {
	addr := freeAddr(t)
	server, err := Start(self(t, "listen", addr, "0"))
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Ready(t.Context(), addr, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := server.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if conn, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		conn.Close()
		t.Error("the server is still listening after Stop")
	}
}
