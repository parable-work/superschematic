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

	// ProjectionCapture is the [go.projection_capture] table (D168). Unset,
	// the Go types write no capture functions.
	ProjectionCapture GoProjectionCapture `toml:"projection_capture"`
}

// GoProjectionCapture is the [go.projection_capture] table: two functions
// the Go types package of a DB schema writes, in projection_capture.go,
// for the @projection views that read exactly one setting, Setting, and
// no optional setting, and that are in Pool when it is set (D168).
// Queries names the function that returns each view's SELECT by the
// relation a reader queries. UUIDColumns names the function that returns
// the columns of each of those views whose Postgres type is uuid. A
// caller sets Setting with set_config(name, value, true) in a
// transaction and runs a query to capture what the view serves under that
// value. Empty Queries writes neither function.
type GoProjectionCapture struct {
	Queries     string `toml:"queries"`
	UUIDColumns string `toml:"uuid_columns"`
	Setting     string `toml:"setting"`
	Pool        string `toml:"pool"`
}

var (
	// captureSettingRE is the form of a setting a projection reads:
	// lowercase identifiers joined by dots.
	captureSettingRE = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)
	// capturePoolRE is the form of a projection's pool.
	capturePoolRE = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

// Enabled reports whether the table names the capture functions.
func (c GoProjectionCapture) Enabled() bool { return c.Queries != "" }

// check refuses a table that is half set or names a function, a setting
// or a pool in a form the generated code cannot use.
func (c GoProjectionCapture) check() error {
	if c == (GoProjectionCapture{}) {
		return nil
	}
	for _, kv := range [][2]string{{"queries", c.Queries}, {"uuid_columns", c.UUIDColumns}, {"setting", c.Setting}} {
		if kv[1] == "" {
			return fmt.Errorf("go.projection_capture.%s is empty: set queries, uuid_columns and setting together, or leave the table out", kv[0])
		}
	}
	for _, kv := range [][2]string{{"queries", c.Queries}, {"uuid_columns", c.UUIDColumns}} {
		if !goIdentifierRE.MatchString(kv[1]) {
			return fmt.Errorf("go.projection_capture.%s %q is not an exported Go identifier", kv[0], kv[1])
		}
	}
	if c.Queries == c.UUIDColumns {
		return fmt.Errorf("go.projection_capture.queries and uuid_columns are both %q: give the two functions different names", c.Queries)
	}
	if !captureSettingRE.MatchString(c.Setting) {
		return fmt.Errorf("go.projection_capture.setting %q is not a setting a projection reads: use two or more lowercase identifiers joined by dots", c.Setting)
	}
	if c.Pool != "" && !capturePoolRE.MatchString(c.Pool) {
		return fmt.Errorf("go.projection_capture.pool %q is not a projection pool: use a snake_case identifier, or leave it out", c.Pool)
	}
	return nil
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
	return c.ProjectionCapture.check()
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
