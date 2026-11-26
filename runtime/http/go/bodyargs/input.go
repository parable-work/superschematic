package bodyargs

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/parable-work/superschematic/runtime/schema/go/validate"
)

// The details every generated server, Go, TypeScript and Rust, answers a
// refused input body with.
const (
	// BodyRequired is the detail of an input without a body.
	BodyRequired = "Request body is required"
	// BodyNotJSON is the detail of a body that is not JSON.
	BodyNotJSON = "Request body is not valid JSON"
	// BodyMismatch is the detail of a body the input type refuses; the
	// refusal's Reason says why.
	BodyMismatch = "Request body does not match the declared input"
)

// InputRefusal is why a route refused the body of an operation's input
// type. Detail is the problem's detail. A body that does not match the
// input type (BodyMismatch) also has a Reason, the problem's
// details.reason ("expected an object", "unknown fields: a, b",
// "validation failed", "does not match the declared type"), and Errors,
// the field errors of an undeclared key or a broken rule, the problem's
// top-level errors member.
//
// Type is the input type that StrictType named, for the refusal of a key
// it does not declare. It is empty otherwise.
type InputRefusal struct {
	Detail string
	Reason string
	Errors validate.ValidationErrors
	Type   string
}

// ReadOption changes how ReadInput and ReadInputDroppingUnknown read an input
// body, and how DecodeRefusal words a decoder's refusal. With no option
// they make the checks every generated server makes. A router whose
// clients know another server's answers passes the options that give
// those answers.
type ReadOption func(*readOptions)

type readOptions struct {
	keepUndeclared bool
	firstValue     bool
	emptyAsObject  bool
	nullAsObject   bool
	strictType     string
}

// KeepUndeclaredKeys makes ReadInput pass a key the type does not declare
// on to the type's decoder, as the body holds it, rather than refuse it.
// The decoder then reads or ignores it as encoding/json does: a key in
// another case reads into the field it matches.
func KeepUndeclaredKeys() ReadOption { return func(o *readOptions) { o.keepUndeclared = true } }

// FirstValue reads the first JSON value of the body and leaves the rest of
// the body unread, as a json.Decoder reads it: trailing data and a second
// value are not refused, and a read error after the first value is not
// seen.
func FirstValue() ReadOption { return func(o *readOptions) { o.firstValue = true } }

// EmptyBodyAsObject reads an empty body, or one of white space alone, as
// the empty object {}, for an input the operation does not require.
func EmptyBodyAsObject() ReadOption { return func(o *readOptions) { o.emptyAsObject = true } }

// NullAsEmptyObject reads a body that is the JSON null as the empty object
// {}, as encoding/json decodes null into a struct: it leaves every field
// unset, for the field rules to find.
func NullAsEmptyObject() ReadOption { return func(o *readOptions) { o.nullAsObject = true } }

// StrictType names a closed (@strictJSON) input type, name, whose own
// decoder owns its refusals. ReadInput records name as the Type of the
// refusal of a key the type does not declare, and DecodeRefusal leaves the
// decoder's refusal of a string an enum lacks as the decoder's: the body
// does not match the declared type, with no "enum" field error.
func StrictType(name string) ReadOption { return func(o *readOptions) { o.strictType = name } }

func readOptionsOf(opts []ReadOption) readOptions {
	var o readOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	return o
}

// readJSON reads a body that holds one JSON value, with the refusals of
// ReadInput's first two checks, as the options say.
func readJSON(r io.Reader, o readOptions) (json.RawMessage, *InputRefusal) {
	raw, refusal := readJSONValue(r, o)
	if refusal == nil && o.nullAsObject && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return json.RawMessage("{}"), nil
	}
	return raw, refusal
}

// readJSONValue reads the body's JSON value as readJSON does, the null
// left as it is.
func readJSONValue(r io.Reader, o readOptions) (json.RawMessage, *InputRefusal) {
	if o.firstValue {
		var raw json.RawMessage
		if err := json.NewDecoder(r).Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) {
				if o.emptyAsObject {
					return json.RawMessage("{}"), nil
				}
				return nil, &InputRefusal{Detail: BodyRequired}
			}
			return nil, &InputRefusal{Detail: BodyNotJSON}
		}
		return raw, nil
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, &InputRefusal{Detail: BodyNotJSON}
	}
	if len(bytes.TrimSpace(data)) == 0 {
		if o.emptyAsObject {
			return json.RawMessage("{}"), nil
		}
		return nil, &InputRefusal{Detail: BodyRequired}
	}
	var raw json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, &InputRefusal{Detail: BodyNotJSON}
	}
	return raw, nil
}

// Mismatch is the refusal of a body the input type refuses for reason,
// with errors when the reason has field errors.
func Mismatch(reason string, errors validate.ValidationErrors) *InputRefusal {
	return &InputRefusal{Detail: BodyMismatch, Reason: reason, Errors: errors}
}

// ReadInput reads the body of an operation's input type and makes the
// checks every generated server makes before the type decodes it: a body
// (BodyRequired), JSON (BodyNotJSON), an object (Mismatch, "expected an
// object"), and only the top-level keys the type declares, fields
// (Mismatch, "unknown fields: a, b", with an "unknown" error at each key,
// in the order the body first holds them). The type's own decoding and
// Validate follow. The options change the reading (ReadOption).
func ReadInput(r io.Reader, fields []string, opts ...ReadOption) (json.RawMessage, *InputRefusal) {
	o := readOptionsOf(opts)
	raw, refusal := readJSON(r, o)
	if refusal != nil {
		return nil, refusal
	}
	unknown, isObject := unknownKeys(raw, fields)
	if !isObject {
		return nil, Mismatch("expected an object", nil)
	}
	if len(unknown) > 0 && !o.keepUndeclared {
		errors := validate.NewValidationErrors()
		for _, key := range unknown {
			errors.SetFieldErrors(key, []validate.ValidationError{{Validator: "unknown", Message: "unknown field"}})
		}
		refusal := Mismatch("unknown fields: "+strings.Join(unknown, ", "), errors)
		refusal.Type = o.strictType
		return nil, refusal
	}
	return raw, nil
}

// ReadInputDroppingUnknown reads the body of an input that accepts the
// keys its type does not declare (D87): a type that is
// @allowUnknownFields, or a @webhook's input. It makes ReadInput's checks
// but the last, and returns the object with only the top-level keys fields
// lists, each where the body holds it. A key is declared only as fields
// spells it, so a key that differs in case is dropped too, where
// encoding/json would read it into the field: every server reads the same
// keys. FirstValue and EmptyBodyAsObject change its reading as they change
// ReadInput's.
func ReadInputDroppingUnknown(r io.Reader, fields []string, opts ...ReadOption) (json.RawMessage, *InputRefusal) {
	raw, refusal := readJSON(r, readOptionsOf(opts))
	if refusal != nil {
		return nil, refusal
	}
	declared, isObject := declaredMembers(raw, fields)
	if !isObject {
		return nil, Mismatch("expected an object", nil)
	}
	return declared, nil
}

// declaredMembers is the JSON object raw with only the members whose keys
// fields lists, in the order raw holds them. isObject is false for any
// other JSON value.
func declaredMembers(raw json.RawMessage, fields []string) (declared json.RawMessage, isObject bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil, false
	}
	known := make(map[string]bool, len(fields))
	for _, field := range fields {
		known[field] = true
	}
	var out bytes.Buffer
	out.WriteByte('{')
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		key, _ := token.(string)
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			break
		}
		if !known[key] {
			continue
		}
		if out.Len() > 1 {
			out.WriteByte(',')
		}
		name, _ := json.Marshal(key)
		out.Write(name)
		out.WriteByte(':')
		out.Write(value)
	}
	out.WriteByte('}')
	return out.Bytes(), true
}

// unknownKeys lists the top-level keys of the JSON object raw that fields
// does not, once each, in the order raw first holds them. isObject is
// false for any other JSON value.
func unknownKeys(raw json.RawMessage, fields []string) (unknown []string, isObject bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil, false
	}
	known := make(map[string]bool, len(fields))
	for _, field := range fields {
		known[field] = true
	}
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return unknown, true
		}
		key, _ := token.(string)
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return unknown, true
		}
		if !known[key] && !seen[key] {
			seen[key] = true
			unknown = append(unknown, key)
		}
	}
	return unknown, true
}
