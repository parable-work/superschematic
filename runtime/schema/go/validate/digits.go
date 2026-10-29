package validate

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// decimalText is a Generic.Decimal's text, trimmed: an optional sign, the
// digits before the point and those after it.
var decimalText = regexp.MustCompile(`^[+-]?([0-9]+)(?:\.([0-9]+))?$`)

// validateFieldDigits checks a field's digit rules (D84) on one value: a
// Generic.Decimal's canonical form holds at most precision less scale
// digits before the point, without leading zeros ("precision"), and at
// most scale after it, without trailing zeros ("scale"); an
// Embedding.Vector, the JSON array or the one its JSON text holds, has
// exactly dimensions numbers ("dimensions"). A value of another shape is
// the scalar's to report, and is not counted.
func validateFieldDigits(field *ir.FieldDef, value any) []ValidationError {
	var errs []ValidationError
	if field.ValidatePrecision != nil {
		if s, ok := value.(string); ok {
			if m := decimalText.FindStringSubmatch(strings.TrimSpace(s)); m != nil {
				scale := field.DecimalScale()
				integerDigits := *field.ValidatePrecision - scale
				if len(strings.TrimLeft(m[1], "0")) > integerDigits {
					errs = append(errs, ValidationError{Validator: "precision", Message: "must have at most " + strconv.Itoa(integerDigits) + " digits before the decimal point"})
				}
				if len(strings.TrimRight(m[2], "0")) > scale {
					errs = append(errs, ValidationError{Validator: "scale", Message: "must have at most " + strconv.Itoa(scale) + " digits after the decimal point"})
				}
			}
		}
	}
	if field.ValidateDimensions != nil {
		vector := value
		if s, ok := value.(string); ok && s != "" {
			vector = nil
			_ = json.Unmarshal([]byte(s), &vector)
		}
		if items, ok := vector.([]any); ok && len(items) != *field.ValidateDimensions {
			errs = append(errs, ValidationError{Validator: "dimensions", Message: "must have exactly " + strconv.Itoa(*field.ValidateDimensions) + " dimensions"})
		}
	}
	return errs
}
