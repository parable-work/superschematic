package rules

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// The conversions between the core's JSON and a column's value, by the
// layout's column: a timestamp is epoch milliseconds in the core, a UUID
// its base62 wire form (D79), and every other value its JSON as the
// driver binds and reads it. TypeScript's adapter converts alike
// (runtime/http/typescript/src/rules.ts, toColumn and fromColumn).

// uuidTypes are the IR types whose wire form is a UUID's base62 text and
// whose column holds its lowercase hyphenated text (D79).
var uuidTypes = map[string]bool{"Identity.UUID": true, "Identity.UserID": true}

// jsonTypes are the IR types a column holds as JSON text on SQLite, which
// a read gives back as the JSON it holds.
var jsonTypes = map[string]bool{"Generic.JSON": true}

const base62Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// maxUUID is the largest 128-bit value.
var maxUUID = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 128), big.NewInt(1))

// uuidValue is a UUID's 128-bit value from its hyphenated form (36
// characters) or its base62 form (1 to 22, case-sensitive), as
// superscalar's ParseUUID reads it; false for any other text.
func uuidValue(text string) (*big.Int, bool) {
	if len(text) == 36 {
		if text[8] != '-' || text[13] != '-' || text[18] != '-' || text[23] != '-' {
			return nil, false
		}
		digits := text[0:8] + text[9:13] + text[14:18] + text[19:23] + text[24:36]
		if _, err := hex.DecodeString(digits); err != nil {
			return nil, false
		}
		n, ok := new(big.Int).SetString(digits, 16)
		return n, ok
	}
	if len(text) < 1 || len(text) > 22 {
		return nil, false
	}
	n := new(big.Int)
	base := big.NewInt(62)
	for i := 0; i < len(text); i++ {
		digit := strings.IndexByte(base62Alphabet, text[i])
		if digit < 0 {
			return nil, false
		}
		n.Mul(n, base)
		n.Add(n, big.NewInt(int64(digit)))
	}
	if n.Cmp(maxUUID) > 0 {
		return nil, false
	}
	return n, true
}

// hyphenated is a UUID's value as its lowercase hyphenated text.
func hyphenated(n *big.Int) string {
	var raw [16]byte
	n.FillBytes(raw[:])
	return hyphenatedBytes(raw)
}

// hyphenatedBytes is sixteen bytes as a UUID's lowercase hyphenated text.
func hyphenatedBytes(raw [16]byte) string {
	h := hex.EncodeToString(raw[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// base62 is a UUID's value as its base62 text, the scalar's canonical
// form: no padding, "0" for zero.
func base62(n *big.Int) string {
	if n.Sign() == 0 {
		return "0"
	}
	rest := new(big.Int).Set(n)
	base := big.NewInt(62)
	digit := new(big.Int)
	var out []byte
	for rest.Sign() > 0 {
		rest.DivMod(rest, base, digit)
		out = append(out, base62Alphabet[digit.Int64()])
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

// uuidToColumn is a UUID in either form as its column holds it,
// hyphenated.
func uuidToColumn(text string) (string, error) {
	n, ok := uuidValue(text)
	if !ok {
		return "", fmt.Errorf("rules: %q is no UUID, hyphenated or base62", text)
	}
	return hyphenated(n), nil
}

// uuidToWire is a UUID in either form as its wire form, base62.
func uuidToWire(text string) (string, error) {
	n, ok := uuidValue(text)
	if !ok {
		return "", fmt.Errorf("rules: %q is no UUID, hyphenated or base62", text)
	}
	return base62(n), nil
}

// randomUUID is a random version 4 UUID's sixteen bytes.
func randomUUID() [16]byte {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic(fmt.Sprintf("rules: read random bytes: %v", err))
	}
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	return raw
}

// isoFormat is the text Date.prototype.toISOString writes: UTC, always
// three fraction digits.
const isoFormat = "2006-01-02T15:04:05.000Z"

// isoText is an instant as Date.prototype.toISOString writes it.
func isoText(t time.Time) string {
	return t.UTC().Format(isoFormat)
}

// timestampLayouts are the texts a timestamp column's value is read from:
// RFC 3339 with a T or a space, any fraction, Z or an offset, and SQLite's
// own text with no zone, which is UTC.
var timestampLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04:05.999999999",
}

// parseTimestamp reads a timestamp's text.
func parseTimestamp(text string) (time.Time, bool) {
	text = strings.TrimSpace(text)
	for _, layout := range timestampLayouts {
		if t, err := time.Parse(layout, text); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// TimeOfMillis reads an owned or own date-time as the core holds it: nil
// for absent or null, else the epoch milliseconds' instant in UTC.
func TimeOfMillis(raw json.RawMessage) (*time.Time, error) {
	if isNull(raw) {
		return nil, nil
	}
	millis, ok := millisOf(raw)
	if !ok {
		return nil, fmt.Errorf("rules: %s is no epoch milliseconds", raw)
	}
	t := time.UnixMilli(millis).UTC()
	return &t, nil
}

// isNull reports whether raw is absent or JSON null.
func isNull(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || string(trimmed) == "null"
}

// millisOf reads epoch milliseconds from a JSON number, or from a string
// that holds one, as JavaScript's Date takes either; a fraction is
// dropped, as a Date drops it.
func millisOf(raw json.RawMessage) (int64, bool) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return 0, false
	}
	var text string
	switch v := value.(type) {
	case json.Number:
		text = v.String()
	case string:
		text = strings.TrimSpace(v)
	default:
		return 0, false
	}
	if n, err := strconv.ParseInt(text, 10, 64); err == nil {
		return n, true
	}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || math.Abs(f) > 8.64e15 {
		return 0, false
	}
	return int64(f), true
}

// toColumn is a value of the core's as its column binds it in a dialect:
// nil for null; a timestamp's epoch milliseconds as a time.Time on
// Postgres and its toISOString text on SQLite; a UUID's hyphenated text;
// any value of a JSON column, and an object or array of any column, as its
// JSON text (which Postgres casts to a JSON column, and which a read of a
// JSON column gives back as the value, a string as a string); a boolean as
// 1 or 0 on SQLite; a number as an int64 when it is an integer that fits,
// else a float64; a string as itself.
func toColumn(raw json.RawMessage, column Column, table *Table, dialect Dialect) (any, error) {
	if isNull(raw) {
		return nil, nil
	}
	if column.Convert == ConvertEpochMillis {
		millis, ok := millisOf(raw)
		if !ok {
			return nil, Internal("%s.%s: %s is no epoch milliseconds", table.Type, column.Field, compactText(raw))
		}
		t := time.UnixMilli(millis).UTC()
		if dialect == DialectPostgres {
			return t, nil
		}
		return isoText(t), nil
	}
	trimmed := bytes.TrimSpace(raw)
	if jsonTypes[column.Type] {
		if !json.Valid(trimmed) {
			return nil, Internal("%s.%s: %s is no JSON value", table.Type, column.Field, string(trimmed))
		}
		return compactText(trimmed), nil
	}
	switch trimmed[0] {
	case '"':
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return nil, Internal("%s.%s: %s is no JSON string", table.Type, column.Field, compactText(raw))
		}
		if uuidTypes[column.Type] {
			return uuidToColumn(text)
		}
		return text, nil
	case '{', '[':
		return compactText(trimmed), nil
	case 't', 'f':
		value := string(trimmed) == "true"
		if dialect == DialectSQLite {
			if value {
				return int64(1), nil
			}
			return int64(0), nil
		}
		return value, nil
	default:
		text := string(trimmed)
		if n, err := strconv.ParseInt(text, 10, 64); err == nil {
			return n, nil
		}
		f, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return nil, Internal("%s.%s: %s is no JSON value", table.Type, column.Field, text)
		}
		return f, nil
	}
}

// compactText is raw's JSON text with no insignificant space.
func compactText(raw json.RawMessage) string {
	var out bytes.Buffer
	if err := json.Compact(&out, raw); err != nil {
		return string(raw)
	}
	return out.String()
}

// fromColumn is a column's value, as the driver scanned it, as the core
// holds it: absent (false) for NULL; a timestamp as epoch milliseconds,
// from a time.Time or from RFC 3339 text; a UUID as its base62 wire form;
// a boolean column's integer as a boolean; any other time.Time as its
// toISOString text; a JSON column's text as the JSON it holds; a number of
// any width as a JSON number of its text; a string or bytes as a JSON
// string.
func fromColumn(value any, column Column, table *Table) (json.RawMessage, bool, error) {
	if value == nil {
		return nil, false, nil
	}
	if column.Convert == ConvertEpochMillis {
		millis, ok := columnMillis(value)
		if !ok {
			text, _ := json.Marshal(textOf(value))
			return nil, false, Internal("%s.%s: %s is no timestamp", table.Type, column.Field, text)
		}
		return json.RawMessage(strconv.FormatInt(millis, 10)), true, nil
	}
	if uuidTypes[column.Type] {
		var wire string
		switch v := value.(type) {
		case [16]byte:
			wire = base62(new(big.Int).SetBytes(v[:]))
		case string:
			converted, err := uuidToWire(v)
			if err != nil {
				return nil, false, err
			}
			wire = converted
		case []byte:
			converted, err := uuidToWire(string(v))
			if err != nil {
				return nil, false, err
			}
			wire = converted
		default:
			return nil, false, fmt.Errorf("rules: %s.%s: a %T is no UUID", table.Type, column.Field, value)
		}
		return marshalled(wire)
	}
	if column.Type == "boolean" {
		if n, ok := integerOf(value); ok {
			return marshalled(n != 0)
		}
	}
	switch v := value.(type) {
	case time.Time:
		return marshalled(isoText(v))
	case bool:
		return marshalled(v)
	case string:
		if jsonTypes[column.Type] && json.Valid([]byte(v)) {
			return json.RawMessage(compactText(json.RawMessage(v))), true, nil
		}
		return marshalled(v)
	case []byte:
		if jsonTypes[column.Type] && json.Valid(v) {
			return json.RawMessage(compactText(v)), true, nil
		}
		return marshalled(string(v))
	case json.RawMessage:
		return append(json.RawMessage(nil), v...), true, nil
	case json.Number:
		return json.RawMessage(v.String()), true, nil
	case map[string]any, []any:
		return marshalled(v)
	case json.Marshaler:
		// A driver's own number (pgtype.Numeric), which writes its value's
		// text.
		out, err := v.MarshalJSON()
		if err != nil {
			return nil, false, fmt.Errorf("rules: %s.%s: %w", table.Type, column.Field, err)
		}
		return out, true, nil
	}
	if n, ok := integerOf(value); ok {
		return json.RawMessage(strconv.FormatInt(n, 10)), true, nil
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return json.RawMessage(strconv.FormatUint(rv.Uint(), 10)), true, nil
	case reflect.Float32, reflect.Float64:
		return marshalled(rv.Float())
	}
	return marshalled(value)
}

// marshalled is a Go value's JSON, present.
func marshalled(value any) (json.RawMessage, bool, error) {
	out, err := json.Marshal(value)
	if err != nil {
		return nil, false, fmt.Errorf("rules: encode a column's value: %w", err)
	}
	return out, true, nil
}

// integerOf is a signed integer of any width as an int64.
func integerOf(value any) (int64, bool) {
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int(), true
	}
	return 0, false
}

// columnMillis is a timestamp column's value as epoch milliseconds.
func columnMillis(value any) (int64, bool) {
	switch v := value.(type) {
	case time.Time:
		return v.UnixMilli(), true
	case string:
		t, ok := parseTimestamp(v)
		return t.UnixMilli(), ok
	case []byte:
		t, ok := parseTimestamp(string(v))
		return t.UnixMilli(), ok
	case float64:
		return int64(v), !math.IsNaN(v) && !math.IsInf(v, 0)
	}
	return integerOf(value)
}

// textOf is a value as a message names it.
func textOf(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	default:
		return fmt.Sprint(v)
	}
}

// versionOf is a version column's value as an int64.
func versionOf(value any) (int64, error) {
	if n, ok := integerOf(value); ok {
		return n, nil
	}
	switch v := value.(type) {
	case float64:
		return int64(v), nil
	case string:
		return strconv.ParseInt(v, 10, 64)
	case []byte:
		return strconv.ParseInt(string(v), 10, 64)
	case json.Number:
		return v.Int64()
	}
	return 0, fmt.Errorf("rules: a version of %T is no integer", value)
}

// keyValue is a row key as its column binds it: a UUID's hyphenated text,
// any other key its text.
func keyValue(table *Table, key string) (any, error) {
	if uuidTypes[table.Key.Type] {
		return uuidToColumn(key)
	}
	return key, nil
}
