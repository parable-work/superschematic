package bodyargs

import (
	"bytes"
	"encoding/json"
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
type InputRefusal struct {
	Detail string
	Reason string
	Errors validate.ValidationErrors
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
// Validate follow.
func ReadInput(r io.Reader, fields []string) (json.RawMessage, *InputRefusal) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, &InputRefusal{Detail: BodyNotJSON}
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, &InputRefusal{Detail: BodyRequired}
	}
	var raw json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, &InputRefusal{Detail: BodyNotJSON}
	}
	unknown, isObject := unknownKeys(raw, fields)
	if !isObject {
		return nil, Mismatch("expected an object", nil)
	}
	if len(unknown) > 0 {
		errors := validate.NewValidationErrors()
		for _, key := range unknown {
			errors.SetFieldErrors(key, []validate.ValidationError{{Validator: "unknown", Message: "unknown field"}})
		}
		return nil, Mismatch("unknown fields: "+strings.Join(unknown, ", "), errors)
	}
	return raw, nil
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
