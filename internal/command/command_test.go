package command

import (
	"slices"
	"testing"
)

func TestResolve(t *testing.T) {
	commands := []string{"shot", "install", "doctor"}
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{"no arguments run shot", nil, []string{"shot"}},
		{"a URL runs shot", []string{"example.com"}, []string{"shot", "example.com"}},
		{"a flag runs shot", []string{"-w", "800", "example.com"}, []string{"shot", "-w", "800", "example.com"}},
		{"a subcommand wins", []string{"doctor"}, []string{"doctor"}},
		{"an explicit shot stays", []string{"shot", "install"}, []string{"shot", "install"}},
		{"root help stays at the root", []string{"--help"}, []string{"--help"}},
		{"root version stays at the root", []string{"--version"}, []string{"--version"}},
		{"a colliding host needs the explicit form", []string{"install"}, []string{"install"}},
		{"a path form escapes the collision", []string{"./install"}, []string{"shot", "./install"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolve(tt.args, commands); !slices.Equal(got, tt.want) {
				t.Errorf("resolve(%q) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}
