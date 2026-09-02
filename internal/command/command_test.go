package command

import (
	"strings"
	"testing"
)

func TestHelpDocumentsExplicitTake(t *testing.T) {
	got := run(t, "--help")
	if got.code != exitOK {
		t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
	}
	for _, want := range []string{"Every invocation names a command", "shots take example.com", "doctor", "install"} {
		if !strings.Contains(string(got.stdout), want) {
			t.Errorf("--help is missing %q:\n%s", want, got.stdout)
		}
	}
}

func TestTakeMustBeExplicit(t *testing.T) {
	got := run(t, "example.com")
	if got.code != exitUsage {
		t.Fatalf("exit %d, want %d; stdout:\n%s\nstderr:\n%s", got.code, exitUsage, got.stdout, got.stderr)
	}
	if !strings.Contains(got.stderr, "unexpected argument example.com") {
		t.Errorf("stderr does not explain the unexpected URL:\n%s", got.stderr)
	}
}

func TestNoArgumentsPrintHelp(t *testing.T) {
	got := run(t)
	if got.code != exitOK {
		t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
	}
	if !strings.Contains(string(got.stdout), "Every invocation names a command") {
		t.Errorf("stdout is not the help:\n%s", got.stdout)
	}
}
