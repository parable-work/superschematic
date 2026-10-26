package memorystore

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"

	"github.com/parable-work/superschematic/ir"
)

// Name joins a declaration's name and its parts' wire strings into a name
// without the store's prefix (`stock-changed:3f2a...`), refusing a
// declaration or a part that is no segment: empty, over 128 bytes, or
// holding `:`, `*`, `?`, `[`, `]`, `@`, `\` or a control character. A part
// is refused, never escaped. The generated definitions format each part
// and call it, so a caller never does.
func Name(declaration string, parts ...string) (string, error) {
	name, err := ir.MemoryStoreName(declaration, parts...)
	if err != nil {
		return "", fmt.Errorf("memorystore: %w", err)
	}
	return name, nil
}

// Prefix is a store's prefix: its name, then `@` and the member's name in
// a member of a parameterized environment. It refuses a store or a member
// that is no segment.
func Prefix(store, member string) (string, error) {
	if err := ir.CheckMemoryStoreSegment(store); err != nil {
		return "", fmt.Errorf("memorystore: the store's name: %w", err)
	}
	if member != "" {
		if err := ir.CheckMemoryStoreSegment(member); err != nil {
			return "", fmt.Errorf("memorystore: the member's name: %w", err)
		}
	}
	return ir.MemoryStorePrefix(store, member), nil
}

// StringPart is the wire string of a part whose format is string: a
// string, or a scalar whose wire form is one, written as its JSON string
// holds it. superscalar's Identity.UUID, say, is its compact base62 form,
// as the API's JSON carries it, so a name built from a value read off the
// wire in any language is the same name.
func StringPart[T any](v T) string {
	if rv := reflect.ValueOf(v); rv.Kind() == reflect.String {
		if _, ok := any(v).(json.Marshaler); !ok {
			return rv.String()
		}
	}
	if data, err := json.Marshal(v); err == nil {
		var s string
		if json.Unmarshal(data, &s) == nil {
			return s
		}
	}
	if s, ok := any(v).(fmt.Stringer); ok {
		return s.String()
	}
	return fmt.Sprint(v)
}

// IntegerPart is the wire string of a part whose format is integer: base
// 10.
func IntegerPart[T ~int | ~int8 | ~int16 | ~int32 | ~int64](v T) string {
	return strconv.FormatInt(int64(v), 10)
}

// BooleanPart is the wire string of a part whose format is boolean:
// `true` or `false`.
func BooleanPart[T ~bool](v T) string {
	return strconv.FormatBool(bool(v))
}

// EnumPart is the wire string of a part whose format is enum: its
// member's wire value.
func EnumPart[T ~string](v T) string {
	return string(v)
}
