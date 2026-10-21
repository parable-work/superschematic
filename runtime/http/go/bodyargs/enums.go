package bodyargs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/parable-work/superschematic/runtime/schema/go/validate"
)

// A generated Go enum's decoder refuses a string the enum lacks (D31,
// amended), where the TypeScript and Rust servers validate the body before
// they decode it and answer "validation failed" with "enum" at the value's
// path, beside every other rule the body breaks. So that a Go route answers
// the same, a body whose decoder refused it is read again with each such
// value replaced by one the enum has: the replaced values are "enum" at
// their paths, and the type's Validate of the rest finds the other rules.

// enumError is the error at the path of a string its enum lacks, as the
// generated enums' Validate words it.
var enumError = validate.ValidationError{Validator: "enum", Message: "invalid enum value"}

// DecodeRefusal is the refusal of an input body, raw, that the decoder of
// target, a pointer to the input type, refused. When raw decodes once each
// string an enum lacks is replaced by one of the enum's values, it is
// "validation failed", with "enum" at each such string's path and the
// field errors the input type's Validate finds in the rest; any other body
// is "does not match the declared type", as a value of the wrong JSON type
// is.
func DecodeRefusal(raw json.RawMessage, target any) *InputRefusal {
	t := reflect.TypeOf(target)
	if t == nil || t.Kind() != reflect.Pointer {
		return Mismatch("does not match the declared type", nil)
	}
	fresh := reflect.New(t.Elem()).Interface()
	refused, ok := decodeWithoutUnknownEnums(raw, fresh)
	if !ok {
		return Mismatch("does not match the declared type", nil)
	}
	errs := validate.NewValidationErrors()
	if validator, isObject := fresh.(interface {
		Validate() validate.ValidationErrors
	}); isObject {
		if own := validator.Validate(); own != nil {
			errs = own
		}
	}
	mergeErrors(errs, refused)
	return Mismatch("validation failed", errs)
}

// decodeWithoutUnknownEnums decodes raw into target, a pointer, with each
// string an enum type at its path lacks replaced by the enum's first value,
// and returns "enum" at each such path, keyed as the type's Validate keys
// its errors. ok is false when raw holds no such string or does not decode
// once they are replaced.
func decodeWithoutUnknownEnums(raw json.RawMessage, target any) (refused validate.ValidationErrors, ok bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return nil, false
	}
	refused = validate.NewValidationErrors()
	value = replaceUnknownEnums(value, reflect.TypeOf(target).Elem(), refused, "")
	if !refused.HasErrors() {
		return nil, false
	}
	patched, err := json.Marshal(value)
	if err != nil || json.Unmarshal(patched, target) != nil {
		return nil, false
	}
	return refused, true
}

// replaceUnknownEnums walks value, a decoded JSON value, as the Go type t
// reads it and returns it with each string an enum lacks replaced by the
// enum's first value, recording "enum" for it in errs at key. A list's
// elements are at key[i], a map's values at key[k], and an object's fields
// at their JSON keys in a ValidationErrors nested under key, or in errs
// itself for the top-level object (key ""). A value of another JSON type
// than t's is left for the decoder to refuse, and a union, whose member
// the union's pick chooses, is not walked.
func replaceUnknownEnums(value any, t reflect.Type, errs validate.ValidationErrors, key string) any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if inner, wraps := inputFieldValue(t); wraps {
		return replaceUnknownEnums(value, inner, errs, key)
	}
	switch {
	case isEnumType(t):
		text, isString := value.(string)
		if !isString {
			return value
		}
		candidate := reflect.New(t).Elem()
		candidate.SetString(text)
		if candidate.MethodByName("IsValid").Call(nil)[0].Bool() {
			return value
		}
		members := candidate.MethodByName("Values").Call(nil)[0]
		if members.Len() == 0 {
			return value
		}
		errs.SetFieldErrors(key, []validate.ValidationError{enumError})
		return members.Index(0).String()
	case t.Kind() == reflect.Slice && t.Elem().Kind() != reflect.Uint8:
		items, isList := value.([]any)
		if !isList {
			return value
		}
		for i, item := range items {
			items[i] = replaceUnknownEnums(item, t.Elem(), errs, fmt.Sprintf("%s[%d]", key, i))
		}
	case t.Kind() == reflect.Map && t.Key().Kind() == reflect.String:
		entries, isObject := value.(map[string]any)
		if !isObject {
			return value
		}
		for k, entry := range entries {
			entries[k] = replaceUnknownEnums(entry, t.Elem(), errs, fmt.Sprintf("%s[%s]", key, k))
		}
	case t.Kind() == reflect.Struct && isObjectType(t):
		object, isObject := value.(map[string]any)
		if !isObject {
			return value
		}
		nested := errs
		if key != "" {
			nested = validate.NewValidationErrors()
		}
		for jsonKey, member := range object {
			field, found := fieldOfKey(t, jsonKey)
			if !found {
				continue
			}
			object[jsonKey] = replaceUnknownEnums(member, field.Type, nested, field.name)
		}
		if key != "" && nested.HasErrors() {
			errs.AddNestedError(key, nested)
		}
	}
	return value
}

// isEnumType reports whether t is a generated enum: a string type with
// IsValid and a Values method that lists its members.
func isEnumType(t reflect.Type) bool {
	if t.Kind() != reflect.String {
		return false
	}
	isValid, ok := t.MethodByName("IsValid")
	if !ok || isValid.Type.NumIn() != 1 || isValid.Type.NumOut() != 1 || isValid.Type.Out(0).Kind() != reflect.Bool {
		return false
	}
	values, ok := t.MethodByName("Values")
	return ok && values.Type.NumIn() == 1 && values.Type.NumOut() == 1 && values.Type.Out(0) == reflect.SliceOf(t)
}

// isObjectType reports whether t is a generated object type, which lists
// its JSON keys.
func isObjectType(t reflect.Type) bool {
	_, ok := reflect.PointerTo(t).MethodByName("JSONFieldNames")
	return ok
}

// inputFieldValue returns the type an InputField[T] wraps: a struct of Set,
// Null and Value, none of them a JSON key.
func inputFieldValue(t reflect.Type) (reflect.Type, bool) {
	if t.Kind() != reflect.Struct || t.NumField() != 3 || !strings.HasPrefix(t.Name(), "InputField[") {
		return nil, false
	}
	value, ok := t.FieldByName("Value")
	if !ok {
		return nil, false
	}
	return value.Type, true
}

type jsonField struct {
	name string
	Type reflect.Type
}

// fieldOfKey returns the field of the struct t that encoding/json decodes
// the JSON key into: the field named key, or else one named key without
// regard to case.
func fieldOfKey(t reflect.Type, key string) (jsonField, bool) {
	var folded *jsonField
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = field.Name
		}
		if name == key {
			return jsonField{name: name, Type: field.Type}, true
		}
		if folded == nil && strings.EqualFold(name, key) {
			folded = &jsonField{name: name, Type: field.Type}
		}
	}
	if folded != nil {
		return *folded, true
	}
	return jsonField{}, false
}

// mergeErrors adds src's errors to dst, merging nested errors under the
// same key and replacing a key's field errors.
func mergeErrors(dst, src validate.ValidationErrors) {
	for key, value := range src {
		if nested, ok := value.(validate.ValidationErrors); ok {
			if existing, ok := dst[key].(validate.ValidationErrors); ok {
				mergeErrors(existing, nested)
				continue
			}
		}
		dst[key] = value
	}
}
