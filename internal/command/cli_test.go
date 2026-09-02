package command

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// childFlag makes the test binary stand in for the child processes a
// configuration starts, so that the tests need no shell and no fixture scripts
// on any platform.
const childFlag = "-child"

// binDir holds the shots binary the subprocess tests build.
var binDir string

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == childFlag {
		os.Exit(child(os.Args[2:]))
	}
	code := m.Run()
	if binDir != "" {
		os.RemoveAll(binDir)
	}
	os.Exit(code)
}

// child implements the modes the tests configure:
//
//	print TEXT         write TEXT to standard output
//	write PATH TEXT    write TEXT to PATH
//	exit CODE
//	serve ADDR AFTER   serve a page on ADDR, AFTER milliseconds from now
func child(args []string) int {
	switch args[0] {
	case "print":
		fmt.Println(args[1])
	case "write":
		if err := os.WriteFile(args[1], []byte(args[2]), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	case "exit":
		var code int
		fmt.Sscan(args[1], &code)
		return code
	case "serve":
		var after int
		fmt.Sscan(args[2], &after)
		time.Sleep(time.Duration(after) * time.Millisecond)
		l, err := net.Listen("tcp", args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		// A server left running by --leave-server is nobody's child, so it
		// bounds its own life rather than outliving the test run.
		time.AfterFunc(time.Minute, func() { os.Exit(0) })
		http.Serve(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Logging every request is what a real server does, and what makes
			// a server writing to a descriptor shots owned die once shots has
			// exited.
			fmt.Fprintln(os.Stderr, "serving", r.URL.Path)
			fmt.Fprint(w, `<!doctype html><style>html,body{margin:0}</style><div id=served>served</div>`)
		}))
	}
	return 0
}

// childArgs renders a command that runs one of [child]'s modes as a YAML flow
// sequence, which is how a configuration spells an argument vector.
func childArgs(t *testing.T, args ...string) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	line, err := json.Marshal(append([]string{exe, childFlag}, args...))
	if err != nil {
		t.Fatal(err)
	}
	return string(line)
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

// answers reports whether something accepts connections at addr.
func answers(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// get fetches a page from addr, which — unlike [answers] — makes the server
// serving it log the request.
func get(addr string) error {
	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, err = io.ReadAll(resp.Body)
	return err
}

// buildBinary builds cmd/shots once for the whole package. The subprocess tests
// need the real binary: it is the only thing that installs the signal handling
// every cleanup path hangs off.
var buildBinary = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "shots-cli")
	if err != nil {
		return "", err
	}
	binDir = dir
	path := filepath.Join(dir, "shots")
	if runtime.GOOS == "windows" {
		path += ".exe"
	}
	out, err := exec.Command("go", "build", "-o", path, "github.com/corneliusio/shots/cmd/shots").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("go build: %w: %s", err, out)
	}
	return path, nil
})

func binary(t *testing.T) string {
	t.Helper()
	path, err := buildBinary()
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// TestCLIStreams runs the real binary to check what PLAN.md Section 10 reserves
// each stream for: a step's own output must not reach the artifact stream.
func TestCLIStreams(t *testing.T) {
	dir := t.TempDir()
	cfg := writeConfig(t, dir, "- sh: "+childArgs(t, "print", "step output"))
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(binary(t), "multi", cfg)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%v; stderr:\n%s", err, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing", stdout.String())
	}
	if !strings.Contains(stderr.String(), "step output") {
		t.Errorf("stderr = %q, want the step's output", stderr.String())
	}
}

// TestCLIOutputFiles checks PLAN.md Section 10's file rules where they are
// hardest to fake: a whole process writing to a real directory.
func TestCLIOutputFiles(t *testing.T) {
	srv := site(t)
	dir := t.TempDir()
	cfg := writeConfig(t, dir, strings.Join([]string{
		"- url: " + srv.URL + "/geometry.html",
		"  output: missing.png",
		"  selector: '#absent'",
		"- url: " + srv.URL + "/geometry.html",
		"  output: taken.png",
		"  height: 100",
		"",
	}, "\n"))

	cmd := exec.Command(binary(t), "multi", cfg)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatal("a batch with a failed shot exited 0")
	}
	if _, err := os.Stat(filepath.Join(dir, "taken.png")); err != nil {
		t.Errorf("the shot that worked is missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "missing.png")); err == nil {
		t.Error("the failed shot left a file behind")
	}
	// A file is written through a temporary in its own directory, so a failed
	// write must not leave one there either.
	if leftovers, _ := filepath.Glob(filepath.Join(dir, ".shots-*")); len(leftovers) > 0 {
		t.Errorf("temporary files left behind: %v", leftovers)
	}

	// The good shot is now in the way of the next run.
	before, err := os.ReadFile(filepath.Join(dir, "taken.png"))
	if err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command(binary(t), "multi", cfg, "--no-clobber", "-o", "taken.png")
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatal("--no-clobber overwrote an existing file")
	}
	after, err := os.ReadFile(filepath.Join(dir, "taken.png"))
	if err != nil || !bytes.Equal(before, after) {
		t.Errorf("the existing file changed (%v)", err)
	}
}

// TestCLIExitCodes checks the mapping in PLAN.md Section 10 through the process
// status, not through Main's return value.
func TestCLIExitCodes(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name string
		yaml string
		want int
	}{
		{"an empty configuration succeeds", "", exitOK},
		{"an invalid configuration is a usage error", "- nope: 1\n", exitUsage},
		{"a failed step is a runtime failure", "- sh: " + childArgs(t, "exit", "3") + "\n", exitFailure},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := filepath.Join(dir, strings.ReplaceAll(tt.name, " ", "-")+".yml")
			if err := os.WriteFile(cfg, []byte(tt.yaml), 0o644); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(binary(t), "multi", cfg)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			err := cmd.Run()
			var exit *exec.ExitError
			code := 0
			if errors.As(err, &exit) {
				code = exit.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if code != tt.want {
				t.Fatalf("exit %d, want %d; stderr:\n%s", code, tt.want, stderr.String())
			}
		})
	}
}
