package apigen

import (
	"fmt"
	"strconv"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// BodyArg is how the Go route decodes one body argument: an argument of an
// operation that is not GET and has no input type, other than a path or
// query parameter. The route reads the body as one JSON object and hands
// each argument's JSON value to the HTTP runtime's bodyargs package, which
// applies the list rules and the value rules and names each failure by its
// path and rule.
type BodyArg struct {
	Param
	// Kind is the bodyargs Kind of each value: String, Number, Integer,
	// Boolean, Object, Array or Any.
	Kind string
	// Options are the bodyargs options after the kind: Required, the list
	// bounds, then the rules of the value's scalar type and the argument's
	// own rules, in the order the runtime checks them.
	Options []string
}

// Decoder is the bodyargs function that decodes the argument: Value, List,
// ListOfLists, Map or MapOfLists.
func (a BodyArg) Decoder() string {
	if a.IsMap {
		if a.IsArray {
			return "MapOfLists"
		}
		return "Map"
	}
	switch a.ArrayDepth() {
	case 2:
		return "ListOfLists"
	case 1:
		return "List"
	default:
		return "Value"
	}
}

// NewArg is the Go expression that builds the argument's bodyargs.Arg.
func (a BodyArg) NewArg() string {
	parts := append([]string{strconv.Quote(a.Name), "bodyargs." + a.Kind}, a.Options...)
	return "bodyargs.NewArg(" + strings.Join(parts, ", ") + ")"
}

// bodyArgs describes how the route decodes each body argument.
func (m *typeMapper) bodyArgs(args []Param) []BodyArg {
	if len(args) == 0 {
		return nil
	}
	out := make([]BodyArg, 0, len(args))
	for _, arg := range args {
		kind := m.bodyArgKind(arg)
		var options []string
		if arg.Required {
			options = append(options, "bodyargs.Required()")
		}
		// List bounds bound a list argument; as in the generated types, a
		// map has none.
		if arg.IsArray && !arg.IsMap && arg.ValidateListMin != nil {
			options = append(options, fmt.Sprintf("bodyargs.ListMin(%d)", *arg.ValidateListMin))
		}
		if arg.IsArray && !arg.IsMap && arg.ValidateListMax != nil {
			options = append(options, fmt.Sprintf("bodyargs.ListMax(%d)", *arg.ValidateListMax))
		}
		if scalarDef, ok := m.findScalarDef(arg.Type); ok {
			options = append(options, scalarRuleOptions(scalarDef, kind)...)
		}
		options = append(options, argRuleOptions(arg, kind)...)
		out = append(out, BodyArg{Param: arg, Kind: kind, Options: options})
	}
	return out
}

// bodyArgKind is the JSON type of each value of arg. A builtin, and a
// scalar the route decodes as a Go number or boolean, follow their Go
// type; any other scalar follows its JSON Schema type, so Generic.JSON
// ("any") takes any JSON value. An enum is a string, an object type an
// object, and anything else (a union) is left to its Go decoder.
func (m *typeMapper) bodyArgKind(arg Param) string {
	switch {
	case arg.IsInt:
		return "Integer"
	case arg.IsFloat:
		return "Number"
	case arg.IsBool:
		return "Boolean"
	case arg.IsString, arg.IsUUID, arg.IsDateTime:
		return "String"
	}
	if scalarDef, ok := m.findScalarDef(arg.Type); ok {
		switch scalarDef.TypeMappings["json_schema"] {
		case "string":
			return "String"
		case "integer":
			return "Integer"
		case "number":
			return "Number"
		case "boolean":
			return "Boolean"
		case "object":
			return "Object"
		case "array":
			return "Array"
		case "":
			switch scalarDef.LanguagePrimitive {
			case ir.LanguageString:
				return "String"
			case ir.LanguageNumber:
				return "Number"
			case ir.LanguageBoolean:
				return "Boolean"
			}
		}
		return "Any"
	}
	if m.isEnum(arg.Type) {
		return "String"
	}
	if _, ok := m.findTypeDef(arg.Type); ok {
		return "Object"
	}
	return "Any"
}

// isEnum reports whether name is an enum of the schema or a dependency.
func (m *typeMapper) isEnum(name string) bool {
	if _, ok := m.schema.Enums[name]; ok {
		return true
	}
	for _, dep := range m.dependencies {
		if dep == nil {
			continue
		}
		if _, ok := dep.Enums[name]; ok {
			return true
		}
	}
	return false
}

// scalarRuleOptions are the scalar's own constraints that fit kind: its
// lengths and pattern for a string, its range for a number. The runtime
// checks them before the argument's own and names a failure by its rule
// (D14).
func scalarRuleOptions(scalarDef *ir.ScalarDef, kind string) []string {
	var options []string
	switch kind {
	case "String":
		if scalarDef.MinLength > 0 {
			options = append(options, fmt.Sprintf("bodyargs.MinLength(%d)", scalarDef.MinLength))
		}
		if scalarDef.MaxLength > 0 {
			options = append(options, fmt.Sprintf("bodyargs.MaxLength(%d)", scalarDef.MaxLength))
		}
		if scalarDef.Pattern != "" {
			options = append(options, "bodyargs.Pattern("+goStringLiteral(scalarDef.Pattern)+")")
		}
	case "Integer", "Number":
		if scalarDef.Minimum != nil {
			options = append(options, fmt.Sprintf("bodyargs.Min(%d)", *scalarDef.Minimum))
		}
		if scalarDef.Maximum != nil {
			options = append(options, fmt.Sprintf("bodyargs.Max(%d)", *scalarDef.Maximum))
		}
	}
	return options
}

// argRuleOptions are the argument's own Validate<> constraints that fit
// kind; a list argument applies them to every element.
func argRuleOptions(arg Param, kind string) []string {
	var options []string
	switch kind {
	case "String":
		if arg.ValidateMinLength != nil {
			options = append(options, fmt.Sprintf("bodyargs.MinLength(%d)", *arg.ValidateMinLength))
		}
		if arg.ValidateMaxLength != nil {
			options = append(options, fmt.Sprintf("bodyargs.MaxLength(%d)", *arg.ValidateMaxLength))
		}
		if arg.ValidatePattern != "" {
			options = append(options, "bodyargs.Pattern("+goStringLiteral(arg.ValidatePattern)+")")
		}
	case "Integer", "Number":
		if arg.ValidateMin != nil {
			options = append(options, "bodyargs.Min("+strconv.FormatFloat(*arg.ValidateMin, 'g', -1, 64)+")")
		}
		if arg.ValidateMax != nil {
			options = append(options, "bodyargs.Max("+strconv.FormatFloat(*arg.ValidateMax, 'g', -1, 64)+")")
		}
	}
	return options
}

// goStringLiteral writes s as a raw string literal when it can be one, so
// a pattern reads as the schema wrote it, and as a quoted one otherwise.
func goStringLiteral(s string) string {
	for _, r := range s {
		if r == '`' || r < ' ' || r > '~' {
			return strconv.Quote(s)
		}
	}
	return "`" + s + "`"
}
