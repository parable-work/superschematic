package naming

import (
	"fmt"
	"regexp"
)

// goSuffixRE is the form of a suffix of an exported Go name.
var goSuffixRE = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*$`)

// GoConfig is the [go] table of superschematic.toml: shapes of the
// generated Go types and ORM beyond the core's. Its defaults are the
// core's output. A project whose hand-written Go code another compiler's
// output shaped sets it, so that code compiles and behaves the same
// against the core's output.
type GoConfig struct {
	// LimitConstants is "off" (default) or "on". "on" writes, beside each
	// type, an untyped constant for each limit a field declares with
	// Validate<T, {...}>, at the bound Validate checks: the field's limit
	// merged with its scalar's (D160). A module that aliases a
	// dependency's type aliases its constants too.
	LimitConstants string `toml:"limit_constants"`

	// OptionalZero is "omit" (default) or "keep" (D161). "omit" is the
	// core's rule (D21): an optional number or string is a value tagged
	// omitempty, and so is an integer scalar or a boolean with a default.
	// "keep" makes a Go type keep every value a payload states: each
	// field whose value type would drop an explicit zero, or read an
	// explicit null as its default, is a pointer instead, and a pointer
	// with a default starts at a pointer to it. The types, the ORM and the
	// env loaders follow it (codegen.GoOptionalShapes).
	OptionalZero string `toml:"optional_zero"`

	// EnvNumberScalars is "parse" (default) or "strconv" (D162): how the
	// Go env loader reads the variable of a number scalar, such as
	// Network.Port. "parse" hands the text to the types package's
	// Parse<Scalar>. "strconv" trims it and reads it with strconv, refuses
	// a float that is not finite, and leaves the scalar's rules to
	// Validate, so a types package with no Parse function for a number
	// scalar needs none.
	EnvNumberScalars string `toml:"env_number_scalars"`

	// ORMRelationFilterSuffix ends the name of the member of a Go ORM
	// filter that keeps the rows whose to-one relation points at a row
	// another filter finds (D90): "" (default) names it after the
	// relation, Vendor; "Where" names it VendorWhere (D163). It holds
	// ASCII letters and digits and starts with an upper-case letter, so
	// the member stays an exported Go name.
	ORMRelationFilterSuffix string `toml:"orm_relation_filter_suffix"`

	// ORMDefaultSelection is "all" (default) or "own" (D164): the fields
	// the Go ORM reads of a relation selected without a nested selection.
	// "all" reads the target's All, whose relations read their All in
	// turn, as deep as the schema goes. "own" writes <Type>Fields.Own, the
	// type's columns with each to-one relation an id-only stub, and reads
	// the target's Own, so a default read is one level deep.
	ORMDefaultSelection string `toml:"orm_default_selection"`

	// EnumTextMethods is "off" (default) or "on" (D165). "on" gives a Go
	// enum whose members' comments carry a `title:` or an `action:` line
	// the methods Title() and Action(), which return the member's line, or
	// "" for a member without one. The comments keep the lines.
	EnumTextMethods string `toml:"enum_text_methods"`
}

// The values of the [go] keys.
const (
	GoOn           = "on"
	GoOff          = "off"
	GoOptionalOmit = "omit"
	GoOptionalKeep = "keep"
	GoEnvParse     = "parse"
	GoEnvStrconv   = "strconv"
	GoSelectAll    = "all"
	GoSelectOwn    = "own"
)

func defaultGoConfig() GoConfig {
	return GoConfig{
		LimitConstants:      GoOff,
		OptionalZero:        GoOptionalOmit,
		EnvNumberScalars:    GoEnvParse,
		ORMDefaultSelection: GoSelectAll,
		EnumTextMethods:     GoOff,
	}
}

// fill gives each empty key its default.
func (c *GoConfig) fill(d GoConfig, fill func(dst *string, def string)) {
	fill(&c.LimitConstants, d.LimitConstants)
	fill(&c.OptionalZero, d.OptionalZero)
	fill(&c.EnvNumberScalars, d.EnvNumberScalars)
	fill(&c.ORMDefaultSelection, d.ORMDefaultSelection)
	fill(&c.EnumTextMethods, d.EnumTextMethods)
}

// check refuses a value the [go] table does not take.
func (c GoConfig) check() error {
	for _, kv := range [][2]string{{"limit_constants", c.LimitConstants}, {"enum_text_methods", c.EnumTextMethods}} {
		if kv[1] != "" && kv[1] != GoOn && kv[1] != GoOff {
			return fmt.Errorf("go.%s is %q: use %q or %q", kv[0], kv[1], GoOn, GoOff)
		}
	}
	if c.OptionalZero != "" && c.OptionalZero != GoOptionalOmit && c.OptionalZero != GoOptionalKeep {
		return fmt.Errorf("go.optional_zero is %q: use %q or %q", c.OptionalZero, GoOptionalOmit, GoOptionalKeep)
	}
	if c.EnvNumberScalars != "" && c.EnvNumberScalars != GoEnvParse && c.EnvNumberScalars != GoEnvStrconv {
		return fmt.Errorf("go.env_number_scalars is %q: use %q or %q", c.EnvNumberScalars, GoEnvParse, GoEnvStrconv)
	}
	if c.ORMDefaultSelection != "" && c.ORMDefaultSelection != GoSelectAll && c.ORMDefaultSelection != GoSelectOwn {
		return fmt.Errorf("go.orm_default_selection is %q: use %q or %q", c.ORMDefaultSelection, GoSelectAll, GoSelectOwn)
	}
	if c.ORMRelationFilterSuffix != "" && !goSuffixRE.MatchString(c.ORMRelationFilterSuffix) {
		return fmt.Errorf("go.orm_relation_filter_suffix is %q: use ASCII letters and digits, starting with an upper-case letter, or leave it out", c.ORMRelationFilterSuffix)
	}
	return nil
}

// WritesLimitConstants reports whether limit_constants is "on".
func (c GoConfig) WritesLimitConstants() bool { return c.LimitConstants == GoOn }

// KeepsOptionalZero reports whether optional_zero is "keep".
func (c GoConfig) KeepsOptionalZero() bool { return c.OptionalZero == GoOptionalKeep }

// ParsesEnvNumbersWithStrconv reports whether env_number_scalars is
// "strconv".
func (c GoConfig) ParsesEnvNumbersWithStrconv() bool { return c.EnvNumberScalars == GoEnvStrconv }

// ReadsOwnByDefault reports whether orm_default_selection is "own".
func (c GoConfig) ReadsOwnByDefault() bool { return c.ORMDefaultSelection == GoSelectOwn }

// WritesEnumTextMethods reports whether enum_text_methods is "on".
func (c GoConfig) WritesEnumTextMethods() bool { return c.EnumTextMethods == GoOn }
