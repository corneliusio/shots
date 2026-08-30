package browser

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/chromedp"
)

// AXNode is one node of the accessibility tree in the stable shots format
// (PLAN.md Section 7): no Chrome node identifiers, ordered children, and
// properties that are simply on folded into States.
type AXNode struct {
	Role        string                     `json:"role"`
	Name        string                     `json:"name,omitempty"`
	Value       json.RawMessage            `json:"value,omitempty"`
	Description string                     `json:"description,omitempty"`
	States      []string                   `json:"states,omitempty"`
	Properties  map[string]json.RawMessage `json:"properties,omitempty"`
	Children    []AXNode                   `json:"children,omitempty"`
}

// Accessibility returns the page's accessibility tree.
//
// Nodes Chrome marks ignored are dropped and their children take their place,
// so hidden wrappers do not shape the output, and neither do the node
// identifiers that change on every run.
func (p *Page) Accessibility(ctx context.Context) (*AXNode, error) {
	var nodes []*accessibility.Node
	err := p.run(ctx, chromedp.ActionFunc(func(c context.Context) error {
		if err := accessibility.Enable().Do(c); err != nil {
			return err
		}
		var err error
		nodes, err = accessibility.GetFullAXTree().Do(c)
		return err
	}))
	if err != nil {
		return nil, fmt.Errorf("reading the accessibility tree: %w", err)
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("the page has no accessibility tree")
	}
	byID := make(map[accessibility.NodeID]*accessibility.Node, len(nodes))
	for _, n := range nodes {
		byID[n.NodeID] = n
	}
	// The first node is the root; an ignored root would leave several trees,
	// so it is converted whole rather than lifted.
	root := convertAX(nodes[0], byID)
	return &root, nil
}

// convertAX converts one node and, recursively, its children.
func convertAX(n *accessibility.Node, byID map[accessibility.NodeID]*accessibility.Node) AXNode {
	out := AXNode{
		Role:        axText(n.Role),
		Name:        axText(n.Name),
		Description: axText(n.Description),
		Children:    axChildren(n, byID),
	}
	if n.Value != nil {
		out.Value = json.RawMessage(n.Value.Value)
	}
	for _, prop := range n.Properties {
		name := prop.Name.String()
		switch state, keep := axState(prop.Value); {
		case !keep:
		case state:
			out.States = append(out.States, name)
		default:
			if out.Properties == nil {
				out.Properties = map[string]json.RawMessage{}
			}
			out.Properties[name] = json.RawMessage(prop.Value.Value)
		}
	}
	return out
}

// axChildren converts a node's children, replacing every dropped child with its
// own children.
func axChildren(n *accessibility.Node, byID map[accessibility.NodeID]*accessibility.Node) []AXNode {
	var out []AXNode
	for _, id := range n.ChildIDs {
		child, ok := byID[id]
		switch {
		case !ok:
		case child.Ignored, axText(child.Role) == inlineTextBox:
			out = append(out, axChildren(child, byID)...)
		default:
			out = append(out, convertAX(child, byID))
		}
	}
	return out
}

// inlineTextBox is Chrome's per-line breakdown of a text node. It is dropped:
// it repeats the text of its parent, and how it splits depends on the viewport,
// which is the opposite of a stable format.
const inlineTextBox = "InlineTextBox"

// axState classifies a property. A property that is on becomes a state, one
// that is off is left out entirely, and anything else keeps its value. A
// property that points at other nodes is dropped: its value is a node
// identifier, which is exactly what the stable format must not contain.
func axState(v *accessibility.Value) (state, keep bool) {
	switch v.Type {
	case accessibility.ValueTypeIdref, accessibility.ValueTypeIdrefList,
		accessibility.ValueTypeNode, accessibility.ValueTypeNodeList,
		accessibility.ValueTypeDomRelation, accessibility.ValueTypeValueUndefined:
		return false, false
	}
	// Booleans arrive as true and false, tristates as "true", "false" and
	// "mixed"; only the last of those is worth a value of its own.
	switch string(v.Value) {
	case "true", `"true"`:
		return true, true
	case "false", `"false"`:
		return false, false
	}
	return false, true
}

// axText decodes an accessibility value that carries a string.
func axText(v *accessibility.Value) string {
	if v == nil {
		return ""
	}
	var s string
	if err := json.Unmarshal(v.Value, &s); err != nil {
		return string(v.Value)
	}
	return s
}
