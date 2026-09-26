package validate

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/runtime/schema/go/internal/jsonshape"
)

// validateScalarRequired validates a scalar value with a required check, mirroring the
// generated ValidateRequired() method. For string primitives, an empty string produces
// a "required" error. For all primitives, IR constraints are then applied.
func (v *Validator) validateScalarRequired(scalar *ir.ScalarDef, value any) []ValidationError {
	if scalar.IsAnyJSON() {
		return validateAnyJSON(value)
	}
	if shape := scalar.StructuredJSONType(); shape != "" {
		if _, isText := value.(string); !isText {
			return v.validateStructuredJSON(scalar, shape, value)
		}
	}
	if isStringPrimitive(scalar.Primitive) {
		s, ok := value.(string)
		if !ok {
			return []ValidationError{{Validator: "type", Message: "expected string value"}}
		}
		if s == "" {
			return []ValidationError{{Validator: "required", Message: "required field"}}
		}
		return v.validateScalarConstraints(scalar, s)
	}

	return v.validateScalarConstraints(scalar, value)
}

// validateScalarValue validates a scalar value without a required check, mirroring the
// generated Validate() method. For string primitives, empty strings are skipped
// (matching generated behavior for optional fields), and a value of another
// JSON type is "type", as it is for a required field.
func (v *Validator) validateScalarValue(scalar *ir.ScalarDef, value any) []ValidationError {
	if scalar.IsAnyJSON() {
		return validateAnyJSON(value)
	}
	if shape := scalar.StructuredJSONType(); shape != "" {
		if _, isText := value.(string); !isText {
			return v.validateStructuredJSON(scalar, shape, value)
		}
	}
	if isStringPrimitive(scalar.Primitive) {
		s, ok := value.(string)
		if !ok {
			return []ValidationError{{Validator: "type", Message: "expected string value"}}
		}
		if s == "" {
			return nil
		}
		return v.validateScalarConstraints(scalar, s)
	}

	return v.validateScalarConstraints(scalar, value)
}

// validateAnyJSON validates a value of a scalar whose value is any JSON value
// (ir.ScalarDef.IsAnyJSON; Generic.JSON in the core catalog). The catalog
// gives it the String primitive, but an object, an array, a string, a number
// and a boolean are all values, so no type, length or pattern check applies,
// and a string need not be JSON text. The callers report a null or missing
// required value as "required" before this runs. A value no JSON document can
// carry, such as a NaN, is "type".
func validateAnyJSON(value any) []ValidationError {
	if !isJSONValue(value, 0) {
		return []ValidationError{{Validator: "type", Message: "expected a JSON value"}}
	}
	return nil
}

// validateStructuredJSON validates a present value, other than a string, of
// a scalar whose value is a JSON object or a JSON array
// (ir.ScalarDef.StructuredJSONType; Generic.StringMap and Embedding.Vector in
// the core catalog). The catalog gives it the String primitive, but the
// object or array is the value every generated type holds. A value of
// another JSON type, or one no JSON document can carry, is "type". The
// object or array goes to the registered validator as its JSON text, the
// form the scalar core reads, so the core checks what it holds (a string
// map's values, a vector's numbers). A string is the value's JSON text: the
// callers hand it to the String primitive's checks, as before.
func (v *Validator) validateStructuredJSON(scalar *ir.ScalarDef, shape string, value any) []ValidationError {
	if jsonshape.Of(value) != shape || !isJSONValue(value, 0) {
		return []ValidationError{{Validator: "type", Message: "expected " + jsonshape.Noun(shape)}}
	}
	fn, ok := v.registry.Get(scalar.Name)
	if !ok {
		return nil
	}
	text, err := json.Marshal(value)
	if err != nil {
		return []ValidationError{{Validator: "type", Message: "expected " + jsonshape.Noun(shape)}}
	}
	return fn(string(text))
}

// maxJSONDepth is encoding/json's nesting limit. No decoded value is deeper,
// so a deeper one (a map that holds itself) is not a JSON value.
const maxJSONDepth = 10000

// isJSONValue reports whether value is a JSON value: what encoding/json
// decodes into an any, or another Go value it can encode. A NaN or an
// infinite number is not.
func isJSONValue(value any, depth int) bool {
	if depth > maxJSONDepth {
		return false
	}
	switch v := value.(type) {
	case nil, bool, string:
		return true
	case float64:
		return !math.IsNaN(v) && !math.IsInf(v, 0)
	case []any:
		for _, elem := range v {
			if !isJSONValue(elem, depth+1) {
				return false
			}
		}
		return true
	case map[string]any:
		for _, elem := range v {
			if !isJSONValue(elem, depth+1) {
				return false
			}
		}
		return true
	}
	_, err := json.Marshal(value)
	return err == nil
}

// validateScalarConstraints validates a value against a scalar definition.
//
// The IR constraints (length, pattern, reserved words, range) run first, and
// a failure is reported by their names (maxLength, pattern, min, ...), as
// every other validator reports it. Only a value they accept goes to the
// registry: when a validator is registered for scalar.Name, regardless of
// HasCustomValidate, its result is returned as-is. [DefaultRegistry]
// registers the linked scalar core's dispatch for every name it knows,
// which carries the deep custom validators (Embedding.Vector /
// Generic.StringMap deep serde, Asset.FilePath / Text.Markdown min_length 1)
// that the IR constraints cannot express. The core checks the IR
// constraints again, so running it only after they pass reports one error
// per failing value. An injected registry decides the set for itself; the
// validator never consults the core directly.
//
// The empty-string contract is handled by the callers (validateScalarValue skips
// empty optional strings; validateScalarRequired emits "required" for empty
// required strings) BEFORE reaching here, so a registered validator never sees
// an empty string it would otherwise reject for a min_length scalar on an
// optional field.
//
// Names with no registered validator (inline schema scalars, test fixtures)
// get the IR constraints alone.
func (v *Validator) validateScalarConstraints(scalar *ir.ScalarDef, value any) []ValidationError {
	switch scalar.Primitive {
	case "String", "":
		if errs := v.validateStringConstraints(scalar, value); len(errs) > 0 {
			return errs
		}
		if fn, ok := v.registry.Get(scalar.Name); ok {
			return validateStringVia(fn, value)
		}
		return nil
	case "Int":
		if errs := v.validateIntConstraints(scalar, value); len(errs) > 0 {
			return errs
		}
		if fn, ok := v.registry.Get(scalar.Name); ok {
			return validateIntVia(fn, value)
		}
		return nil
	case "Float":
		return v.validateFloatConstraints(scalar, value)
	default:
		return nil
	}
}

func validateIntVia(fn ScalarValidateFunc, value any) []ValidationError {
	i, ok := toInt64(value)
	if !ok {
		return []ValidationError{{Validator: "type", Message: "expected integer value"}}
	}
	return fn(strconv.FormatInt(i, 10))
}

// validateStringVia hands a string value to a registered validator. A
// non-string value is a type error, matching validateStringConstraints.
func validateStringVia(fn ScalarValidateFunc, value any) []ValidationError {
	s, ok := value.(string)
	if !ok {
		return []ValidationError{{Validator: "type", Message: "expected string value"}}
	}
	return fn(s)
}

// validateStringConstraints applies MinLength, MaxLength, Pattern, and custom validation
// to a string value.
func (v *Validator) validateStringConstraints(scalar *ir.ScalarDef, value any) []ValidationError {
	s, ok := value.(string)
	if !ok {
		return []ValidationError{{Validator: "type", Message: "expected string value"}}
	}

	var errs []ValidationError

	if scalar.MinLength > 0 && len(s) < scalar.MinLength {
		errs = append(errs, ValidationError{
			Validator: "minLength",
			Message:   fmt.Sprintf("must be at least %d characters", scalar.MinLength),
		})
	}

	if scalar.MaxLength > 0 && len(s) > scalar.MaxLength {
		errs = append(errs, ValidationError{
			Validator: "maxLength",
			Message:   fmt.Sprintf("must be at most %d characters", scalar.MaxLength),
		})
	}

	if scalar.Pattern != "" {
		re := v.getPattern(scalar.Pattern)
		if re != nil && !re.MatchString(s) {
			errs = append(errs, ValidationError{
				Validator: "pattern",
				Message:   "invalid format",
			})
		}
	}

	if len(scalar.ReservedWords) > 0 {
		if scalar.CaseInsensitive {
			sLower := strings.ToLower(s)
			for _, reserved := range scalar.ReservedWords {
				if sLower == strings.ToLower(reserved) {
					errs = append(errs, ValidationError{
						Validator: "reservedWord",
						Message:   "contains reserved word",
					})
					break
				}
			}
		} else {
			for _, reserved := range scalar.ReservedWords {
				if s == reserved {
					errs = append(errs, ValidationError{
						Validator: "reservedWord",
						Message:   "contains reserved word",
					})
					break
				}
			}
		}
	}

	return errs
}

// validateIntConstraints applies Minimum and Maximum to a numeric value.
func (v *Validator) validateIntConstraints(scalar *ir.ScalarDef, value any) []ValidationError {
	i, ok := toInt64(value)
	if !ok {
		return []ValidationError{{Validator: "type", Message: "expected integer value"}}
	}

	var errs []ValidationError

	if scalar.Minimum != nil && i < *scalar.Minimum {
		errs = append(errs, ValidationError{
			Validator: "min",
			Message:   fmt.Sprintf("must be at least %d", *scalar.Minimum),
		})
	}

	if scalar.Maximum != nil && i > *scalar.Maximum {
		errs = append(errs, ValidationError{
			Validator: "max",
			Message:   fmt.Sprintf("must be at most %d", *scalar.Maximum),
		})
	}

	return errs
}

// validateFloatConstraints applies Minimum and Maximum to a float value.
func (v *Validator) validateFloatConstraints(scalar *ir.ScalarDef, value any) []ValidationError {
	f, ok := toFloat64(value)
	if !ok {
		return []ValidationError{{Validator: "type", Message: "expected numeric value"}}
	}

	var errs []ValidationError

	if scalar.Minimum != nil && f < float64(*scalar.Minimum) {
		errs = append(errs, ValidationError{
			Validator: "min",
			Message:   fmt.Sprintf("must be at least %d", *scalar.Minimum),
		})
	}

	if scalar.Maximum != nil && f > float64(*scalar.Maximum) {
		errs = append(errs, ValidationError{
			Validator: "max",
			Message:   fmt.Sprintf("must be at most %d", *scalar.Maximum),
		})
	}

	return errs
}

// getPattern returns a compiled regex for the given pattern string, using the
// pre-compiled cache populated during Validator construction.
func (v *Validator) getPattern(pattern string) *regexp.Regexp {
	return v.patterns[pattern]
}

// isStringPrimitive returns true if the scalar primitive indicates a string type.
// An empty primitive defaults to String (most custom scalars are string-based).
func isStringPrimitive(primitive string) bool {
	return primitive == "String" || primitive == ""
}

// toInt64 coerces a value to int64. Handles float64 (from JSON unmarshal),
// and native Go integer types.
func toInt64(value any) (int64, bool) {
	switch v := value.(type) {
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || math.Trunc(v) != v ||
			v < math.MinInt64 || v >= float64(math.MaxInt64) {
			return 0, false
		}
		return int64(v), true
	case int:
		return int64(v), true
	case int64:
		return v, true
	case int32:
		return int64(v), true
	default:
		return 0, false
	}
}

// toFloat64 coerces a value to float64. Handles native float and integer types.
func toFloat64(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case int32:
		return float64(v), true
	default:
		return 0, false
	}
}
