package validate

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/parable-work/superschematic/ir"
)

// validateScalarRequired validates a scalar value with a required check, mirroring the
// generated ValidateRequired() method. For string primitives, an empty string produces
// a "required" error. For all primitives, IR constraints are then applied.
func (v *Validator) validateScalarRequired(scalar *ir.ScalarDef, value any) []ValidationError {
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
// (matching generated behavior for optional fields).
func (v *Validator) validateScalarValue(scalar *ir.ScalarDef, value any) []ValidationError {
	if isStringPrimitive(scalar.Primitive) {
		s, ok := value.(string)
		if !ok {
			return nil
		}
		if s == "" {
			return nil
		}
		return v.validateScalarConstraints(scalar, s)
	}

	return v.validateScalarConstraints(scalar, value)
}

// validateScalarConstraints validates a value against a scalar definition.
//
// The registry governs every name it holds: when a validator is registered
// for scalar.Name the value goes to it and the result is returned as-is,
// regardless of HasCustomValidate. [DefaultRegistry] registers the linked
// scalar core's dispatch for every name it knows, which carries the PAR-12
// custom validators (Embedding.Vector / Generic.StringMap deep serde,
// Asset.FilePath / Text.Markdown min_length 1) that the generic IR
// pattern/min/max logic below cannot express. An injected registry decides
// the set for itself; the validator never consults the core directly.
//
// The empty-string contract is handled by the callers (validateScalarValue skips
// empty optional strings; validateScalarRequired emits "required" for empty
// required strings) BEFORE reaching here, so a registered validator never sees
// an empty string it would otherwise reject for a min_length scalar on an
// optional field.
//
// Names with no registered validator (inline schema scalars, test fixtures)
// fall back to the generic IR constraint logic.
func (v *Validator) validateScalarConstraints(scalar *ir.ScalarDef, value any) []ValidationError {
	switch scalar.Primitive {
	case "String", "":
		if fn, ok := v.registry.Get(scalar.Name); ok {
			return validateStringVia(fn, value)
		}
		return v.validateStringConstraints(scalar, value)
	case "Int":
		if fn, ok := v.registry.Get(scalar.Name); ok {
			return validateIntVia(fn, value)
		}
		return v.validateIntConstraints(scalar, value)
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
