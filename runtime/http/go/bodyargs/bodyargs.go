// Package bodyargs decodes the body arguments of an operation that has no
// input type: its arguments other than path and query parameters. The
// request body is one JSON object, and each argument is read from its own
// JSON value and checked by the list rules and the value rules every
// validator shares. A failure is recorded in a ValidationErrors under the
// argument's path, so a client reads it as it reads an input type's field
// errors:
//
//   - A required argument is present: absent or null is "required" at its
//     name. An optional one that is absent or null is its zero value.
//     [] satisfies a required list.
//   - A value must have its kind's JSON type ("type": "expected a string",
//     "expected a number", ...). A string is not a number, a number is not a
//     string, and "true" is not a boolean.
//   - A list is a JSON array ("type", "expected an array"), bounded by
//     ListMin and ListMax. An element is never null: "required" at name[i].
//     A list of lists has the same rules one level down: an inner list is
//     never null ("required" at name[i]) and is an array ("type" at
//     name[i]); an innermost element is checked at name[i][j].
//   - Each value, alone or as an element, passes the rules its argument
//     was built with, in order: the rules of its scalar type, then the
//     argument's own. The first rule it breaks is its one error, named by
//     the rule ("minLength", "maxLength", "pattern", "min", "max").
//   - A value the rules accept is decoded into its Go type; a string its
//     type's decoder refuses (a malformed UUID or timestamp), and an empty
//     one a non-string type decodes, is "pattern".
//     Then the type's own Validate runs: a scalar's core check, an enum's
//     membership ("enum"), an object's field validation, whose errors nest
//     under the element's path.
//   - A map (Record<string, T>) is a JSON object whose values follow the
//     rules of a list element at name[key]: never null, and checked as a T.
//     A map of lists (Record<string, T[]>) has a list at each key, checked
//     as a list argument's elements at name[key][i].
//
// A generated route builds one Arg per body argument when it is created
// and decodes a request's arguments with Value, List, ListOfLists, Map or
// MapOfLists.
package bodyargs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"regexp"
	"strconv"

	"github.com/parable-work/superschematic/runtime/schema/go/validate"
)

// Body is a request body object: the raw JSON value of each key.
type Body map[string]json.RawMessage

// ErrNotObject is returned by ReadObject for a body that is valid JSON but
// not an object (null, an array, a string, a number or a boolean).
var ErrNotObject = errors.New("request body is not a JSON object")

// ReadObject decodes a request body that is one JSON object. An empty body
// and invalid JSON are the decoder's errors; any other JSON value is
// ErrNotObject.
func ReadObject(r io.Reader) (Body, error) {
	var raw json.RawMessage
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return nil, err
	}
	if trimmed := bytes.TrimSpace(raw); len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, ErrNotObject
	}
	var body Body
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	return body, nil
}

// Kind is the JSON type a value must have: a single argument's value, each
// element of a list or each innermost element of a list of lists.
type Kind uint8

const (
	// Any is any JSON value but null (Generic.JSON). The Go type's decoder
	// decides the rest.
	Any Kind = iota
	// String is a JSON string: a string, enum, UUID, timestamp or other
	// string scalar.
	String
	// Number is a JSON number.
	Number
	// Integer is a JSON number the Go type (int64 or an integer scalar)
	// decodes.
	Integer
	// Boolean is true or false.
	Boolean
	// Object is a JSON object, decoded by its type's decoder.
	Object
	// Array is a JSON array held by a scalar (a vector), not a list.
	Array
)

// typeMessage is the "type" error of a value of the wrong JSON type.
func (k Kind) typeMessage() string {
	switch k {
	case String:
		return "expected a string"
	case Number:
		return "expected a number"
	case Integer:
		return "expected an integer"
	case Boolean:
		return "expected a boolean"
	case Object:
		return "expected an object"
	case Array:
		return "expected an array"
	default:
		return "expected a JSON value"
	}
}

// matches reports whether a non-null JSON value has the kind's JSON type,
// from its first byte.
func (k Kind) matches(value []byte) bool {
	if len(value) == 0 {
		return false
	}
	first := value[0]
	switch k {
	case String:
		return first == '"'
	case Number, Integer:
		return first == '-' || (first >= '0' && first <= '9')
	case Boolean:
		return first == 't' || first == 'f'
	case Object:
		return first == '{'
	case Array:
		return first == '['
	default:
		return true
	}
}

// ruleKind names one value rule.
type ruleKind uint8

const (
	ruleMinLength ruleKind = iota
	ruleMaxLength
	rulePattern
	ruleMin
	ruleMax
)

// rule is one value constraint. A pattern Go cannot compile (pattern is
// nil) accepts no value, as the generated validators treat it.
type rule struct {
	kind    ruleKind
	length  int
	bound   float64
	pattern *regexp.Regexp
}

// Arg is how one body argument is decoded and checked. Build it once with
// NewArg; it is safe for concurrent use.
type Arg struct {
	name     string
	kind     Kind
	required bool
	listMin  int
	listMax  int
	rules    []rule
}

// Option sets one property of an Arg.
type Option func(*Arg)

// NewArg returns the Arg of the body argument name, whose values have kind.
// A list bound applies to a list argument and to the outer list of a list of
// lists; the value rules apply to every value in the order given, and
// only to the kinds they fit (lengths and patterns to strings, min and max
// to numbers).
func NewArg(name string, kind Kind, options ...Option) *Arg {
	arg := &Arg{name: name, kind: kind, listMin: -1, listMax: -1}
	for _, option := range options {
		option(arg)
	}
	return arg
}

// Required makes the argument required: absent or null is "required".
func Required() Option { return func(a *Arg) { a.required = true } }

// ListMin is the least number of elements of a list argument ("listMin").
func ListMin(n int) Option { return func(a *Arg) { a.listMin = n } }

// ListMax is the greatest number of elements of a list argument ("listMax").
func ListMax(n int) Option { return func(a *Arg) { a.listMax = n } }

// MinLength is the least length of a string value ("minLength"), in bytes as
// every Go validator counts it.
func MinLength(n int) Option {
	return func(a *Arg) { a.rules = append(a.rules, rule{kind: ruleMinLength, length: n}) }
}

// MaxLength is the greatest length of a string value ("maxLength"), in bytes.
func MaxLength(n int) Option {
	return func(a *Arg) { a.rules = append(a.rules, rule{kind: ruleMaxLength, length: n}) }
}

// Pattern is a regular expression a string value matches ("pattern"). An
// expression Go cannot compile accepts no value.
func Pattern(expr string) Option {
	compiled, err := regexp.Compile(expr)
	if err != nil {
		compiled = nil
	}
	return func(a *Arg) { a.rules = append(a.rules, rule{kind: rulePattern, pattern: compiled}) }
}

// Min is the least value of a number ("min").
func Min(v float64) Option {
	return func(a *Arg) { a.rules = append(a.rules, rule{kind: ruleMin, bound: v}) }
}

// Max is the greatest value of a number ("max").
func Max(v float64) Option {
	return func(a *Arg) { a.rules = append(a.rules, rule{kind: ruleMax, bound: v}) }
}

// Value decodes a single-valued argument. An absent or null value is the
// zero T, and "required" when the argument is required.
func Value[T any](errs validate.ValidationErrors, body Body, arg *Arg) T {
	var value T
	raw := present(body, arg.name)
	if raw == nil {
		if arg.required {
			errs.AddFieldError(arg.name, "required", "required field")
		}
		return value
	}
	decode(errs, arg, arg.name, raw, &value)
	return value
}

// List decodes a list argument (T[]). An absent or null list is nil, and
// "required" when the argument is required; [] is an empty, non-nil list.
func List[T any](errs validate.ValidationErrors, body Body, arg *Arg) []T {
	elements, ok := arg.list(errs, body)
	if !ok {
		return nil
	}
	values := make([]T, len(elements))
	for i, element := range elements {
		path := fmt.Sprintf("%s[%d]", arg.name, i)
		if isNull(element) {
			errs.AddFieldError(path, "required", "required field")
			continue
		}
		decode(errs, arg, path, element, &values[i])
	}
	return values
}

// ListOfLists decodes a list of lists argument (T[][]): the outer list as
// List does, then each inner list, which is never null and may be empty.
func ListOfLists[T any](errs validate.ValidationErrors, body Body, arg *Arg) [][]T {
	rows, ok := arg.list(errs, body)
	if !ok {
		return nil
	}
	values := make([][]T, len(rows))
	for i, row := range rows {
		rowPath := fmt.Sprintf("%s[%d]", arg.name, i)
		if isNull(row) {
			errs.AddFieldError(rowPath, "required", "required field")
			continue
		}
		var elements []json.RawMessage
		if !Array.matches(row) || json.Unmarshal(row, &elements) != nil {
			errs.AddFieldError(rowPath, "type", Array.typeMessage())
			continue
		}
		values[i] = make([]T, len(elements))
		for j, element := range elements {
			path := fmt.Sprintf("%s[%d][%d]", arg.name, i, j)
			if isNull(element) {
				errs.AddFieldError(path, "required", "required field")
				continue
			}
			decode(errs, arg, path, element, &values[i][j])
		}
	}
	return values
}

// Map decodes a map argument (Record<string, T>): a JSON object ("type",
// "expected an object" otherwise) whose values are never null ("required"
// at name[key]) and pass the checks of a list element, at name[key]. An
// absent or null map is nil, and "required" when the argument is required;
// {} is an empty, non-nil map. List bounds do not apply to a map.
func Map[T any](errs validate.ValidationErrors, body Body, arg *Arg) map[string]T {
	entries, ok := arg.object(errs, body)
	if !ok {
		return nil
	}
	values := make(map[string]T, len(entries))
	for key, entry := range entries {
		path := fmt.Sprintf("%s[%s]", arg.name, key)
		if isNull(entry) {
			errs.AddFieldError(path, "required", "required field")
			continue
		}
		var value T
		decode(errs, arg, path, entry, &value)
		values[key] = value
	}
	return values
}

// MapOfLists decodes a map whose values are lists (Record<string, T[]>):
// the map as Map does, then each value as a list that is never null
// ("required" at name[key]), is an array ("type" at name[key]) and whose
// elements are checked at name[key][i].
func MapOfLists[T any](errs validate.ValidationErrors, body Body, arg *Arg) map[string][]T {
	entries, ok := arg.object(errs, body)
	if !ok {
		return nil
	}
	values := make(map[string][]T, len(entries))
	for key, entry := range entries {
		path := fmt.Sprintf("%s[%s]", arg.name, key)
		if isNull(entry) {
			errs.AddFieldError(path, "required", "required field")
			continue
		}
		var elements []json.RawMessage
		if !Array.matches(bytes.TrimSpace(entry)) || json.Unmarshal(entry, &elements) != nil {
			errs.AddFieldError(path, "type", Array.typeMessage())
			continue
		}
		list := make([]T, len(elements))
		for i, element := range elements {
			elementPath := fmt.Sprintf("%s[%d]", path, i)
			if isNull(element) {
				errs.AddFieldError(elementPath, "required", "required field")
				continue
			}
			decode(errs, arg, elementPath, element, &list[i])
		}
		values[key] = list
	}
	return values
}

// object reads a map argument: absent or null is "required" when the
// argument is required, and anything but a JSON object is "type". ok is
// false when there is no map to decode.
func (a *Arg) object(errs validate.ValidationErrors, body Body) (entries map[string]json.RawMessage, ok bool) {
	raw := present(body, a.name)
	if raw == nil {
		if a.required {
			errs.AddFieldError(a.name, "required", "required field")
		}
		return nil, false
	}
	if !Object.matches(raw) || json.Unmarshal(raw, &entries) != nil {
		errs.AddFieldError(a.name, "type", Object.typeMessage())
		return nil, false
	}
	return entries, true
}

// list reads the outer list of a list argument: absent or null is "required"
// when the argument is required, anything but an array is "type", and
// ListMin and ListMax bound its length. ok is false when there is no list to
// decode.
func (a *Arg) list(errs validate.ValidationErrors, body Body) (elements []json.RawMessage, ok bool) {
	raw := present(body, a.name)
	if raw == nil {
		if a.required {
			errs.AddFieldError(a.name, "required", "required field")
		}
		return nil, false
	}
	if !Array.matches(raw) || json.Unmarshal(raw, &elements) != nil {
		errs.AddFieldError(a.name, "type", Array.typeMessage())
		return nil, false
	}
	if elements == nil {
		elements = []json.RawMessage{}
	}
	if a.listMin >= 0 && len(elements) < a.listMin {
		errs.AddFieldError(a.name, "listMin", fmt.Sprintf("must contain at least %d items", a.listMin))
	}
	if a.listMax >= 0 && len(elements) > a.listMax {
		errs.AddFieldError(a.name, "listMax", fmt.Sprintf("must contain at most %d items", a.listMax))
	}
	return elements, true
}

// decode checks one non-null value and decodes it into target, recording at
// most one error for it at path, or the nested errors of an object.
func decode[T any](errs validate.ValidationErrors, arg *Arg, path string, raw json.RawMessage, target *T) {
	raw = bytes.TrimSpace(raw)
	if !arg.kind.matches(raw) {
		errs.AddFieldError(path, "type", arg.kind.typeMessage())
		return
	}
	switch arg.kind {
	case String:
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			errs.AddFieldError(path, "type", arg.kind.typeMessage())
			return
		}
		if broken, ok := arg.checkString(s); !ok {
			errs.SetFieldErrors(path, []validate.ValidationError{broken})
			return
		}
		// A string a non-string Go type decodes (a UUID, a timestamp) is
		// malformed when the decoder refuses it, and when it is empty: a
		// timestamp decoder reads "" as the zero time, which no client means.
		if err := json.Unmarshal(raw, target); err != nil || (s == "" && reflect.ValueOf(target).Elem().Kind() != reflect.String) {
			errs.AddFieldError(path, "pattern", "invalid format")
			return
		}
	case Number, Integer:
		if err := json.Unmarshal(raw, target); err != nil {
			errs.AddFieldError(path, "type", arg.kind.typeMessage())
			return
		}
		f, err := strconv.ParseFloat(string(raw), 64)
		if err != nil {
			errs.AddFieldError(path, "type", arg.kind.typeMessage())
			return
		}
		if broken, ok := arg.checkNumber(f); !ok {
			errs.SetFieldErrors(path, []validate.ValidationError{broken})
			return
		}
	case Boolean:
		if err := json.Unmarshal(raw, target); err != nil {
			errs.AddFieldError(path, "type", arg.kind.typeMessage())
			return
		}
	default:
		if err := json.Unmarshal(raw, target); err != nil {
			errs.AddFieldError(path, "type", "does not match the declared type")
			return
		}
	}
	validateOwn(errs, path, target)
}

// checkString applies the length and pattern rules in order and returns the
// first one s breaks.
func (a *Arg) checkString(s string) (validate.ValidationError, bool) {
	for _, r := range a.rules {
		switch r.kind {
		case ruleMinLength:
			if len(s) < r.length {
				return validate.ValidationError{Validator: "minLength", Message: fmt.Sprintf("must be at least %d characters", r.length)}, false
			}
		case ruleMaxLength:
			if len(s) > r.length {
				return validate.ValidationError{Validator: "maxLength", Message: fmt.Sprintf("must be at most %d characters", r.length)}, false
			}
		case rulePattern:
			if r.pattern == nil || !r.pattern.MatchString(s) {
				return validate.ValidationError{Validator: "pattern", Message: "invalid format"}, false
			}
		}
	}
	return validate.ValidationError{}, true
}

// checkNumber applies the range rules in order and returns the first one f
// breaks.
func (a *Arg) checkNumber(f float64) (validate.ValidationError, bool) {
	for _, r := range a.rules {
		switch r.kind {
		case ruleMin:
			if f < r.bound {
				return validate.ValidationError{Validator: "min", Message: "must be at least " + formatBound(r.bound)}, false
			}
		case ruleMax:
			if f > r.bound {
				return validate.ValidationError{Validator: "max", Message: "must be at most " + formatBound(r.bound)}, false
			}
		}
	}
	return validate.ValidationError{}, true
}

// validateOwn runs the decoded value's own validation, when its type has
// one: a scalar's or an enum's Validate, whose errors are recorded at path,
// or an object's, whose field errors nest under path. target is a pointer,
// so an object type, whose Validate has a pointer receiver, qualifies.
func validateOwn(errs validate.ValidationErrors, path string, target any) {
	switch validator := target.(type) {
	case interface {
		Validate() (bool, []validate.ValidationError)
	}:
		if valid, own := validator.Validate(); !valid {
			errs.SetFieldErrors(path, own)
		}
	case interface {
		Validate() validate.ValidationErrors
	}:
		if nested := validator.Validate(); nested.HasErrors() {
			errs.AddNestedError(path, nested)
		}
	}
}

// present returns the raw value of key, or nil when it is absent or null.
func present(body Body, key string) json.RawMessage {
	raw, ok := body[key]
	if !ok || isNull(raw) {
		return nil
	}
	return bytes.TrimSpace(raw)
}

func isNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// formatBound writes a bound as the schema wrote it: 1, 0.5, 9007199254740991.
func formatBound(v float64) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e21 {
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}
