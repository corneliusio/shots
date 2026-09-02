package command

import (
	"slices"
	"strings"
	"testing"
)

// The resolution rule is only useful if users can read it: PLAN.md Section 7
// requires --help to explain it.
func TestHelpDocumentsTheResolutionRule(t *testing.T) {
	got := run(t, "--help")
	if got.code != exitOK {
		t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
	}
	for _, want := range []string{"shorthand for the take command", "shots take html", "shots ./html", "doctor", "install"} {
		if !strings.Contains(string(got.stdout), want) {
			t.Errorf("--help is missing %q:\n%s", want, got.stdout)
		}
	}
}

func TestResolve(t *testing.T) {
	commands := []string{"take", "install", "doctor"}
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{"no arguments ask for help", nil, []string{"--help"}},
		{"a URL runs take", []string{"example.com"}, []string{"take", "example.com"}},
		{"a flag runs take", []string{"-w", "800", "example.com"}, []string{"take", "-w", "800", "example.com"}},
		{"a subcommand wins", []string{"doctor"}, []string{"doctor"}},
		{"an explicit take stays", []string{"take", "install"}, []string{"take", "install"}},
		{"root help stays at the root", []string{"--help"}, []string{"--help"}},
		{"root version stays at the root", []string{"--version"}, []string{"--version"}},
		{"a colliding host needs the explicit form", []string{"install"}, []string{"install"}},
		{"a path form escapes the collision", []string{"./install"}, []string{"take", "./install"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolve(tt.args, commands); !slices.Equal(got, tt.want) {
				t.Errorf("resolve(%q) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

func TestNoArgumentsPrintHelp(t *testing.T) {
	got := run(t)
	if got.code != exitOK {
		t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
	}
	if !strings.Contains(string(got.stdout), "shorthand for the take command") {
		t.Errorf("stdout is not the help:\n%s", got.stdout)
	}
}
