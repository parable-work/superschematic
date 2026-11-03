package codegen

import (
	"fmt"

	ir "github.com/parable-work/superschematic/ir"
)

// DigitRules are a field's Validate rules on the digits of a Generic.Decimal
// and the length of an Embedding.Vector (D84). Every validator, generated
// or a schema runtime, checks them on each value, a single one or each
// element of a list, after the scalar's own checks and only on a value of
// the scalar's shape: a decimal's digits are counted on its canonical form
// (no sign, no leading zero before the point, no trailing zero after it),
// and a vector's numbers on the JSON array or the array its JSON text holds.
type DigitRules struct {
	// Decimal is a field with a precision. IntegerDigits is the most digits
	// its canonical form holds before the point, the precision less the
	// scale, a value with more failing "precision"; Scale the most after
	// it, a value with more failing "scale".
	Decimal       bool
	IntegerDigits int
	Scale         int

	// Dimensions is the length every vector has, a value of another failing
	// "dimensions"; 0 for any.
	Dimensions int
}

// The validators the digit rules fail with.
const (
	ValidatorPrecision  = "precision"
	ValidatorScale      = "scale"
	ValidatorDimensions = "dimensions"
)

// DigitRulesOf are field's digit rules, or nil when it has none.
func DigitRulesOf(field *ir.FieldDef) *DigitRules {
	if field == nil || (field.ValidatePrecision == nil && field.ValidateDimensions == nil) {
		return nil
	}
	rules := &DigitRules{}
	if field.ValidatePrecision != nil {
		rules.Decimal = true
		rules.Scale = field.DecimalScale()
		rules.IntegerDigits = *field.ValidatePrecision - rules.Scale
	}
	if field.ValidateDimensions != nil {
		rules.Dimensions = *field.ValidateDimensions
	}
	return rules
}

// PrecisionMessage, ScaleMessage and DimensionsMessage are each rule's
// failure, the same in every validator.
func (d DigitRules) PrecisionMessage() string {
	return fmt.Sprintf("must have at most %d digits before the decimal point", d.IntegerDigits)
}

func (d DigitRules) ScaleMessage() string {
	return fmt.Sprintf("must have at most %d digits after the decimal point", d.Scale)
}

func (d DigitRules) DimensionsMessage() string {
	return fmt.Sprintf("must have exactly %d dimensions", d.Dimensions)
}
