// Package bodyargs decodes the body arguments of an operation that has no
// input type: its arguments other than path and query parameters. The
// request body is one JSON object, and each argument is read from its own
// JSON value and checked by the list rules and the value rules every
// validator shares. A failure is recorded in a ValidationErrors under the
// argument's path, so a client reads it as it reads an input type's field
// errors:
//
//   - A required argument is present: absent or null is "required" at its
//     name. An optional one that is absent or null is its zero value, but
//     for an optional JSON value built with KeepNull (Generic.JSON), whose
//     null is a value: it decodes to the JSON null token, apart from an
//     absent one. [] satisfies a required list.
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
//
// The list arguments of a GET operation travel in the query string instead.
// QueryList reads one with the same Arg and the same rules: each item is
// read as its kind's JSON value and then checked as a list element at
// name[i].
package bodyargs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

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
	// Any is any JSON value (Generic.JSON). Null is a value only of an
	// optional single argument built with KeepNull; otherwise it is
	// "required" or the zero value. The Go type's decoder decides the rest.
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
	keepNull bool
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

// KeepNull makes a present null a value of an optional single argument
// whose Go type holds JSON null, as Generic.JSON's does: Value decodes it
// into T (the JSON null token), so the implementation tells it from an
// absent argument, which stays T's zero value (nil). A required argument's
// null is still "required". List and map decoders ignore it: an element is
// never null, and an absent or null list or map is nil.
func KeepNull() Option { return func(a *Arg) { a.keepNull = true } }

// ListMin is the least number of elements of a list argument ("listMin").
func ListMin(n int) Option { return func(a *Arg) { a.listMin = n } }

// ListMax is the greatest number of elements of a list argument ("listMax").
func ListMax(n int) Option { return func(a *Arg) { a.listMax = n } }

// MinLength is the least length of a string value ("minLength"), in Unicode
// code points as every validator counts it.
func MinLength(n int) Option {
	return func(a *Arg) { a.rules = append(a.rules, rule{kind: ruleMinLength, length: n}) }
}

// MaxLength is the greatest length of a string value ("maxLength"), in Unicode
// code points.
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
// zero T, and "required" when the argument is required. With KeepNull, a
// null value of an optional argument is T decoded from null.
func Value[T any](errs validate.ValidationErrors, body Body, arg *Arg) T {
	var value T
	raw := present(body, arg.name)
	if raw == nil {
		if arg.required {
			errs.AddFieldError(arg.name, "required", "required field")
		} else if _, ok := body[arg.name]; ok && arg.keepNull {
			if err := json.Unmarshal([]byte("null"), &value); err != nil {
				errs.AddFieldError(arg.name, "type", "does not match the declared type")
			}
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

// QueryList decodes a list argument of a GET operation from the query
// string: every occurrence of the argument's key, each split on commas, each
// item trimmed and an empty item dropped (?labels=a,b&labels=c is [a b c]).
// No item is an absent list: nil, and "required" when the argument is
// required. ListMin and ListMax bound the number of items.
//
// Each item is read as its kind's JSON value, then checked and decoded as a
// list element at name[i]. A Number or Integer item is a JSON number ("type"
// otherwise, so NaN, Infinity and 0x10 are refused, and an Integer's 1.5 is
// "type" when it decodes), a Boolean item is one strconv.ParseBool accepts,
// and an item of any other kind is its text as a JSON string.
func QueryList[T any](errs validate.ValidationErrors, query url.Values, arg *Arg) []T {
	var items []string
	for _, raw := range query[arg.name] {
		for _, item := range strings.Split(raw, ",") {
			if item = strings.TrimSpace(item); item != "" {
				items = append(items, item)
			}
		}
	}
	if len(items) == 0 {
		if arg.required {
			errs.AddFieldError(arg.name, "required", "required field")
		}
		return nil
	}
	arg.checkBounds(errs, len(items))
	values := make([]T, len(items))
	for i, item := range items {
		path := fmt.Sprintf("%s[%d]", arg.name, i)
		raw, ok := arg.kind.fromQuery(item)
		if !ok {
			errs.AddFieldError(path, "type", arg.kind.typeMessage())
			continue
		}
		decode(errs, arg, path, raw, &values[i])
	}
	return values
}

// fromQuery reads a query string item as a JSON value of the kind. ok is
// false for a Number or Integer item that is not a JSON number and a Boolean
// item strconv.ParseBool refuses.
func (k Kind) fromQuery(item string) (json.RawMessage, bool) {
	switch k {
	case Number, Integer:
		raw := json.RawMessage(item)
		return raw, k.matches(raw) && json.Valid(raw)
	case Boolean:
		b, err := strconv.ParseBool(item)
		if err != nil {
			return nil, false
		}
		return json.RawMessage(strconv.FormatBool(b)), true
	default:
		raw, err := json.Marshal(item)
		return raw, err == nil
	}
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
	a.checkBounds(errs, len(elements))
	return elements, true
}

// checkBounds applies ListMin and ListMax to a list of n elements.
func (a *Arg) checkBounds(errs validate.ValidationErrors, n int) {
	if a.listMin >= 0 && n < a.listMin {
		errs.AddFieldError(a.name, "listMin", fmt.Sprintf("must contain at least %d items", a.listMin))
	}
	if a.listMax >= 0 && n > a.listMax {
		errs.AddFieldError(a.name, "listMax", fmt.Sprintf("must contain at most %d items", a.listMax))
	}
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
			if utf8.RuneCountInString(s) < r.length {
				return validate.ValidationError{Validator: "minLength", Message: fmt.Sprintf("must be at least %d characters", r.length)}, false
			}
		case ruleMaxLength:
			if utf8.RuneCountInString(s) > r.length {
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
