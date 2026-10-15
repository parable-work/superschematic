package enginehttp_test

// JSON as the replay reads and writes it: objects keep their members'
// order, so a document a step sends is the bytes the recording sent, and
// numbers keep their text (json.Number), compared by value as JavaScript
// compares them.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
)

// object is a JSON object with its members in order. A value is nil, a
// bool, a json.Number, a string, a []any or an *object.
type object struct {
	keys    []string
	members map[string]any
}

func newObject() *object {
	return &object{members: map[string]any{}}
}

func (o *object) get(key string) (any, bool) {
	if o == nil {
		return nil, false
	}
	value, ok := o.members[key]
	return value, ok
}

func (o *object) set(key string, value any) {
	if _, ok := o.members[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.members[key] = value
}

func (o *object) remove(key string) {
	if _, ok := o.members[key]; !ok {
		return
	}
	delete(o.members, key)
	for i, each := range o.keys {
		if each == key {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

func (o *object) len() int {
	if o == nil {
		return 0
	}
	return len(o.keys)
}

// decode reads one JSON document.
func decode(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := decodeValue(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("JSON with more after its value")
	}
	return value, nil
}

func decodeValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}
	switch delim {
	case '{':
		out := newObject()
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			value, err := decodeValue(decoder)
			if err != nil {
				return nil, err
			}
			out.set(key.(string), value)
		}
		_, err := decoder.Token()
		return out, err
	case '[':
		out := []any{}
		for decoder.More() {
			value, err := decodeValue(decoder)
			if err != nil {
				return nil, err
			}
			out = append(out, value)
		}
		_, err := decoder.Token()
		return out, err
	}
	return nil, fmt.Errorf("unexpected %v", delim)
}

// encode writes a value as compact JSON, its objects' members in order and
// no character escaped that JSON.stringify leaves as it is.
func encode(value any) []byte {
	var out bytes.Buffer
	encodeTo(&out, value)
	return out.Bytes()
}

func encodeTo(out *bytes.Buffer, value any) {
	switch v := value.(type) {
	case nil:
		out.WriteString("null")
	case bool:
		if v {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
	case json.Number:
		out.WriteString(v.String())
	case string:
		out.Write(quote(v))
	case []any:
		out.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				out.WriteByte(',')
			}
			encodeTo(out, item)
		}
		out.WriteByte(']')
	case *object:
		if v == nil {
			out.WriteString("null")
			return
		}
		out.WriteByte('{')
		for i, key := range v.keys {
			if i > 0 {
				out.WriteByte(',')
			}
			out.Write(quote(key))
			out.WriteByte(':')
			encodeTo(out, v.members[key])
		}
		out.WriteByte('}')
	default:
		text, _ := json.Marshal(v)
		out.Write(text)
	}
}

func quote(text string) []byte {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(text)
	return bytes.TrimSuffix(out.Bytes(), []byte("\n"))
}

// show is a value as JSON, for a message.
func show(value any) string {
	return string(encode(value))
}

func member(value any, key string) any {
	if o, ok := value.(*object); ok {
		found, _ := o.get(key)
		return found
	}
	return nil
}

func text(value any) (string, bool) {
	s, ok := value.(string)
	return s, ok
}

func textOf(value any) string {
	s, _ := value.(string)
	return s
}

func list(value any) []any {
	items, _ := value.([]any)
	return items
}

// integer is a JSON number that is a whole number.
func integer(value any) (int64, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	f, err := number.Float64()
	if err != nil || f != math.Trunc(f) || math.Abs(f) > 1<<53 {
		return 0, false
	}
	return int64(f), true
}

func number(n int64) json.Number {
	return json.Number(fmt.Sprintf("%d", n))
}

// jsEqual compares as the replayers do: objects by member, regardless of
// order; arrays in order; numbers by value.
func jsEqual(a, b any) bool {
	switch x := a.(type) {
	case json.Number:
		y, ok := b.(json.Number)
		if !ok {
			return false
		}
		fx, errX := x.Float64()
		fy, errY := y.Float64()
		return errX == nil && errY == nil && fx == fy
	case *object:
		y, ok := b.(*object)
		if !ok || x.len() != y.len() {
			return false
		}
		for _, key := range x.keys {
			other, ok := y.get(key)
			if !ok || !jsEqual(x.members[key], other) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !jsEqual(x[i], y[i]) {
				return false
			}
		}
		return true
	default:
		return a == b
	}
}

// clone copies a value deeply.
func clone(value any) any {
	switch v := value.(type) {
	case *object:
		out := newObject()
		for _, key := range v.keys {
			out.set(key, clone(v.members[key]))
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = clone(item)
		}
		return out
	default:
		return value
	}
}

// resolveShared puts each shared value in its places.
func resolveShared(value any, shared *object) (any, error) {
	switch v := value.(type) {
	case *object:
		if v.len() == 1 {
			if key, ok := text(member(v, "$shared")); ok {
				found, ok := shared.get(key)
				if !ok {
					return nil, fmt.Errorf("no shared value %s", key)
				}
				return clone(found), nil
			}
		}
		for _, key := range v.keys {
			resolved, err := resolveShared(v.members[key], shared)
			if err != nil {
				return nil, err
			}
			v.members[key] = resolved
		}
		return v, nil
	case []any:
		for i, item := range v {
			resolved, err := resolveShared(item, shared)
			if err != nil {
				return nil, err
			}
			v[i] = resolved
		}
		return v, nil
	default:
		return value, nil
	}
}

// fromGo is a value the binding decoded (an engine error's details) as the
// replay holds values.
func fromGo(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	value, err := decode(raw)
	if err != nil {
		return strings.TrimSpace(string(raw))
	}
	return value
}
