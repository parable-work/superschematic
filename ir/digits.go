package ir

import "fmt"

// The scalars Validate's digit and length rules bound (D84): a decimal's
// precision and scale, and a vector's dimensions.
const (
	DecimalScalarName = "Generic.Decimal"
	VectorScalarName  = "Embedding.Vector"
)

// The bounds the rules may take: Postgres's NUMERIC holds at most 1000
// digits, and pgvector's vector type at most 16000 dimensions.
const (
	MaxDecimalPrecision = 1000
	MaxVectorDimensions = 16000
)

// DecimalScale is the scale of a field with a precision: its
// ValidateScale, or 0, as NUMERIC(p) means NUMERIC(p,0).
func (f *FieldDef) DecimalScale() int {
	if f.ValidateScale != nil {
		return *f.ValidateScale
	}
	return 0
}

// validateDigitRules checks Validate's precision, scale and dimensions on a
// field (D84): precision and scale on a Generic.Decimal alone, dimensions
// on an Embedding.Vector alone, each a whole number in range, a scale with
// a precision no smaller than it, and neither on a map or a list of lists.
func validateDigitRules(owner string, f *FieldDef) []error {
	if f.ValidatePrecision == nil && f.ValidateScale == nil && f.ValidateDimensions == nil {
		return nil
	}
	var errs []error
	where := owner + "." + f.Name
	if f.TypeRef.IsMap || f.TypeRef.IsArrayOfArrays {
		errs = append(errs, fmt.Errorf("%s: Validate's precision, scale and dimensions bound a single value or a list of them, not a map or a list of lists; support follows", where))
	}
	if f.ValidatePrecision != nil || f.ValidateScale != nil {
		switch {
		case f.TypeRef.Name != DecimalScalarName:
			errs = append(errs, fmt.Errorf("%s: Validate's precision and scale bound a %s, not %s; declare it Validate<Generic.Decimal, { precision, scale }>", where, DecimalScalarName, f.TypeRef.Name))
		case f.ValidatePrecision == nil:
			errs = append(errs, fmt.Errorf("%s: Validate's scale needs a precision, the digits in all: Validate<Generic.Decimal, { precision: 12, scale: %d }>", where, *f.ValidateScale))
		case *f.ValidatePrecision < 1 || *f.ValidatePrecision > MaxDecimalPrecision:
			errs = append(errs, fmt.Errorf("%s: Validate's precision is %d; it must be from 1 to %d", where, *f.ValidatePrecision, MaxDecimalPrecision))
		case f.ValidateScale != nil && *f.ValidateScale < 0:
			errs = append(errs, fmt.Errorf("%s: Validate's scale is %d; it must be 0 or more", where, *f.ValidateScale))
		case f.ValidateScale != nil && *f.ValidateScale > *f.ValidatePrecision:
			errs = append(errs, fmt.Errorf("%s: Validate's scale %d is more than its precision %d; the scale counts the digits after the point among the precision's", where, *f.ValidateScale, *f.ValidatePrecision))
		}
	}
	if f.ValidateDimensions != nil {
		switch {
		case f.TypeRef.Name != VectorScalarName:
			errs = append(errs, fmt.Errorf("%s: Validate's dimensions bound an %s, not %s; declare it Validate<Embedding.Vector, { dimensions: %d }>", where, VectorScalarName, f.TypeRef.Name, *f.ValidateDimensions))
		case *f.ValidateDimensions < 1 || *f.ValidateDimensions > MaxVectorDimensions:
			errs = append(errs, fmt.Errorf("%s: Validate's dimensions is %d; it must be from 1 to %d", where, *f.ValidateDimensions, MaxVectorDimensions))
		}
	}
	return errs
}
