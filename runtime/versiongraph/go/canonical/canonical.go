// Package canonical turns rows as Postgres renders them into canonical rows
// (D19), the form the version-graph core compares and hashes.
//
// A canonical row is a JSON object keyed by column name whose values are the
// schema runtime's JSON for each field's type. A graph descriptor (version 3)
// gives every column a value class, and each class has one rule that turns
// what Postgres returns, in to_jsonb of a live row or in a history image,
// into its canonical JSON. runtime/versiongraph/README.md ("Canonical rows")
// is the contract, and runtime/versiongraph/testdata/canonical holds its
// vectors.
//
// The package is plain Go with no cgo, so a storage adapter can use it
// without linking the core.
package canonical

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// The element classes. A column's class is one of them, or a list of one
// ("uuid[]") or a list of lists ("uuid[][]").
const (
	String   = "string"
	Integer  = "integer"
	Number   = "number"
	Boolean  = "boolean"
	UUID     = "uuid"
	DateTime = "dateTime"
	Date     = "date"
	Time     = "time"
	Duration = "duration"
	Enum     = "enum"
	JSON     = "json"
)

// Error is a value its class's rule refuses, or a row that does not fit its
// columns.
type Error struct {
	// Column is the row's column, empty for a single value.
	Column string
	// Class is the value class the rule belongs to.
	Class string
	// Message says what is wrong with the value.
	Message string
}

func (e *Error) Error() string {
	if e.Column != "" {
		return fmt.Sprintf("canonical: column %s (%s): %s", e.Column, e.Class, e.Message)
	}
	return fmt.Sprintf("canonical: %s: %s", e.Class, e.Message)
}

// ErrUnknownClass is returned for a class that is not an element class, a
// list of one or a list of lists of one.
var ErrUnknownClass = errors.New("canonical: unknown value class")

// rule turns one decoded element into its canonical JSON.
type rule func(value any) (string, error)

var rules = map[string]rule{
	String:   stringRule,
	Integer:  integerRule,
	Number:   numberRule,
	Boolean:  booleanRule,
	UUID:     uuidRule,
	DateTime: dateTimeRule,
	Date:     dateRule,
	Time:     timeRule,
	Duration: durationRule,
	Enum:     stringRule,
	JSON:     jsonRule,
}

// parseClass splits a class into its element rule and list depth.
func parseClass(class string) (rule, int, error) {
	element, depth := class, 0
	switch {
	case strings.HasSuffix(class, "[][]"):
		element, depth = strings.TrimSuffix(class, "[][]"), 2
	case strings.HasSuffix(class, "[]"):
		element, depth = strings.TrimSuffix(class, "[]"), 1
	}
	r, ok := rules[element]
	if !ok {
		return nil, 0, fmt.Errorf("%w %q", ErrUnknownClass, class)
	}
	return r, depth, nil
}

// Postgres returns the canonical JSON of one value of class, given as
// Postgres renders it inside to_jsonb. JSON null is null in every class.
func Postgres(class string, value json.RawMessage) (json.RawMessage, error) {
	r, depth, err := parseClass(class)
	if err != nil {
		return nil, err
	}
	decoded, err := decode(value)
	if err != nil {
		return nil, &Error{Class: class, Message: err.Error()}
	}
	out, err := apply(r, depth, decoded)
	if err != nil {
		return nil, &Error{Class: class, Message: err.Error()}
	}
	return json.RawMessage(out), nil
}

// PostgresRow returns the canonical row of a row as to_jsonb renders it,
// whose columns have the classes in columns: a JSON object with its members
// sorted by column name. A column the row has and columns lacks is refused;
// one columns has and the row lacks stays absent.
func PostgresRow(columns map[string]string, row json.RawMessage) (json.RawMessage, error) {
	decoded, err := decode(row)
	if err != nil {
		return nil, fmt.Errorf("canonical: row: %w", err)
	}
	members, ok := decoded.(map[string]any)
	if !ok {
		return nil, errors.New("canonical: a row is a JSON object")
	}
	names := make([]string, 0, len(members))
	for name := range members {
		names = append(names, name)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, name := range names {
		class, ok := columns[name]
		if !ok {
			return nil, &Error{Column: name, Message: "the row has a column its descriptor does not declare"}
		}
		r, depth, err := parseClass(class)
		if err != nil {
			return nil, fmt.Errorf("column %s: %w", name, err)
		}
		out, err := apply(r, depth, members[name])
		if err != nil {
			return nil, &Error{Column: name, Class: class, Message: err.Error()}
		}
		if i > 0 {
			buf.WriteByte(',')
		}
		writeString(&buf, name)
		buf.WriteByte(':')
		buf.WriteString(out)
	}
	buf.WriteByte('}')
	return json.RawMessage(buf.Bytes()), nil
}

// Row returns the canonical row of a row whose values are the schema
// runtime's JSON for each column's field type, as a typed value
// serializes: what a typed facade hands the engine. The rules read the
// schema runtime's forms as they read Postgres's (a base62 UUID, a
// date-time with any offset, an "HH:MM" time, a duration string), so it is
// PostgresRow by another name.
func Row(columns map[string]string, row json.RawMessage) (json.RawMessage, error) {
	return PostgresRow(columns, row)
}

// apply runs an element rule over a value, a list or a list of lists. A
// null value is null; a null element is refused, since a list element is
// never null (D12).
func apply(r rule, depth int, value any) (string, error) {
	if value == nil {
		return "null", nil
	}
	if depth == 0 {
		return r(value)
	}
	list, ok := value.([]any)
	if !ok {
		return "", fmt.Errorf("%s is not a list", describe(value))
	}
	var buf strings.Builder
	buf.WriteByte('[')
	for i, element := range list {
		if element == nil {
			return "", fmt.Errorf("element %d is null, and a list element is never null", i)
		}
		out, err := apply(r, depth-1, element)
		if err != nil {
			return "", fmt.Errorf("element %d: %w", i, err)
		}
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.WriteString(out)
	}
	buf.WriteByte(']')
	return buf.String(), nil
}

// decode reads one JSON value, keeping each number's text.
func decode(value json.RawMessage) (any, error) {
	d := json.NewDecoder(bytes.NewReader(value))
	d.UseNumber()
	var out any
	if err := d.Decode(&out); err != nil {
		return nil, err
	}
	if _, err := d.Token(); err == nil {
		return nil, errors.New("more than one JSON value")
	}
	return out, nil
}

// describe names a decoded value for an error message.
func describe(value any) string {
	switch v := value.(type) {
	case string:
		return fmt.Sprintf("%q", v)
	case json.Number:
		return "the number " + v.String()
	case bool:
		return fmt.Sprintf("%t", v)
	case []any:
		return "a list"
	case map[string]any:
		return "an object"
	}
	return fmt.Sprintf("%v", value)
}

func stringRule(value any) (string, error) {
	s, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s is not a string", describe(value))
	}
	var buf bytes.Buffer
	writeString(&buf, s)
	return buf.String(), nil
}

func booleanRule(value any) (string, error) {
	b, ok := value.(bool)
	if !ok {
		return "", fmt.Errorf("%s is not a boolean", describe(value))
	}
	if b {
		return "true", nil
	}
	return "false", nil
}

// jsonRule writes any JSON value canonically: object members sorted by key,
// no whitespace, strings escaped as writeString does, and every number in
// the number class's form.
func jsonRule(value any) (string, error) {
	var buf bytes.Buffer
	if err := writeJSON(&buf, value); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func writeJSON(buf *bytes.Buffer, value any) error {
	switch v := value.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if v {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case string:
		writeString(buf, v)
	case json.Number:
		out, err := numberRule(v)
		if err != nil {
			return err
		}
		buf.WriteString(out)
	case []any:
		buf.WriteByte('[')
		for i, element := range v {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeJSON(buf, element); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeString(buf, key)
			buf.WriteByte(':')
			if err := writeJSON(buf, v[key]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		return fmt.Errorf("%v is not a JSON value", value)
	}
	return nil
}

// writeString writes a JSON string as the core's canonical JSON does: `"`
// and `\` escaped, \b, \f, \n, \r and \t by name, every other control
// character as \u00xx in lowercase hex, and everything else as it is.
func writeString(buf *bytes.Buffer, s string) {
	const hex = "0123456789abcdef"
	buf.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			buf.WriteString(`\"`)
		case '\\':
			buf.WriteString(`\\`)
		case '\b':
			buf.WriteString(`\b`)
		case '\f':
			buf.WriteString(`\f`)
		case '\n':
			buf.WriteString(`\n`)
		case '\r':
			buf.WriteString(`\r`)
		case '\t':
			buf.WriteString(`\t`)
		default:
			if c < 0x20 {
				buf.WriteString(`\u00`)
				buf.WriteByte(hex[c>>4])
				buf.WriteByte(hex[c&0xf])
			} else {
				buf.WriteByte(c)
			}
		}
	}
	buf.WriteByte('"')
}
