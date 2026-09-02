// Package install wires sonar into MCP clients and coding agents by writing
// their configuration files. Every write is idempotent and touches only the
// keys sonar owns.
package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Object is a JSON object that remembers the order its keys were parsed in.
// encoding/json decodes objects into map[string]any, which loses that order
// and would reshuffle a user's hand-written config on every write, so we keep
// our own ordered model. Values are json.Number, string, bool, nil, []any, or
// *Object.
type Object struct {
	keys   []string
	values map[string]any
}

// NewObject returns an empty Object.
func NewObject() *Object {
	return &Object{values: map[string]any{}}
}

// Keys returns the keys in insertion order.
func (o *Object) Keys() []string {
	out := make([]string, len(o.keys))
	copy(out, o.keys)
	return out
}

// Get returns the value for key and whether it was present.
func (o *Object) Get(key string) (any, bool) {
	v, ok := o.values[key]
	return v, ok
}

// Object returns the value for key when it is a nested object.
func (o *Object) Object(key string) (*Object, bool) {
	v, ok := o.values[key]
	if !ok {
		return nil, false
	}
	nested, ok := v.(*Object)
	return nested, ok
}

// Set stores value under key, keeping an existing key in place and appending a
// new one at the end.
func (o *Object) Set(key string, value any) {
	if o.values == nil {
		o.values = map[string]any{}
	}
	if _, exists := o.values[key]; !exists {
		o.keys = append(o.keys, key)
	}
	o.values[key] = value
}

// Delete removes key, reporting whether it was present.
func (o *Object) Delete(key string) bool {
	if _, exists := o.values[key]; !exists {
		return false
	}
	delete(o.values, key)
	for i, k := range o.keys {
		if k == key {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
	return true
}

// Len reports how many keys the object holds.
func (o *Object) Len() int { return len(o.keys) }

// ParseObject decodes a JSON document that must be an object, preserving key
// order. Numbers are kept as json.Number so re-encoding does not turn 1 into
// 1.0 or lose precision on large integers.
func ParseObject(data []byte) (*Object, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("not valid JSON: %w", err)
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '{' {
		return nil, errors.New("not valid JSON: top level value is not an object")
	}
	obj, err := parseObjectBody(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err == nil {
		return nil, errors.New("not valid JSON: trailing content after the top level object")
	}
	return obj, nil
}

// parseObjectBody consumes tokens up to and including the closing brace.
func parseObjectBody(dec *json.Decoder) (*Object, error) {
	obj := NewObject()
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("not valid JSON: %w", err)
		}
		if delim, ok := tok.(json.Delim); ok && delim == '}' {
			return obj, nil
		}
		key, ok := tok.(string)
		if !ok {
			return nil, errors.New("not valid JSON: object key is not a string")
		}
		value, err := parseValue(dec)
		if err != nil {
			return nil, err
		}
		obj.Set(key, value)
	}
}

// parseArrayBody consumes tokens up to and including the closing bracket.
func parseArrayBody(dec *json.Decoder) ([]any, error) {
	arr := []any{}
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("not valid JSON: %w", err)
		}
		if delim, ok := tok.(json.Delim); ok && delim == ']' {
			return arr, nil
		}
		value, err := valueFromToken(dec, tok)
		if err != nil {
			return nil, err
		}
		arr = append(arr, value)
	}
}

func parseValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("not valid JSON: %w", err)
	}
	return valueFromToken(dec, tok)
}

func valueFromToken(dec *json.Decoder, tok json.Token) (any, error) {
	if delim, ok := tok.(json.Delim); ok {
		switch delim {
		case '{':
			return parseObjectBody(dec)
		case '[':
			return parseArrayBody(dec)
		}
		return nil, fmt.Errorf("not valid JSON: unexpected %q", delim)
	}
	return tok, nil
}

// Encode renders obj as JSON. An empty indent produces a single line; any
// other indent produces a pretty document. The result always ends in a
// newline.
func Encode(obj *Object, indent string) ([]byte, error) {
	var buf bytes.Buffer
	if err := encodeObject(&buf, obj, indent, ""); err != nil {
		return nil, err
	}
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

func encodeValue(buf *bytes.Buffer, v any, indent, prefix string) error {
	switch val := v.(type) {
	case *Object:
		return encodeObject(buf, val, indent, prefix)
	case []any:
		return encodeArray(buf, val, indent, prefix)
	case json.Number:
		buf.WriteString(val.String())
		return nil
	default:
		enc, err := json.Marshal(val)
		if err != nil {
			return err
		}
		buf.Write(enc)
		return nil
	}
}

func encodeObject(buf *bytes.Buffer, obj *Object, indent, prefix string) error {
	if obj.Len() == 0 {
		buf.WriteString("{}")
		return nil
	}
	inner := prefix + indent
	buf.WriteByte('{')
	for i, key := range obj.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		writeNewlineIndent(buf, indent, inner)
		enc, err := json.Marshal(key)
		if err != nil {
			return err
		}
		buf.Write(enc)
		buf.WriteByte(':')
		if indent != "" {
			buf.WriteByte(' ')
		}
		if err := encodeValue(buf, obj.values[key], indent, inner); err != nil {
			return err
		}
	}
	writeNewlineIndent(buf, indent, prefix)
	buf.WriteByte('}')
	return nil
}

func encodeArray(buf *bytes.Buffer, arr []any, indent, prefix string) error {
	if len(arr) == 0 {
		buf.WriteString("[]")
		return nil
	}
	inner := prefix + indent
	buf.WriteByte('[')
	for i, item := range arr {
		if i > 0 {
			buf.WriteByte(',')
		}
		writeNewlineIndent(buf, indent, inner)
		if err := encodeValue(buf, item, indent, inner); err != nil {
			return err
		}
	}
	writeNewlineIndent(buf, indent, prefix)
	buf.WriteByte(']')
	return nil
}

func writeNewlineIndent(buf *bytes.Buffer, indent, prefix string) {
	if indent == "" {
		return
	}
	buf.WriteByte('\n')
	buf.WriteString(prefix)
}

// DetectIndent guesses the indentation of an existing JSON document from its
// first indented line so a rewrite keeps the file's own style. It falls back
// to two spaces.
func DetectIndent(data []byte) string {
	const fallback = "  "
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" || len(trimmed) == len(line) {
			continue
		}
		return line[:len(line)-len(trimmed)]
	}
	return fallback
}

// HasComments reports whether data contains a // or /* comment marker outside
// of a JSON string. Clients such as Cursor tolerate JSONC, but encoding/json
// drops comments, so callers warn before rewriting such a file.
func HasComments(data []byte) bool {
	inString := false
	escaped := false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			continue
		}
		if c == '/' && i+1 < len(data) && (data[i+1] == '/' || data[i+1] == '*') {
			return true
		}
	}
	return false
}
