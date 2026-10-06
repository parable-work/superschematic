package rustrestgen

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/rustapigen"
	"github.com/parable-work/superschematic/internal/generator/rustutil"
	ir "github.com/parable-work/superschematic/ir"
)

// ParamInfo is one decoded argument of an operation: a field of its Args
// struct, and the ParamSpec static the router decodes it with.
type ParamInfo struct {
	// Name is the wire name (the schema argument name); Field is the Args
	// struct's field.
	Name  string
	Field string
	// RustType is the field's type: the element type in its Vec levels,
	// inside a HashMap for a map, and in Option for an optional argument.
	RustType string
	// Spec is the name of the ParamSpec static; SpecExpr builds it.
	Spec     string
	SpecExpr string
	// KeepNull marks an optional single Generic.JSON body argument, whose
	// null is a value apart from absent: its field is an
	// Option<serde_json::Value> holding Some(Value::Null) for null.
	KeepNull bool
}

// InputInfo is an operation's input type, which travels as the request
// body and is parsed by its type's generated parse_<type>.
type InputInfo struct {
	RustType string // types::<Input>, in Option when the input is optional
	Parse    string // the parse function, or a closure that only decodes
	// Prepare is the type's prepare_<type>, which the Args struct's check
	// runs on an input a caller built; empty for a type its crate emits
	// without validators, which has no rules to check.
	Prepare  string
	Required bool
}

// PatternStatic is a compiled pattern the router's parameter specs share.
type PatternStatic struct {
	Name   string
	Source string
}

// DependencyCrate is a dependency's generated types crate the router names
// directly, for the validators of a type that dependency declares.
type DependencyCrate struct {
	Name string
	Path string
}

// validatedRoles are the type roles a generated Rust types crate emits
// with validators (rustgen's localObjectRoles and its inputs): a type of
// one of them has a parse_<type> and a prepare_<type>.
var validatedRoles = map[ir.Role]bool{
	ir.RoleDBTable:        true,
	ir.RoleAPIView:        true,
	ir.RoleEmbeddedStruct: true,
	ir.RoleAPIInput:       true,
}

// builder resolves each argument's kind, Rust type and spec against the
// schema and its dependencies, as tsrestgen's builder does for the
// TypeScript router.
type builder struct {
	schema   *ir.Schema
	deps     map[string]*ir.Schema
	depNames []string
	naming   naming.Naming
	// typesDir and outputDir locate a dependency's types crate, a sibling
	// of the schema's own (TypesDir), from the API crate.
	typesDir  string
	outputDir string

	specNames     map[string]bool
	patterns      []PatternStatic
	patternByKey  map[string]string
	depCrates     map[string]DependencyCrate
	usesSchemaRef bool
}

func newBuilder(schema *ir.Schema, opts Options) *builder {
	depNames := make([]string, 0, len(opts.Dependencies))
	for name := range opts.Dependencies {
		depNames = append(depNames, name)
	}
	sort.Strings(depNames)
	return &builder{
		schema:       schema,
		deps:         opts.Dependencies,
		depNames:     depNames,
		naming:       opts.Naming.OrDefault(),
		typesDir:     opts.TypesDir,
		outputDir:    opts.OutputDir,
		specNames:    map[string]bool{},
		patternByKey: map[string]string{},
		depCrates:    map[string]DependencyCrate{},
	}
}

// dependencyCrates are the dependency types crates the router names, by
// crate name.
func (b *builder) dependencyCrates() []DependencyCrate {
	crates := make([]DependencyCrate, 0, len(b.depCrates))
	for _, crate := range b.depCrates {
		crates = append(crates, crate)
	}
	sort.Slice(crates, func(i, j int) bool { return crates[i].Name < crates[j].Name })
	return crates
}

// paramPlace is where an argument travels.
type paramPlace int

const (
	inPath paramPlace = iota
	inQuery
	inBody
)

// params fills the endpoint's arguments, input and output from apigen's
// endpoint. Undecorated scalar arguments travel in the query string on GET
// (the Go router's rule) and as fields of the JSON body object otherwise.
func (b *builder) params(ep apigen.EndpointInfo, info *EndpointInfo) error {
	prefix := rustutil.ToSnakeCase(info.Namespace) + "_" + info.FunctionName
	fields := map[string]string{}
	add := func(p apigen.Param, place paramPlace) (ParamInfo, error) {
		param, err := b.param(p, place, prefix)
		switch {
		case err != nil && p.IsArrayOfArrays:
			return param, fmt.Errorf("rustrestgen does not support this list of lists (%s.%s(%s)): %w", ep.Namespace, ep.Name, p.Name, err)
		case err != nil:
			return param, fmt.Errorf("rustrestgen cannot decode argument %s.%s(%s): %w", ep.Namespace, ep.Name, p.Name, err)
		}
		if other, taken := fields[param.Field]; taken {
			return param, fmt.Errorf("rustrestgen: operation %s.%s arguments %s and %s are both the Rust field %s", ep.Namespace, ep.Name, other, p.Name, param.Field)
		}
		fields[param.Field] = p.Name
		return param, nil
	}
	if ep.HasInput {
		fields["input"] = "its input"
	}
	for _, p := range ep.PathParams {
		param, err := add(p, inPath)
		if err != nil {
			return err
		}
		info.PathArgs = append(info.PathArgs, param)
	}
	for _, p := range ep.QueryParams {
		param, err := add(p, inQuery)
		if err != nil {
			return err
		}
		info.QueryArgs = append(info.QueryArgs, param)
	}
	scalarPlace := inBody
	if strings.EqualFold(ep.Method, "GET") {
		scalarPlace = inQuery
	}
	for _, p := range ep.ScalarArgs {
		param, err := add(p, scalarPlace)
		if err != nil {
			return err
		}
		if scalarPlace == inQuery {
			info.QueryArgs = append(info.QueryArgs, param)
		} else {
			info.BodyArgs = append(info.BodyArgs, param)
			info.BodyRequired = info.BodyRequired || p.Required
		}
	}

	if ep.HasInput {
		input, err := b.input(ep.InputType, ep.InputRequired)
		if err != nil {
			return fmt.Errorf("rustrestgen: operation %s.%s: %w", ep.Namespace, ep.Name, err)
		}
		info.Input = input
	}
	output, err := b.outputType(ep)
	if err != nil {
		return fmt.Errorf("rustrestgen: operation %s.%s: %w", ep.Namespace, ep.Name, err)
	}
	info.OutputRustType = output
	return nil
}

// param resolves the kind, the Rust type and the ParamSpec of one argument
// from apigen's parse flags. A body argument arrives as a JSON value, so its
// type may also be an object type, alone, in a list, in a list of lists or
// as a map value; the router prepares each value with the generated
// prepare_<type> of the type. A body argument of a JSON-valued scalar
// (Generic.JSON) is taken as the JSON value it is. param fails for a body
// argument of any other type that is not a scalar or an enum: a union has
// no prepare_<type>. A scalar-typed argument carries the scalar's own
// lengths, pattern and range, which the router checks on every value.
func (b *builder) param(p apigen.Param, place paramPlace, prefix string) (ParamInfo, error) {
	info := ParamInfo{
		Name:  p.Name,
		Field: rustutil.Identifier(p.Name, "value"),
	}
	required := p.Required || place == inPath
	if p.IsMap && place != inBody {
		// apigen refuses a map outside the body; the query string and the
		// path have no encoding for one.
		return info, fmt.Errorf("a map travels only in the request body")
	}

	var kind, elem string
	var enumValues []string
	var calls []string
	scalar, isScalar := b.findScalar(p.Type)
	scalarType := func(fallback string) string {
		if isScalar {
			return "types::" + scalarSymbol(p.Type)
		}
		return fallback
	}
	switch {
	case p.IsInt:
		kind, elem = "Integer", scalarType("i64")
	case p.IsFloat:
		kind, elem = "Number", scalarType("f64")
	case p.IsBool:
		kind, elem = "Boolean", scalarType("bool")
	case p.IsUUID:
		kind, elem = "Uuid", scalarType("String")
		calls = append(calls, ".check(types::validators::scalars::validate_"+codegen.ToSnakeCase(scalarSymbol(p.Type))+")")
	case p.IsDateTime:
		kind, elem = "DateTime", scalarType("String")
		calls = append(calls, ".check(types::validators::scalars::validate_"+codegen.ToSnakeCase(scalarSymbol(p.Type))+")")
	case p.IsString:
		kind, elem = "String", "String"
	default:
		if enumDef, ok := b.findEnum(p.Type); ok {
			kind, elem = "Enum", "types::"+enumDef.Name
			for _, value := range enumDef.Values {
				serialized := value.SerializedAs
				if serialized == "" {
					serialized = value.Name
				}
				enumValues = append(enumValues, serialized)
			}
		} else if isScalar || place != inBody {
			kind, elem = "String", scalarType("String")
			if place == inBody && isJSONValued(scalar) {
				kind = "Json"
			}
			if place == inBody && scalar != nil {
				switch scalar.StructuredJSONType() {
				case ir.JSONSchemaObjectType:
					kind = "JsonObject"
				case ir.JSONSchemaArrayType:
					kind = "JsonArray"
				}
			}
		} else {
			validators, ok := b.validatorsOf(p.Type)
			if !ok && !b.declares(p.Type) {
				what := "type"
				if p.IsArray {
					what = "element type"
				}
				return info, fmt.Errorf("%s %s is not a scalar, enum or object type", what, p.Type)
			}
			kind, elem = "Object", "types::"+p.Type
			if ok {
				calls = append(calls, ".prepare("+validators+"::prepare_"+codegen.ToSnakeCase(p.Type)+")")
			}
		}
	}

	rustType := rustListType(elem, p.ArrayDepth())
	if p.IsMap {
		rustType = "std::collections::HashMap<String, " + rustType + ">"
	}
	info.KeepNull = kind == "Json" && place == inBody && !required && !p.IsArray && !p.IsMap
	switch {
	case info.KeepNull:
		rustType = "Option<serde_json::Value>"
	case !required:
		rustType = rustutil.WrapOptionalType(rustType)
	}
	info.RustType = rustType

	info.Spec = b.specName(prefix + "_" + p.Name)
	spec := []string{fmt.Sprintf("ParamSpec::new(%s, ParamKind::%s)", rustString(p.Name), kind)}
	if required {
		spec = append(spec, ".required()")
	}
	switch {
	case p.IsArrayOfArrays:
		spec = append(spec, ".array_of_arrays()")
	case p.IsArray:
		spec = append(spec, ".array()")
	}
	if p.IsMap {
		spec = append(spec, ".map()")
	}
	if len(enumValues) > 0 {
		literals := make([]string, len(enumValues))
		for i, value := range enumValues {
			literals[i] = rustString(value)
		}
		spec = append(spec, ".enum_values(&["+strings.Join(literals, ", ")+"])")
	}
	if p.DefaultValue != nil {
		spec = append(spec, ".default_value("+rustString(*p.DefaultValue)+")")
	}
	if p.ValidateMin != nil {
		spec = append(spec, ".min("+floatLiteral(*p.ValidateMin)+")")
	}
	if p.ValidateMax != nil {
		spec = append(spec, ".max("+floatLiteral(*p.ValidateMax)+")")
	}
	if p.ValidateMinLength != nil {
		spec = append(spec, fmt.Sprintf(".min_length(%d)", *p.ValidateMinLength))
	}
	if p.ValidateMaxLength != nil {
		spec = append(spec, fmt.Sprintf(".max_length(%d)", *p.ValidateMaxLength))
	}
	// List bounds bound a list, not a map or its lists, as in the generated
	// types and the Go router.
	if p.ValidateListMin != nil && !p.IsMap {
		spec = append(spec, fmt.Sprintf(".list_min(%d)", *p.ValidateListMin))
	}
	if p.ValidateListMax != nil && !p.IsMap {
		spec = append(spec, fmt.Sprintf(".list_max(%d)", *p.ValidateListMax))
	}
	if p.ValidatePattern != "" {
		spec = append(spec, ".pattern(&"+b.pattern(info.Spec+"_PATTERN", p.ValidatePattern)+")")
	}
	if isScalar {
		if constraints := b.scalarConstraints(scalar, p.Type, kind); constraints != "" {
			spec = append(spec, ".scalar("+constraints+")")
		}
	}
	spec = append(spec, calls...)
	info.SpecExpr = strings.Join(spec, "")
	return info, nil
}

// scalarConstraints renders the ScalarConstraints of a scalar's own lengths
// and pattern (kind String) or range (kinds Integer and Number), or "" when
// the scalar has none for the kind. UUID and timestamp kinds are checked by
// the scalar's validator instead.
func (b *builder) scalarConstraints(scalar *ir.ScalarDef, name, kind string) string {
	var calls []string
	switch kind {
	case "String":
		if scalar.MinLength > 0 {
			calls = append(calls, fmt.Sprintf(".min_length(%d)", scalar.MinLength))
		}
		if scalar.MaxLength > 0 {
			calls = append(calls, fmt.Sprintf(".max_length(%d)", scalar.MaxLength))
		}
		if scalar.Pattern != "" {
			static := b.pattern(constName(scalarSymbol(name))+"_PATTERN", scalar.Pattern)
			calls = append(calls, ".pattern(&"+static+")")
		}
	case "Integer", "Number":
		if scalar.Minimum != nil {
			calls = append(calls, ".min("+floatLiteral(float64(*scalar.Minimum))+")")
		}
		if scalar.Maximum != nil {
			calls = append(calls, ".max("+floatLiteral(float64(*scalar.Maximum))+")")
		}
	}
	if len(calls) == 0 {
		return ""
	}
	return "ScalarConstraints::new(" + rustString(name) + ")" + strings.Join(calls, "")
}

// input resolves an operation's input type and its parse function.
func (b *builder) input(typeName string, required bool) (*InputInfo, error) {
	if !b.declares(typeName) {
		return nil, fmt.Errorf("input type %s is not declared by the schema or its dependencies", typeName)
	}
	input := &InputInfo{RustType: "types::" + typeName, Required: required}
	if !required {
		input.RustType = rustutil.WrapOptionalType(input.RustType)
	}
	if validators, ok := b.validatorsOf(typeName); ok {
		input.Parse = validators + "::parse_" + codegen.ToSnakeCase(typeName)
		input.Prepare = validators + "::prepare_" + codegen.ToSnakeCase(typeName)
	} else {
		// A type its crate emits without validators is only decoded.
		b.usesSchemaRef = true
		input.Parse = "|value, _| serde_json::from_value(value).map_err(schema::ParseError::Decode)"
	}
	return input, nil
}

// outputType is the Rust type of an operation's result: () without one, a
// primitive, a scalar's alias or a declared type, in its Vec levels.
func (b *builder) outputType(ep apigen.EndpointInfo) (string, error) {
	name := ep.OutputType
	var elem string
	switch {
	case name == "":
		return "()", nil
	case codegen.IsLanguagePrimitive(name):
		mapped, ok := rustutil.PrimitiveToRustType(name)
		if !ok {
			return "", fmt.Errorf("output type %s has no Rust type", name)
		}
		elem = mapped
	default:
		if _, ok := b.findScalar(name); ok {
			elem = "types::" + scalarSymbol(name)
		} else if b.declares(name) {
			elem = "types::" + name
		} else {
			return "", fmt.Errorf("output type %s is not declared by the schema or its dependencies", name)
		}
	}
	return rustListType(elem, ep.OutputArrayDepth()), nil
}

// declares reports whether the schema or a dependency declares the type,
// enum or union name, which the schema's types crate then names.
func (b *builder) declares(name string) bool {
	for _, schema := range b.schemas() {
		if schema.Types[name] != nil || schema.Enums[name] != nil || schema.Unions[name] != nil {
			return true
		}
	}
	return false
}

// validatorsOf is the path of the validators module that holds the type's
// prepare_<type> and parse_<type>: the schema's types crate's for its own
// type, a dependency's types crate's for one the dependency declares, which
// the API crate then depends on. ok is false for a type no crate emits with
// validators.
func (b *builder) validatorsOf(name string) (string, bool) {
	if typeDef := b.schema.Types[name]; typeDef != nil {
		return "types::validators", hasValidators(typeDef)
	}
	for _, depName := range b.depNames {
		dep := b.deps[depName]
		if dep == nil || dep.Types[name] == nil {
			continue
		}
		if !hasValidators(dep.Types[name]) {
			return "", false
		}
		crate := b.naming.RustTypesCrate(depName)
		b.depCrates[crate] = DependencyCrate{
			Name: crate,
			Path: rustapigen.ResolveTypesDependencyPath(b.outputDir, filepath.Join(filepath.Dir(b.typesDir), depName)),
		}
		return strings.ReplaceAll(crate, "-", "_") + "::validators", true
	}
	return "", false
}

// hasValidators reports whether a types crate emits the type with
// validators.
func hasValidators(typeDef *ir.TypeDef) bool {
	return !typeDef.IsTrait && validatedRoles[typeDef.Role]
}

func (b *builder) schemas() []*ir.Schema {
	schemas := []*ir.Schema{b.schema}
	for _, depName := range b.depNames {
		if dep := b.deps[depName]; dep != nil {
			schemas = append(schemas, dep)
		}
	}
	return schemas
}

func (b *builder) findEnum(name string) (*ir.EnumDef, bool) {
	for _, schema := range b.schemas() {
		if enumDef, ok := schema.Enums[name]; ok {
			return enumDef, true
		}
	}
	return nil, false
}

func (b *builder) findScalar(name string) (*ir.ScalarDef, bool) {
	for _, schema := range b.schemas() {
		if scalar, ok := schema.Scalars[name]; ok {
			return scalar, true
		}
	}
	return nil, false
}

// specName is a unique SCREAMING_SNAKE_CASE static name.
func (b *builder) specName(name string) string {
	base := constName(name)
	candidate := base
	for i := 2; b.specNames[candidate]; i++ {
		candidate = fmt.Sprintf("%s_%d", base, i)
	}
	b.specNames[candidate] = true
	return candidate
}

// pattern is the static compiling source, named name unless a static with
// the same name and source exists.
func (b *builder) pattern(name, source string) string {
	key := name + "\x00" + source
	if static, ok := b.patternByKey[key]; ok {
		return static
	}
	static := b.specName(name)
	b.patternByKey[key] = static
	b.patterns = append(b.patterns, PatternStatic{Name: static, Source: source})
	return static
}

// isJSONValued reports whether a scalar holds any JSON value
// (Generic.JSON), as tsrestgen reads it.
func isJSONValued(scalar *ir.ScalarDef) bool {
	return scalar != nil && scalar.TypeMappings["typescript"] == "JSONValue"
}

// scalarSymbol is a scalar's alias in the types crate: Identity.UUID is
// IdentityUUID.
func scalarSymbol(name string) string {
	if symbol := strings.TrimSpace(codegen.BuildScalarTokens(name).Symbol); symbol != "" {
		return symbol
	}
	return name
}

// rustListType wraps a Rust element type in depth Vec levels: "T",
// "Vec<T>" or "Vec<Vec<T>>".
func rustListType(elem string, depth int) string {
	return codegen.WrapArray(elem, depth, func(inner string) string { return "Vec<" + inner + ">" })
}

// floatLiteral renders a bound as a Rust f64 literal, which needs a
// fraction or an exponent.
func floatLiteral(value float64) string {
	formatted := strconv.FormatFloat(value, 'f', -1, 64)
	if !strings.Contains(formatted, ".") {
		formatted += ".0"
	}
	return formatted
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
	return rustutil.CollapseUnderscores(strings.Trim(b.String(), "_"))
}
