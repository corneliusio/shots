package command

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/mostlyserious/shots/internal/browser"
)

// tree runs the accessibility command and decodes its output.
func tree(t *testing.T, args ...string) browser.AXNode {
	t.Helper()
	got := run(t, append([]string{"accessibility"}, args...)...)
	if got.code != exitOK {
		t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
	}
	var root browser.AXNode
	if err := json.Unmarshal(got.stdout, &root); err != nil {
		t.Fatalf("%v in:\n%s", err, got.stdout)
	}
	return root
}

// nodes flattens a tree in document order.
func nodes(root browser.AXNode) []browser.AXNode {
	out := []browser.AXNode{root}
	for _, child := range root.Children {
		out = append(out, nodes(child)...)
	}
	return out
}

// find returns the first node with this role and name.
func find(t *testing.T, root browser.AXNode, role, name string) browser.AXNode {
	t.Helper()
	for _, n := range nodes(root) {
		if n.Role == role && n.Name == name {
			return n
		}
	}
	t.Fatalf("no %s named %q in the tree", role, name)
	return browser.AXNode{}
}

func TestAccessibility(t *testing.T) {
	srv := site(t)
	root := tree(t, srv.URL+"/accessibility.html")

	if root.Role != "RootWebArea" || root.Name != "Accessibility" {
		t.Errorf("root = %s %q, want the document", root.Role, root.Name)
	}

	t.Run("states report what is on", func(t *testing.T) {
		tests := []struct {
			role, name string
			states     []string
		}{
			{"button", "Mute", []string{"focusable", "pressed"}},
			{"button", "Send", []string{"disabled"}},
			{"textbox", "Your name", []string{"focusable", "required"}},
			{"checkbox", "", []string{"checked", "focusable"}},
		}
		for _, tt := range tests {
			node := find(t, root, tt.role, tt.name)
			for _, want := range tt.states {
				if !slices.Contains(node.States, want) {
					t.Errorf("%s %q states = %v, want %q among them", tt.role, tt.name, node.States, want)
				}
			}
		}
	})

	t.Run("an off state is left out", func(t *testing.T) {
		node := find(t, root, "button", "Mute")
		if slices.Contains(node.States, "disabled") {
			t.Errorf("states = %v, want no disabled", node.States)
		}
	})

	t.Run("a value that is not a state keeps its value", func(t *testing.T) {
		node := find(t, root, "checkbox", "Some toppings")
		if got := string(node.Properties["checked"]); got != `"mixed"` {
			t.Errorf("checked = %s, want \"mixed\"", got)
		}
		if slices.Contains(node.States, "checked") {
			t.Errorf("states = %v, want no checked for a mixed checkbox", node.States)
		}
	})

	t.Run("names, values and descriptions are resolved", func(t *testing.T) {
		if node := find(t, root, "textbox", "Your name"); string(node.Value) != `"Ada"` {
			t.Errorf("value = %s, want \"Ada\"", node.Value)
		}
		if node := find(t, root, "link", "Home"); node.Description != "Ada Lovelace" {
			t.Errorf("description = %q, want the described-by text", node.Description)
		}
	})

	t.Run("hidden and transient nodes are gone", func(t *testing.T) {
		for _, n := range nodes(root) {
			if n.Name == "not exposed" {
				t.Error("an aria-hidden node reached the output")
			}
			if n.Role == "InlineTextBox" {
				t.Error("a per-line text box reached the output")
			}
		}
	})
}

func TestAccessibilityOutput(t *testing.T) {
	srv := site(t)

	t.Run("no node identifiers reach the output", func(t *testing.T) {
		got := run(t, "accessibility", srv.URL+"/accessibility.html")
		if got.code != exitOK {
			t.Fatalf("exit %d, stderr:\n%s", got.code, got.stderr)
		}
		for _, forbidden := range []string{"nodeId", "backendDOMNodeId", "parentId", "childIds", "frameId"} {
			if strings.Contains(string(got.stdout), forbidden) {
				t.Errorf("output contains %q", forbidden)
			}
		}
	})

	t.Run("JavaScript runs before the snapshot", func(t *testing.T) {
		root := tree(t, srv.URL+"/accessibility.html", "-j", "document.querySelector('#heading').textContent = 'Renamed'")
		find(t, root, "heading", "Renamed")
	})

	t.Run("the tree is stable across runs", func(t *testing.T) {
		first := run(t, "accessibility", srv.URL+"/accessibility.html")
		second := run(t, "accessibility", srv.URL+"/accessibility.html")
		if string(first.stdout) != string(second.stdout) {
			t.Error("two runs of the same page produced different trees")
		}
	})
}
