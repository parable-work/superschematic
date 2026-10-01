// Package jsonkeys is the raw-body check the raw-body-check-api route tests
// register for Generic.JSON (TestRawBodyCheckRoutesRefuseBeforeDecoding
// writes it into the generated API module). Decoding a JSON object keeps
// the last of two equal keys, so only the raw body shows that a key was
// repeated.
package jsonkeys

import (
	"bytes"
	"encoding/json"

	types "example.com/schemas/types/go/raw-body-check-api"
)

// DuplicateKeyErrors reports each key the object of a named top-level field
// of body repeats, at <field>.<key>. A body that is not a JSON object, a
// field that is absent and a value that is not an object have nothing to
// report; the decode that follows judges them.
func DuplicateKeyErrors(body []byte, fields ...string) types.ValidationErrors {
	errs := types.NewValidationErrors()
	var values map[string]json.RawMessage
	if err := json.Unmarshal(body, &values); err != nil {
		return errs
	}
	for _, field := range fields {
		raw, ok := values[field]
		if !ok {
			continue
		}
		repeated := repeatedKeys(raw)
		if len(repeated) == 0 {
			continue
		}
		nested := types.NewValidationErrors()
		for _, key := range repeated {
			nested.AddFieldError(key, "duplicateKey", "the key appears more than once")
		}
		errs.AddNestedError(field, nested)
	}
	return errs
}

// repeatedKeys returns each key the JSON object raw repeats, once, in the
// order of its first repetition; nil when raw is not an object.
func repeatedKeys(raw json.RawMessage) []string {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil
	}
	seen := map[string]int{}
	var repeated []string
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return repeated
		}
		key, _ := token.(string)
		seen[key]++
		if seen[key] == 2 {
			repeated = append(repeated, key)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return repeated
		}
	}
	return repeated
}
