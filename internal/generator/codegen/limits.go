package codegen

import (
	"fmt"

	ir "github.com/parable-work/superschematic/ir"
)

// LimitKind names one limit a field can declare with Validate<T, {...}>.
// Its value is the validator's name.
type LimitKind string

const (
	LimitMinLength LimitKind = "minLength"
	LimitMaxLength LimitKind = "maxLength"
	LimitMin       LimitKind = "min"
	LimitMax       LimitKind = "max"
	LimitListMin   LimitKind = "listMin"
	LimitListMax   LimitKind = "listMax"
)

// LimitKinds lists every limit kind in the order generators write them.
var LimitKinds = []LimitKind{LimitMinLength, LimitMaxLength, LimitMin, LimitMax, LimitListMin, LimitListMax}

// Words returns the kind's name as words, such as ["max", "length"], so
// each generator can case a constant's name its own way.
func (k LimitKind) Words() []string {
	switch k {
	case LimitMinLength:
		return []string{"min", "length"}
	case LimitMaxLength:
		return []string{"max", "length"}
	case LimitListMin:
		return []string{"list", "min"}
	case LimitListMax:
		return []string{"list", "max"}
	}
	return []string{string(k)}
}

// FieldLimit is one effective limit of a field.
type FieldLimit struct {
	Kind LimitKind

	// Value is the effective bound. A length or a list bound is a whole
	// number.
	Value float64

	// FromScalar is true when the scalar's bound is the effective one. On
	// a tie the field's bound wins.
	FromScalar bool

	// Declared is true when the field declares this kind with Validate<>.
	// A generator writes a constant for a declared limit only: a limit
	// that only the scalar sets belongs to the scalar.
	Declared bool
}

// Literal renders the value as a number literal every target language
// reads: 64, 2.5 or -9007199254740991.
func (l FieldLimit) Literal() string {
	if l.FromScalar || isLengthOrListKind(l.Kind) {
		return fmt.Sprintf("%d", int64(l.Value))
	}
	return fmt.Sprintf("%v", l.Value)
}

func isLengthOrListKind(kind LimitKind) bool {
	switch kind {
	case LimitMinLength, LimitMaxLength, LimitListMin, LimitListMax:
		return true
	}
	return false
}

// EffectiveLimits returns a field's effective limits, in LimitKinds order.
// A field's Validate<> limit and its scalar's limit of the same kind merge
// into one: the larger of the two minimums and the smaller of the two
// maximums, which is the bound a value must meet when a validator checks
// both. A scalar sets minLength and maxLength only above 0. List bounds
// come only from the field.
func EffectiveLimits(scalar *ScalarInfo, field *ir.FieldDef) []FieldLimit {
	var limits []FieldLimit
	for _, kind := range LimitKinds {
		fieldBound, declared := fieldLimitValue(field, kind)
		scalarBound, scalarHas := scalarLimitValue(scalar, kind)
		switch {
		case declared && scalarHas:
			value, fromScalar := fieldBound, false
			if tighterLimit(kind, scalarBound, fieldBound) {
				value, fromScalar = scalarBound, true
			}
			limits = append(limits, FieldLimit{Kind: kind, Value: value, FromScalar: fromScalar, Declared: true})
		case declared:
			limits = append(limits, FieldLimit{Kind: kind, Value: fieldBound, Declared: true})
		case scalarHas:
			limits = append(limits, FieldLimit{Kind: kind, Value: scalarBound, FromScalar: true})
		}
	}
	return limits
}

// DeclaredLimitKinds returns the limit kinds a field declares with
// Validate<>, in LimitKinds order. A generator that aliases a dependency's
// type uses it to alias the dependency's constants.
func DeclaredLimitKinds(field *ir.FieldDef) []LimitKind {
	var kinds []LimitKind
	for _, kind := range LimitKinds {
		if _, declared := fieldLimitValue(field, kind); declared {
			kinds = append(kinds, kind)
		}
	}
	return kinds
}

// DeclaredLimits returns the field's effective limits that the field
// declares with Validate<>.
func (f FieldInfo) DeclaredLimits() []FieldLimit {
	var declared []FieldLimit
	for _, limit := range f.Limits {
		if limit.Declared {
			declared = append(declared, limit)
		}
	}
	return declared
}

// tighterLimit reports whether candidate is a tighter bound of kind than
// current.
func tighterLimit(kind LimitKind, candidate, current float64) bool {
	switch kind {
	case LimitMinLength, LimitMin, LimitListMin:
		return candidate > current
	}
	return candidate < current
}

func fieldLimitValue(field *ir.FieldDef, kind LimitKind) (float64, bool) {
	if field == nil {
		return 0, false
	}
	var intValue *int
	switch kind {
	case LimitMinLength:
		intValue = field.ValidateMinLength
	case LimitMaxLength:
		intValue = field.ValidateMaxLength
	case LimitListMin:
		intValue = field.ValidateListMin
	case LimitListMax:
		intValue = field.ValidateListMax
	case LimitMin:
		if field.ValidateMin != nil {
			return *field.ValidateMin, true
		}
		return 0, false
	case LimitMax:
		if field.ValidateMax != nil {
			return *field.ValidateMax, true
		}
		return 0, false
	}
	if intValue == nil {
		return 0, false
	}
	return float64(*intValue), true
}

func scalarLimitValue(scalar *ScalarInfo, kind LimitKind) (float64, bool) {
	if scalar == nil {
		return 0, false
	}
	switch kind {
	case LimitMinLength:
		return float64(scalar.MinLength), scalar.MinLength > 0
	case LimitMaxLength:
		return float64(scalar.MaxLength), scalar.MaxLength > 0
	case LimitMin:
		if scalar.Minimum != nil {
			return float64(*scalar.Minimum), true
		}
	case LimitMax:
		if scalar.Maximum != nil {
			return float64(*scalar.Maximum), true
		}
	}
	return 0, false
}
