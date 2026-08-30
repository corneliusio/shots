package config

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Error is a configuration error located by field path and source line.
type Error struct {
	Path string // for example "entries[3].server"
	Line int
	Msg  string
}

func (e *Error) Error() string {
	loc := e.Path
	switch {
	case e.Line > 0 && loc != "":
		loc = fmt.Sprintf("%s (line %d)", loc, e.Line)
	case e.Line > 0:
		loc = fmt.Sprintf("line %d", e.Line)
	case loc == "":
		return e.Msg
	}
	return loc + ": " + e.Msg
}

func errorf(n *yaml.Node, path, format string, args ...any) *Error {
	err := &Error{Path: path, Msg: fmt.Sprintf(format, args...)}
	if n != nil {
		err.Line = n.Line
	}
	return err
}

// field joins a field name onto a path: entries[3] plus server. An index joins
// without a separator, so scenes plus [0].do[2] reads as scenes[0].do[2].
func field(path, name string) string {
	switch {
	case path == "":
		return name
	case name == "":
		return path
	case name[0] == '[':
		return path + name
	}
	return path + "." + name
}

// index formats an element path: entries plus 3.
func index(path string, i int) string {
	return path + "[" + strconv.Itoa(i) + "]"
}

// reroot re-parents an error raised inside a nested value onto path, so that a
// failure an [yaml.Unmarshaler] reported relative to itself still names the
// whole field path. Anything that is not a configuration error is returned
// unchanged.
func reroot(err error, path string) error {
	var inner *Error
	if !errors.As(err, &inner) {
		return err
	}
	return &Error{Path: field(path, inner.Path), Line: inner.Line, Msg: inner.Msg}
}

// decodeStruct decodes a YAML mapping into a struct pointer, rejecting unknown
// and duplicate keys. It returns the source line of every key that was present,
// so validation can report values against the line they came from.
//
// Struct fields are matched by their yaml tag. A field may also carry an
// `alias` tag listing extra spellings of the same key ("selector" for
// "selectors"); values of slice fields accumulate across their spellings.
//
// allow, when given, restricts the mapping to those fields — one struct can
// then serve several shapes, as a storyboard action does, with each shape
// rejecting the keys that belong to another.
func decodeStruct(n *yaml.Node, path string, out any, allow ...string) (map[string]int, error) {
	n = resolve(n)
	if n.Kind != yaml.MappingNode {
		return nil, errorf(n, path, "expected a mapping, got %s", kindOf(n))
	}
	v := reflect.ValueOf(out).Elem()
	fields := yamlFields(v.Type())
	if len(allow) > 0 {
		fields = only(fields, allow)
	}
	lines := make(map[string]int, len(n.Content)/2)
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, value := n.Content[i], n.Content[i+1]
		if key.Kind != yaml.ScalarNode {
			return nil, errorf(key, path, "expected a string key")
		}
		name := key.Value
		keyPath := field(path, name)
		if name == "<<" {
			return nil, errorf(key, path, "merge keys are not supported; alias the whole entry instead")
		}
		idx, known := fields[name]
		if !known {
			return nil, errorf(key, keyPath, "unknown field")
		}
		if _, seen := lines[name]; seen {
			return nil, errorf(key, keyPath, "duplicate field")
		}
		lines[name] = key.Line
		if err := decodeField(value, keyPath, v.Field(idx)); err != nil {
			return nil, err
		}
	}
	return lines, nil
}

// setField decodes a node into the struct field spelled name, which must exist.
func setField(n *yaml.Node, path string, out any, name string) error {
	v := reflect.ValueOf(out).Elem()
	idx, ok := yamlFields(v.Type())[name]
	if !ok {
		return errorf(n, path, "no field named %s", name)
	}
	return decodeField(n, path, v.Field(idx))
}

func decodeField(n *yaml.Node, path string, dst reflect.Value) error {
	fresh := reflect.New(dst.Type())
	if err := n.Decode(fresh.Interface()); err != nil {
		// A nested [yaml.Unmarshaler] reports its own field path relative to
		// itself; rooting it here is what makes scenes[0].do[2].click.selector.
		var inner *Error
		if errors.As(err, &inner) {
			return reroot(inner, path)
		}
		return errorf(n, path, "%s", yamlMessage(err))
	}
	if dst.Kind() == reflect.Slice && dst.Len() > 0 {
		dst.Set(reflect.AppendSlice(dst, fresh.Elem()))
		return nil
	}
	dst.Set(fresh.Elem())
	return nil
}

// yamlFields maps every accepted key of a struct type to its field index.
func yamlFields(t reflect.Type) map[string]int {
	fields := make(map[string]int, t.NumField())
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		fields[name] = i
		for _, alias := range strings.Split(f.Tag.Get("alias"), ",") {
			if alias != "" {
				fields[alias] = i
			}
		}
	}
	return fields
}

// only keeps the keys naming one of the allowed fields, alias spellings
// included, because an alias shares its field's index.
func only(fields map[string]int, allow []string) map[string]int {
	allowed := make(map[int]bool, len(allow))
	for _, name := range allow {
		if idx, ok := fields[name]; ok {
			allowed[idx] = true
		}
	}
	kept := make(map[string]int, len(allow))
	for name, idx := range fields {
		if allowed[idx] {
			kept[name] = idx
		}
	}
	return kept
}

// decodeSlice decodes a YAML sequence element by element, reporting a failure
// against the element's index.
func decodeSlice[T any](n *yaml.Node, out *[]T) error {
	n = resolve(n)
	if n.Kind != yaml.SequenceNode {
		return errorf(n, "", "expected a list, got %s", kindOf(n))
	}
	list := make([]T, len(n.Content))
	for i, item := range n.Content {
		if err := item.Decode(&list[i]); err != nil {
			return reroot(err, index("", i))
		}
	}
	*out = list
	return nil
}

// resolve follows a YAML alias node to its anchor.
func resolve(n *yaml.Node) *yaml.Node {
	if n != nil && n.Kind == yaml.AliasNode {
		return n.Alias
	}
	return n
}

func kindOf(n *yaml.Node) string {
	switch {
	case n == nil:
		return "nothing"
	case n.Kind == yaml.SequenceNode:
		return "a list"
	case n.Kind == yaml.MappingNode:
		return "a mapping"
	case n.Tag == "!!null":
		return "null"
	default:
		return "a value"
	}
}

// yamlMessage reduces a yaml decoding error to a single line without the
// library's own "yaml: unmarshal errors:" preamble and line prefixes, which the
// [Error] wrapper supplies more precisely.
func yamlMessage(err error) string {
	var typeErr *yaml.TypeError
	msg := err.Error()
	if errors.As(err, &typeErr) && len(typeErr.Errors) > 0 {
		msg = typeErr.Errors[0]
	}
	msg = strings.TrimPrefix(msg, "yaml: ")
	if _, rest, ok := strings.Cut(msg, ": "); ok && strings.HasPrefix(msg, "line ") {
		msg = rest
	}
	return msg
}
