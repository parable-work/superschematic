package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// CanonicalJSON is value as JSON with every object's members sorted and
// numbers and strings written as JavaScript's JSON.stringify writes them:
// one text for one value, whatever order a client wrote it in, and the
// text the TypeScript runtime's canonicalJson and the Rust runtime's
// canonical_json write for it, so a confirmation one runtime minted binds
// the same arguments in another (D93). Members sort by their UTF-16 code
// units, as JavaScript's sort does. value is what a JSON decoder gives
// (maps, slices, strings, bools, nil, float64 or json.Number), or anything
// encoding/json encodes, which is read back first.
func CanonicalJSON(value any) string {
	var buf bytes.Buffer
	writeCanonical(&buf, plainValue(value))
	return buf.String()
}

// plainValue is value as a decoder gives it: itself when it is one, else
// its JSON read back with its numbers as written.
func plainValue(value any) any {
	switch value.(type) {
	case nil, bool, string, float64, json.Number, map[string]any, []any:
		return value
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	out, err := decodeJSON(data)
	if err != nil {
		return nil
	}
	return out
}

func writeCanonical(buf *bytes.Buffer, value any) {
	switch v := value.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		buf.WriteString(strconv.FormatBool(v))
	case string:
		writeJSString(buf, v)
	case float64:
		buf.WriteString(JSNumber(v))
	case json.Number:
		buf.WriteString(numberText(v))
	case []any:
		buf.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeCanonical(buf, plainValue(item))
		}
		buf.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		slices.SortFunc(keys, compareUTF16)
		buf.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeJSString(buf, key)
			buf.WriteByte(':')
			writeCanonical(buf, plainValue(v[key]))
		}
		buf.WriteByte('}')
	default:
		writeCanonical(buf, plainValue(v))
	}
}

// compareUTF16 orders a and b by their UTF-16 code units, as JavaScript
// compares strings.
func compareUTF16(a, b string) int {
	return slices.Compare(utf16.Encode([]rune(a)), utf16.Encode([]rune(b)))
}

// numberText is a decoded number as JavaScript writes it: the double it
// reads as, in its shortest form.
func numberText(n json.Number) string {
	f, err := strconv.ParseFloat(n.String(), 64)
	if err != nil {
		return n.String()
	}
	return JSNumber(f)
}

// JSNumber is f as JavaScript's Number.prototype.toString writes it
// (ECMAScript's Number::toString): the shortest digits that read back as
// f, in fixed notation from 1e-7 up to 1e21 and in exponent notation
// outside it, and 0 for negative zero.
func JSNumber(f float64) string {
	switch {
	case math.IsNaN(f) || math.IsInf(f, 0):
		return "null"
	case f == 0:
		return "0"
	case f < 0:
		return "-" + JSNumber(-f)
	}
	mantissa, exponent, _ := strings.Cut(strconv.FormatFloat(f, 'e', -1, 64), "e")
	digits := strings.Replace(mantissa, ".", "", 1)
	e, _ := strconv.Atoi(exponent)
	k, n := len(digits), e+1
	switch {
	case k <= n && n <= 21:
		return digits + strings.Repeat("0", n-k)
	case 0 < n && n <= 21:
		return digits[:n] + "." + digits[n:]
	case -6 < n && n <= 0:
		return "0." + strings.Repeat("0", -n) + digits
	}
	sign := "+"
	if n-1 < 0 {
		sign = "-"
	}
	power := strconv.Itoa(abs(n - 1))
	if k == 1 {
		return digits + "e" + sign + power
	}
	return digits[:1] + "." + digits[1:] + "e" + sign + power
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// writeJSString writes s as JSON.stringify writes a string: a quote, a
// backslash and the control characters escaped, the seven with short forms
// by them, and every other character as it is.
func writeJSString(buf *bytes.Buffer, s string) {
	buf.WriteByte('"')
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == '"':
			buf.WriteString(`\"`)
		case r == '\\':
			buf.WriteString(`\\`)
		case r == '\b':
			buf.WriteString(`\b`)
		case r == '\f':
			buf.WriteString(`\f`)
		case r == '\n':
			buf.WriteString(`\n`)
		case r == '\r':
			buf.WriteString(`\r`)
		case r == '\t':
			buf.WriteString(`\t`)
		case r < 0x20:
			fmt.Fprintf(buf, `\u%04x`, r)
		default:
			buf.WriteString(s[i : i+size])
		}
		i += size
	}
	buf.WriteByte('"')
}

// decodeJSON reads data as a decoder gives it, numbers as json.Number: one
// value and nothing after it.
func decodeJSON(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var out any
	if err := decoder.Decode(&out); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("a JSON value is followed by more")
	}
	return out, nil
}

// marshal is value as JSON without HTML escapes, with no newline after it.
func marshal(value any) []byte {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return []byte("null")
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

// compact is data, JSON, without insignificant space; data itself when it
// is not JSON.
func compact(data []byte) []byte {
	var buf bytes.Buffer
	if err := json.Compact(&buf, data); err != nil {
		return data
	}
	return buf.Bytes()
}

// PlainArgs are args with each number read as a float64, as
// encoding/json decodes one into an any: what a tool that reads its
// arguments as a plain decoder gives them takes.
func PlainArgs(args map[string]any) map[string]any {
	out := make(map[string]any, len(args))
	for key, value := range args {
		out[key] = plainNumbers(value)
	}
	return out
}

func plainNumbers(value any) any {
	switch v := value.(type) {
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return v.String()
		}
		return f
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = plainNumbers(item)
		}
		return out
	case map[string]any:
		return PlainArgs(v)
	}
	return value
}
