package rustgen

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

// The validators of a Rust types crate (src/validators.rs, D31) check a JSON
// value against its schema type as the generated TypeScript validators do
// (tsgen's validator_*.tmpl, D14): one error per failing value, named by the
// rule it breaks, at the value's wire path. Each check below names the
// TypeScript block it ports, so the two read side by side.

// Kinds of value a field holds, which decide the blocks of its check.
const (
	// fieldScalar is a semantic scalar, checked by its scalar validator.
	fieldScalar = "scalar"
	// fieldEnum is a local or imported enum, checked by its enum validator.
	fieldEnum = "enum"
	// fieldObject is another type this crate generates, validated nested.
	fieldObject = "object"
	// fieldSelf is the type itself: validated nested unless the type is
	// @strictJSON, as tsgen leaves it out of a strict type's nested types.
	fieldSelf = "self"
	// fieldPrimitive is a builtin string, number or boolean.
	fieldPrimitive = "primitive"
	// fieldOther is anything else (a union, an imported object type, the
	// object primitive): only its presence is checked, as in TypeScript.
	fieldOther = "other"
)

// Kinds of scalar validator body.
const (
	scalarString     = "string"
	scalarNumber     = "number"
	scalarStructured = "structured"
	scalarOpaque     = "opaque"
)

// ValidatorsInfo is what validators.tmpl renders.
type ValidatorsInfo struct {
	// RuntimeCrate and RegistryExpr are how the module names the schema
	// runtime crate and the scalar registry.
	RuntimeCrate string
	RegistryExpr string

	Scalars []ScalarValidator
	Enums   []EnumValidator
	Types   []TypeValidator

	// CallsCore is true when a scalar validator asks the scalar core for
	// its verdict (coerce_lenient), ParsesCore when one runs a scalar's
	// parse and ValidatesJSON when one hands the core a value's JSON text;
	// validators.rs then defines the helper and the crate depends on the
	// scalar crate.
	CallsCore     bool
	ParsesCore    bool
	ValidatesJSON bool
}

// HasScalars reports whether validators.rs declares its scalars module.
func (v *ValidatorsInfo) HasScalars() bool { return len(v.Scalars) > 0 }

// ScalarValidator is validate_<scalar> and validate_<scalar>_required.
type ScalarValidator struct {
	Canonical string
	Fn        string
	Kind      string

	// TypeMessage is the message of the one type error for a value of
	// another JSON type.
	TypeMessage string

	// EmptyIsMissing is true for a scalar TypeScript holds as a plain
	// string: the required check takes "" for no value.
	EmptyIsMissing bool

	MinLength, MaxLength int
	// PatternStatic names the scalar's compiled pattern.
	PatternStatic, Pattern string

	ReservedWords                []string
	ReservedWordsCaseInsensitive bool
	ReservedWordsMatchPartial    bool

	IsInteger bool
	// Minimum and Maximum are the bounds as decimal integers, the form
	// their messages print, and MinimumF64 and MaximumF64 as f64 literals;
	// all empty when unset.
	Minimum, Maximum       string
	MinimumF64, MaximumF64 string

	// Structured is "object" or "array" for a JSON object or array scalar.
	Structured string

	// CoreValidate asks the scalar core once the rules pass; CoreParse runs
	// the scalar's parse on a JSON-shaped value. CoreValidateJSON hands an
	// any-JSON scalar's value to the core's validate as JSON text, as
	// superscalar's TypeScript binding validates Generic.JSON.
	CoreValidate     bool
	CoreParse        bool
	CoreValidateJSON bool
}

// HasRules reports whether the scalar's body checks its value against a
// rule of its own, and so needs it by name.
func (s ScalarValidator) HasRules() bool {
	switch s.Kind {
	case scalarString:
		return s.MinLength > 0 || s.MaxLength > 0 || s.Pattern != "" || len(s.ReservedWords) > 0
	case scalarNumber:
		return s.Minimum != "" || s.Maximum != ""
	}
	return false
}

// EnumValidator is validate_<enum> and validate_<enum>_required.
type EnumValidator struct {
	Name string
	Fn   string
	// RustPath is the enum's Rust type, whose ALL lists its values.
	RustPath string
}

// TypeValidator is validate_<type>, validate_<type>_required and
// parse_<type>.
type TypeValidator struct {
	Name     string
	Fn       string
	ParseFn  string
	FieldsID string
	RustPath string
	Strict   bool
	Fields   []FieldValidator
	Defaults []FieldDefault
	Patterns []PatternStatic
}

// FieldValidator is one field's checks in validate_<type>.
type FieldValidator struct {
	Name string
	Kind string

	Required, InternalMetadata      bool
	IsArray, IsArrayOfArrays, IsMap bool

	// Check is the validator a scalar or enum value runs, and Nested the
	// type validator of a nested object; both are Rust paths without the
	// _required suffix.
	Check  string
	Nested string

	// ElementRequired is whether a scalar or enum value inside the field
	// (an element, a map value) runs the required variant of Check.
	ElementRequired bool

	// Primitive is the runtime's type check of a builtin primitive field.
	Primitive string

	// RulesID names the field's own rules (fieldRules); empty for none.
	RulesID string
	Rules   []string
}

// HasChecks reports whether the field has any block in validate_<type>.
func (f FieldValidator) HasChecks(strict bool) bool {
	switch {
	case f.IsArray && !f.IsMap:
		return true
	case f.Kind == fieldScalar || f.Kind == fieldEnum || f.Kind == fieldPrimitive:
		return true
	case f.Kind == fieldObject || (f.Kind == fieldSelf && !strict):
		return true
	case f.Required && !f.InternalMetadata:
		return true
	}
	return f.RulesID != ""
}

// FieldDefault is a @default a parse fills in, as JSON text.
type FieldDefault struct {
	Name string
	JSON string
}

// PatternStatic is a compiled pattern of a field rule.
type PatternStatic struct {
	ID     string
	Source string
}

// validatorInputs is what buildValidators reads from Generate.
type validatorInputs struct {
	schema        *ir.Schema
	scalars       []codegen.ScalarInfo
	types         []codegen.TypeInfo
	localEnums    []codegen.EnumInfo
	importedEnums []codegen.EnumInfo
	enumLookup    codegen.EnumLookup
	naming        naming.Naming
}

// buildValidators builds the validators of every scalar the schema uses,
// every enum a type field holds, and every type the crate generates.
func buildValidators(in validatorInputs) *ValidatorsInfo {
	info := &ValidatorsInfo{
		RuntimeCrate: in.naming.SchemaRuntimeRustCrateIdent(),
		RegistryExpr: in.naming.ScalarRustRegistryExpr(),
	}

	scalarFns := map[string]string{}
	anyJSON := map[string]bool{}
	for _, s := range in.scalars {
		sv := buildScalarValidator(s, in.schema.Scalars[s.Name])
		info.Scalars = append(info.Scalars, sv)
		scalarFns[s.Name] = "scalars::" + sv.Fn
		anyJSON[s.Name] = s.Traits.IsAnyJSON
		info.CallsCore = info.CallsCore || sv.CoreValidate || sv.Kind == scalarStructured
		info.ParsesCore = info.ParsesCore || sv.CoreParse
		info.ValidatesJSON = info.ValidatesJSON || sv.CoreValidateJSON
	}

	enumFns := map[string]string{}
	for _, e := range in.localEnums {
		enumFns[e.Name] = validatorFnName(e.Name)
		info.Enums = append(info.Enums, EnumValidator{Name: e.Name, Fn: enumFns[e.Name], RustPath: "crate::enums::" + e.Name})
	}
	importedEnums := map[string]bool{}
	for _, e := range in.importedEnums {
		importedEnums[e.Name] = true
	}

	generated := map[string]bool{}
	for _, t := range in.types {
		generated[t.Name] = true
	}

	for _, t := range in.types {
		tv := TypeValidator{
			Name:     t.Name,
			Fn:       validatorFnName(t.Name),
			ParseFn:  "parse_" + codegen.ToSnakeCase(t.Name),
			FieldsID: constName(t.Name) + "_FIELDS",
			RustPath: "crate::types::" + t.Name,
			Strict:   t.StrictJSON,
		}
		rulesIDs := map[string]bool{}
		for _, f := range t.Fields {
			fv := FieldValidator{
				Name:             f.Name,
				Required:         f.Required,
				InternalMetadata: f.InternalMetadata,
				IsArray:          f.IsArray,
				IsArrayOfArrays:  f.IsArrayOfArrays,
				IsMap:            f.IsMap,
			}
			switch {
			case f.IsScalar && scalarFns[f.Type] != "":
				fv.Kind = fieldScalar
				fv.Check = scalarFns[f.Type]
			case in.enumLookup != nil && in.enumLookup(f.Type):
				fv.Kind = fieldEnum
				if enumFns[f.Type] == "" && importedEnums[f.Type] {
					// An imported enum is aliased in types.rs; its validator
					// is generated here from the alias.
					enumFns[f.Type] = validatorFnName(f.Type)
					info.Enums = append(info.Enums, EnumValidator{Name: f.Type, Fn: enumFns[f.Type], RustPath: "crate::types::" + f.Type})
				}
				fv.Check = enumFns[f.Type]
			case !f.IsScalar && f.Type == t.Name:
				fv.Kind = fieldSelf
				fv.Nested = tv.Fn
			case !f.IsScalar && generated[f.Type]:
				fv.Kind = fieldObject
				fv.Nested = validatorFnName(f.Type)
			case primitiveCheck(f) != "":
				fv.Kind = fieldPrimitive
				fv.Primitive = primitiveCheck(f)
			default:
				fv.Kind = fieldOther
			}
			if fv.Check == "" && (fv.Kind == fieldScalar || fv.Kind == fieldEnum) {
				fv.Kind = fieldOther
			}
			// A value inside a field holds the field's requiredness, except
			// that a map value of an any-JSON scalar may be null (tsgen's
			// mapScalarValueRequired and mapArrayElementRequired).
			fv.ElementRequired = f.Required && (!f.IsMap || !anyJSON[f.Type])

			rules := fieldRules(f)
			if len(rules) > 0 {
				fv.RulesID = uniqueID(constName(t.Name)+"_"+constName(f.Name)+"_RULES", rulesIDs)
				for _, rule := range rules {
					expr, pattern := ruleExpr(rule, fv.RulesID)
					if pattern != nil {
						tv.Patterns = append(tv.Patterns, *pattern)
					}
					fv.Rules = append(fv.Rules, expr)
				}
			}
			tv.Fields = append(tv.Fields, fv)

			if literal, ok := defaultJSON(f, in.enumLookup, in.localEnums, in.importedEnums); ok {
				tv.Defaults = append(tv.Defaults, FieldDefault{Name: f.Name, JSON: literal})
			}
		}
		info.Types = append(info.Types, tv)
	}
	return info
}

// buildScalarValidator ports tsgen's validator_scalar.tmpl for one scalar.
func buildScalarValidator(s codegen.ScalarInfo, def *ir.ScalarDef) ScalarValidator {
	symbol := strings.TrimSpace(s.Tokens.Symbol)
	if symbol == "" {
		symbol = s.Name
	}
	sv := ScalarValidator{
		Canonical:  s.Name,
		Fn:         validatorFnName(symbol),
		Kind:       scalarOpaque,
		Structured: s.Traits.StructuredJSON,
	}
	hasJSONParse := s.HasCustomParse && s.Traits.IsJSONLike
	tsType := typeScriptScalarType(s, def)
	sv.EmptyIsMissing = tsType == "string" || sv.Structured != ""

	switch {
	case sv.Structured != "":
		sv.Kind = scalarStructured
	case s.Primitive == ir.LanguageString && !hasJSONParse && !s.Traits.IsAnyJSON:
		sv.Kind = scalarString
		sv.TypeMessage = "expected string value"
		if tsType == "JSDate" {
			sv.TypeMessage = "expected a date-time string"
		}
		sv.MinLength, sv.MaxLength = s.MinLength, s.MaxLength
		if s.Pattern != "" {
			sv.Pattern = s.Pattern
			sv.PatternStatic = constName(symbol) + "_PATTERN"
		}
		sv.ReservedWordsCaseInsensitive = s.ReservedWordsCaseInsensitive
		sv.ReservedWordsMatchPartial = s.ReservedWordsMatchPartial
		for _, word := range s.ReservedWords {
			if s.ReservedWordsCaseInsensitive {
				word = strings.ToLower(word)
			}
			sv.ReservedWords = append(sv.ReservedWords, word)
		}
	case s.Primitive == ir.LanguageNumber:
		sv.Kind = scalarNumber
		sv.IsInteger = s.Traits.IsIntegerLike
		sv.TypeMessage = "must be a number"
		if sv.IsInteger {
			sv.TypeMessage = "must be an integer"
		}
		if s.Minimum != nil {
			sv.Minimum = strconv.FormatInt(*s.Minimum, 10)
			sv.MinimumF64 = sv.Minimum + "_f64"
		}
		if s.Maximum != nil {
			sv.Maximum = strconv.FormatInt(*s.Maximum, 10)
			sv.MaximumF64 = sv.Maximum + "_f64"
		}
	}
	sv.CoreValidateJSON = s.HasCustomValidate && s.Traits.IsAnyJSON
	sv.CoreValidate = s.HasCustomValidate && sv.Structured == "" && !sv.CoreValidateJSON
	sv.CoreParse = hasJSONParse && !s.HasCustomValidate && sv.Structured == ""
	return sv
}

// typeScriptScalarType is the type tsgen gives the scalar (its TSType): the
// scalar's typescript mapping, else the type inferTSType gives its
// primitive, and JSDate for a date-time scalar whatever its mapping.
func typeScriptScalarType(s codegen.ScalarInfo, def *ir.ScalarDef) string {
	if s.Traits.IsDateTimeLike {
		return "JSDate"
	}
	if def != nil {
		if mapping, ok := def.TypeMappings["typescript"]; ok && mapping != "" {
			return mapping
		}
	}
	traits := codegen.GuessScalarTraits(s.Name)
	switch s.Primitive {
	case ir.LanguageString:
		if traits.IsDateTimeLike {
			return "JSDate"
		}
		if traits.IsJSONLike || traits.IsObjectLike || traits.IsLocationLike {
			return "object"
		}
		return "string"
	case ir.LanguageNumber:
		return "number"
	case ir.LanguageBoolean:
		return "boolean"
	}
	return "object"
}

// primitiveCheck is the runtime's type check of a field typed with a
// builtin primitive (tsgen's primitiveCheck), or "" for any other field.
func primitiveCheck(f codegen.FieldInfo) string {
	if f.IsScalar {
		return ""
	}
	switch f.Type {
	case codegen.PrimitiveString:
		return "expect_string"
	case codegen.PrimitiveNumber:
		return "expect_number"
	case codegen.PrimitiveBoolean:
		return "expect_boolean"
	}
	return ""
}

// fieldRules returns the field's own @validate rules that can fail for its
// type (tsgen's fieldRules and ruleApplies): not the required rule, and not
// the rules copied from its scalar, which the scalar validator checks.
func fieldRules(f codegen.FieldInfo) []codegen.ValidationRule {
	var rules []codegen.ValidationRule
	for _, rule := range f.Validations {
		if rule.Validator == "required" || rule.FromScalar || !ruleApplies(f, rule.Validator) {
			continue
		}
		rules = append(rules, rule)
	}
	return rules
}

// ruleApplies reports whether a rule can fail for a value of the field's
// type: a builtin primitive's value of another JSON type is "type", so a
// string rule applies to a string field only and a range rule to a number
// field only.
func ruleApplies(f codegen.FieldInfo, validator string) bool {
	stringRule := validator == "minLength" || validator == "maxLength" || validator == "pattern"
	rangeRule := validator == "min" || validator == "max"
	switch primitiveCheck(f) {
	case "expect_string":
		return !rangeRule
	case "expect_number":
		return !stringRule
	case "expect_boolean":
		return !stringRule && !rangeRule
	}
	return true
}

// ruleExpr renders one rule as a runtime Rule, with the static its pattern
// compiles into.
func ruleExpr(rule codegen.ValidationRule, rulesID string) (string, *PatternStatic) {
	message := rustString(rule.Message)
	switch rule.Validator {
	case "minLength":
		return fmt.Sprintf("rt::Rule::MinLength(%d, %s)", intValue(rule.Value), message), nil
	case "maxLength":
		return fmt.Sprintf("rt::Rule::MaxLength(%d, %s)", intValue(rule.Value), message), nil
	case "listMin":
		return fmt.Sprintf("rt::Rule::ListMin(%d, %s)", intValue(rule.Value), message), nil
	case "listMax":
		return fmt.Sprintf("rt::Rule::ListMax(%d, %s)", intValue(rule.Value), message), nil
	case "min":
		return fmt.Sprintf("rt::Rule::Min(%s, %s)", f64Literal(floatValue(rule.Value)), message), nil
	case "max":
		return fmt.Sprintf("rt::Rule::Max(%s, %s)", f64Literal(floatValue(rule.Value)), message), nil
	case "pattern":
		pattern := &PatternStatic{ID: strings.TrimSuffix(rulesID, "_RULES") + "_PATTERN", Source: fmt.Sprint(rule.Value)}
		return fmt.Sprintf("rt::Rule::Pattern(&%s, %s)", pattern.ID, message), pattern
	}
	return "", nil
}

// defaultJSON is the field's @default as JSON text, for a parse to fill in
// when the key is missing: the literals tsgen and rustgen render, as JSON.
func defaultJSON(f codegen.FieldInfo, enumLookup codegen.EnumLookup, enumSets ...[]codegen.EnumInfo) (string, bool) {
	if f.Default == nil {
		return "", false
	}
	raw := *f.Default
	trimmed := strings.TrimSpace(raw)
	switch codegen.ClassifyDefault(f, enumLookup) {
	case codegen.DefaultLiteralBool:
		switch strings.ToLower(trimmed) {
		case "true", "false":
			return strings.ToLower(trimmed), true
		}
	case codegen.DefaultLiteralInt:
		if _, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
			return trimmed, true
		}
	case codegen.DefaultLiteralFloat:
		if v, err := strconv.ParseFloat(trimmed, 64); err == nil {
			return strconv.FormatFloat(v, 'g', -1, 64), true
		}
	case codegen.DefaultLiteralString:
		return strconv.Quote(raw), true
	case codegen.DefaultLiteralEnum:
		for _, enums := range enumSets {
			for _, e := range enums {
				if e.Name != f.Type {
					continue
				}
				for _, v := range e.Values {
					if v.Name == raw || v.Value == raw {
						return strconv.Quote(v.Value), true
					}
				}
			}
		}
	case codegen.DefaultLiteralEmptyArray:
		return "[]", true
	}
	return "", false
}

// validatorFnName is validate_<name> in snake case.
func validatorFnName(name string) string {
	return "validate_" + codegen.ToSnakeCase(name)
}

// constName is name as a Rust constant: SCREAMING_SNAKE_CASE of its
// letters and digits.
func constName(name string) string {
	snake := codegen.ToSnakeCase(name)
	var b strings.Builder
	for _, r := range strings.ToUpper(snake) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" || (out[0] >= '0' && out[0] <= '9') {
		out = "F_" + out
	}
	return out
}

// uniqueID returns id, or id with a numeric suffix when taken.
func uniqueID(id string, taken map[string]bool) string {
	candidate := id
	for i := 2; taken[candidate]; i++ {
		candidate = fmt.Sprintf("%s_%d", id, i)
	}
	taken[candidate] = true
	return candidate
}

// f64Literal renders v as a Rust f64 literal, in fixed notation so an
// integral value reads as one.
func f64Literal(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64) + "_f64"
}

// fieldChecksData is what the fieldChecks template renders: one field and
// whether its type is @strictJSON.
type fieldChecksData struct {
	Field  FieldValidator
	Strict bool
}

func fieldData(f FieldValidator, t TypeValidator) fieldChecksData {
	return fieldChecksData{Field: f, Strict: t.Strict}
}

func intValue(v any) int64 {
	switch n := v.(type) {
	case int:
		return int64(n)
	case int64:
		return n
	case float64:
		return int64(n)
	}
	return 0
}

func floatValue(v any) float64 {
	switch n := v.(type) {
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case float64:
		return n
	}
	return 0
}

// rustStrings renders values as the items of a Rust string slice literal.
func rustStrings(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = rustString(value)
	}
	return strings.Join(quoted, ", ")
}

// fieldNames renders the fields' wire names as the items of a Rust string
// slice literal.
func fieldNames(fields []FieldValidator) string {
	names := make([]string, len(fields))
	for i, f := range fields {
		names[i] = f.Name
	}
	return rustStrings(names)
}

// rustString renders s as a Rust string literal.
func rustString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case 0:
			b.WriteString(`\0`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u{%x}`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
