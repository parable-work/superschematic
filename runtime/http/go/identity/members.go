package identity

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// checkMemberNames refuses a JSON object member that names none of t's
// fields exactly, in the objects t's struct fields describe too.
// encoding/json matches a member to a field without regard to case, so
// {"SessionTtlSeconds": 5} would set sessionTtlSeconds, and
// DisallowUnknownFields does not refuse it. The TypeScript and Rust
// runtimes match names exactly, and the identity config, descriptor and
// request bodies are one contract in all three, so Go refuses what they
// refuse. A value that is not an object is left to the decoder.
func checkMemberNames(data []byte, t reflect.Type) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
	case reflect.Slice, reflect.Array:
		var items []json.RawMessage
		if json.Unmarshal(data, &items) != nil {
			return nil
		}
		for _, item := range items {
			if err := checkMemberNames(item, t.Elem()); err != nil {
				return err
			}
		}
		return nil
	default:
		return nil
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil
	}
	var members map[string]json.RawMessage
	if json.Unmarshal(trimmed, &members) != nil {
		return nil
	}
	fields := jsonFields(t)
	for name, value := range members {
		field, ok := fields[name]
		if !ok {
			return fmt.Errorf("json: unknown field %q", name)
		}
		if err := checkMemberNames(value, field); err != nil {
			return err
		}
	}
	return nil
}

// jsonFields maps each JSON member name t's fields take, exactly as their
// tags spell it, to the field's type. Embedded structs contribute their
// fields, as encoding/json flattens them.
func jsonFields(t reflect.Type) map[string]reflect.Type {
	fields := make(map[string]reflect.Type, t.NumField())
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if f.Anonymous && name == "" {
			inner := f.Type
			if inner.Kind() == reflect.Pointer {
				inner = inner.Elem()
			}
			if inner.Kind() == reflect.Struct {
				for k, v := range jsonFields(inner) {
					fields[k] = v
				}
				continue
			}
		}
		if name == "" {
			name = f.Name
		}
		fields[name] = f.Type
	}
	return fields
}
