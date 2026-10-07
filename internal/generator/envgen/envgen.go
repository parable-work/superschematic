// Package envgen generates typed environment-variable loaders from schema
// config classes marked @envVars. It discovers the @envVars type in the IR
// and emits config.go (or src/config.rs for Rust services) plus a
// machine-readable values-schema.json env var manifest.
package envgen

import (
	"embed"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/ir"
)

//go:embed templates/*.tmpl
var templatesFS embed.FS

// ConfigOutput contains all generated configuration code.
type ConfigOutput struct {
	// PackageName is the Go package name (derived from schema name).
	PackageName string
	// SchemaName is the name of the schema being processed.
	SchemaName string
	// TypeName is the name of the @envVars type found in the schema.
	TypeName string
	// ModulePath is the Go module path for the generated config.
	ModulePath string
	// TypesModule is the Go module path for the types package.
	TypesModule string
	// IndirectModules are the Go types modules TypesModule imports,
	// directly or through one another, sorted; the dispatch layer sets them
	// before WriteConfigModule, whose go.mod requires them as indirect and
	// replaces them.
	IndirectModules []string
	// Naming supplies the scalar library and schema IR module paths the
	// standalone module's go.mod replaces.
	Naming naming.Naming
	// ScalarLibReplacePath and SchemaIRReplacePath are the go.mod replace
	// targets computed by SetReplacePaths. Empty values omit the directive.
	ScalarLibReplacePath string
	SchemaIRReplacePath  string
	// Pins are the runtime modules the standalone module's go.mod takes
	// from the module proxy, at the release that generates it
	// (SetReleasePins).
	Pins naming.Pins
	// Fields are the environment variable fields to generate.
	Fields []ConfigField
	// Enums are enum types used by the config fields.
	Enums []codegen.EnumInfo
	// HasEnums indicates if any field uses an enum type.
	HasEnums bool
	// HasScalars indicates if any field uses a custom scalar type.
	HasScalars bool
	// HasNamed indicates if any field uses a named generated type that is not
	// available in the local enum map, usually an imported enum.
	HasNamed bool
	// NeedsTypes indicates if the types module needs to be imported.
	NeedsTypes bool
	// HasDefaults indicates whether the config class declares at least one
	// default value.
	HasDefaults bool
	// EnvConfig writes EnvConfig, the API's settings and derived fields
	// together, and LoadEnvConfig: the config the Go API's Deps holds. Set
	// by Options.Derived.
	EnvConfig bool
	// Derived are the config fields the API's edges derive in a stack
	// (docs/stack-model.md, section 3.4): its database's, then one per
	// calls entry. Set by Options.Derived.
	Derived []DerivedField
}

// EnvConfigTypeName is the Go type that holds an API's settings and the
// fields its edges derive.
const EnvConfigTypeName = "EnvConfig"

// DerivedField is a config field an edge derives: a database connection
// or a service endpoint, which the platform sets and the loader reads from
// one environment variable per member of the value.
type DerivedField struct {
	// Key is the field's name, which prefixes its variables
	// (SHOP_DB_DATABASE).
	Key string
	// GoName is the EnvConfig field's name (ShopDbDatabase).
	GoName string
	// Kind is the edge's kind: sql for the database, http for a call.
	Kind ir.EdgeKind
	// Service is the DB service or the called API service.
	Service string
	// From is the config key the edge comes from: authDb, dependencies or
	// calls.
	From string
	// Variables are the environment variables the field may be read from,
	// one per member of its value.
	Variables []string
}

// RuntimeType is the field's type in the Go HTTP runtime's stackconfig
// package, whose Load<RuntimeType> reads it.
func (f DerivedField) RuntimeType() string {
	if f.Kind == ir.EdgeSQL {
		return "Database"
	}
	return "Service"
}

// ContractType names the ir type of the value the field holds.
func (f DerivedField) ContractType() string {
	if f.Kind == ir.EdgeSQL {
		return "DatabaseConnection"
	}
	return "ServiceEndpoint"
}

// ConfigField represents a single environment variable configuration field.
type ConfigField struct {
	// Key is the environment variable name (e.g., "DATABASE_URL").
	Key string
	// GoName is the Go struct field name (e.g., "DatabaseUrl").
	GoName string
	// VarName is the local variable name (e.g., "databaseUrl").
	VarName string
	// GoType is the Go type for the field (e.g., "string", "int", "Duration").
	GoType string
	// IRType is the original schema type name.
	IRType string
	// Required indicates if the field is required (non-optional in the schema).
	Required bool
	// DefaultValue is the default value from @default directive.
	DefaultValue string
	// HasDefault indicates if a default value was specified.
	HasDefault bool
	// IsEnum indicates if the field type is an enum.
	IsEnum bool
	// IsScalar indicates if the field type is a custom scalar.
	IsScalar bool
	// ScalarPrimitive is the IR language primitive for custom scalars (e.g., "string", "number").
	ScalarPrimitive string
	// ScalarKind distinguishes integral, floating-point, and other numeric scalars.
	ScalarKind string
	// EnumValues are the valid values for enum types.
	EnumValues []string
	// Description is the field doc text from the schema.
	Description string
	// Secret indicates the field carries @secret in the IR. Deploy values
	// must bind such fields with a secretRef, never a literal.
	Secret bool
	// OptionalBool marks an optional boolean without a default, which the
	// types module declares as *bool; the loader leaves it nil when unset.
	OptionalBool bool
}

// findEnvVarsTypeIR finds the type with EnvVars == true in the IR schema.
// Returns nil if no such type is found.
func findEnvVarsTypeIR(schema *ir.Schema) *ir.TypeDef {
	for _, typeDef := range schema.Types {
		if typeDef.EnvVars {
			return typeDef
		}
	}
	return nil
}

// Options configures env-config generation.
type Options struct {
	// SchemaName is the service name (e.g. "web-api").
	SchemaName string

	// Dependencies maps dependency service names to their loaded schemas,
	// used to resolve imported enum fields.
	Dependencies map[string]*ir.Schema

	// Naming supplies the Go module root the config and types modules live
	// under, and the names of the derived fields. Empty fields fall back to
	// naming.Default().
	Naming naming.Naming

	// Derived adds the config fields an API's edges derive, and EnvConfig,
	// which joins them to the @envVars settings: what the Go API's Deps
	// holds. The output then exists for an API with derived fields and no
	// @envVars type. The TypeScript and Rust loaders do not read the
	// derived fields yet, so only the Go API asks for them.
	Derived bool
}

// Generate generates configuration code from an IR schema with @envVars directive.
// Returns nil if no @envVars type is found in the schema.
func Generate(schema *ir.Schema, schemaName string) (*ConfigOutput, error) {
	return GenerateWithDependencies(schema, schemaName, nil)
}

// GenerateWithDependencies generates configuration code, resolving imported
// enum fields against loaded dependency schemas when available, under the
// default naming.
func GenerateWithDependencies(schema *ir.Schema, schemaName string, dependencies map[string]*ir.Schema) (*ConfigOutput, error) {
	return GenerateWithOptions(schema, Options{SchemaName: schemaName, Dependencies: dependencies})
}

// GenerateWithOptions generates configuration code with explicit naming.
func GenerateWithOptions(schema *ir.Schema, opts Options) (*ConfigOutput, error) {
	schemaName := opts.SchemaName
	dependencies := opts.Dependencies
	names := opts.Naming.OrDefault()
	envVarsType := findEnvVarsTypeIR(schema)
	var derived []DerivedField
	if opts.Derived {
		var err error
		if derived, err = derivedFields(schema, envVarsType, names); err != nil {
			return nil, err
		}
	}
	if envVarsType == nil {
		if len(derived) == 0 {
			return nil, nil
		}
		return &ConfigOutput{
			PackageName: toPackageName(schemaName),
			SchemaName:  schemaName,
			ModulePath:  names.GoAPIModule(schemaName),
			TypesModule: names.GoTypesModule(schemaName),
			Naming:      names,
			Fields:      []ConfigField{},
			Enums:       []codegen.EnumInfo{},
			EnvConfig:   true,
			Derived:     derived,
		}, nil
	}
	if opts.Derived && envVarsType.Name == EnvConfigTypeName {
		return nil, fmt.Errorf("envgen: the @envVars type of %s is named %s, the name of the generated config that embeds it; rename it", schemaName, EnvConfigTypeName)
	}

	allEnums := codegen.ExtractEnums(schema)
	enumMap := buildEnumMapIR(allEnums)
	importedEnumMap, err := buildImportedEnumMapIR(schema, dependencies)
	if err != nil {
		return nil, err
	}
	for name, enumConfig := range importedEnumMap {
		enumMap[name] = enumConfig
	}

	scalarMap := buildScalarMapIR(schema)

	output := &ConfigOutput{
		PackageName: toPackageName(schemaName),
		SchemaName:  schemaName,
		TypeName:    envVarsType.Name,
		ModulePath:  names.GoAPIModule(schemaName),
		TypesModule: names.GoTypesModule(schemaName),
		Naming:      names,
		Fields:      []ConfigField{},
		Enums:       []codegen.EnumInfo{},
		EnvConfig:   opts.Derived,
		Derived:     derived,
	}

	usedEnums := make(map[string]bool)

	for _, field := range envVarsType.Fields {
		configField, err := extractConfigFieldIR(field, schema, enumMap, scalarMap)
		if err != nil {
			return nil, fmt.Errorf("failed to process field %s: %w", field.Name, err)
		}

		output.Fields = append(output.Fields, configField)

		if configField.IsEnum {
			usedEnums[configField.IRType] = true
			output.HasEnums = true
		}

		if configField.IsScalar {
			output.HasScalars = true
		}

		if isNamedConfigField(configField) {
			output.HasNamed = true
		}

		if configField.HasDefault {
			output.HasDefaults = true
		}
	}

	output.NeedsTypes = true

	for _, enum := range allEnums {
		if usedEnums[enum.Name] {
			output.Enums = append(output.Enums, enum)
		}
	}

	sort.Slice(output.Enums, func(i, j int) bool {
		return output.Enums[i].Name < output.Enums[j].Name
	})

	return output, nil
}

// derivedFields lists the config fields an API's edges derive, named by
// the naming file's [derived_fields], and refuses an @envVars field that
// collides with one: its name, or one of its variables'. The loader
// refuses the collision first; this guards a schema built as IR.
func derivedFields(schema *ir.Schema, envVarsType *ir.TypeDef, names naming.Naming) ([]DerivedField, error) {
	var out []DerivedField
	for _, f := range schema.DerivedConfigFields(names.DerivedFields.FieldNames()) {
		field := DerivedField{
			Key:     f.Name,
			GoName:  toEnvGoName(f.Name),
			Kind:    f.Kind,
			Service: f.Service,
			From:    f.From,
		}
		for _, path := range ir.DerivedMembers(f.Kind) {
			field.Variables = append(field.Variables, ir.DerivedVariableName(f.Name, path))
		}
		if envVarsType != nil {
			for _, setting := range envVarsType.Fields {
				if ir.DerivedFieldClaims(f.Name, setting.Name) {
					return nil, fmt.Errorf("envgen: %s: @envVars field %s of %s collides with %s, the config field its %s %s derives", schema.Name, setting.Name, envVarsType.Name, f.Name, f.From, f.Service)
				}
			}
		}
		out = append(out, field)
	}
	return out, nil
}

// extractConfigFieldIR extracts configuration field information from an IR FieldDef.
func extractConfigFieldIR(field *ir.FieldDef, schema *ir.Schema, enumMap map[string]enumMapping, scalarMap map[string]scalarInfo) (ConfigField, error) {
	typeName := field.TypeRef.Name
	enumConfig, isEnum := enumMap[typeName]
	scalarConfig, isScalar := scalarMap[typeName]

	configField := ConfigField{
		Key:         field.Name,
		GoName:      toEnvGoName(field.Name),
		VarName:     toLowerGoName(toEnvGoName(field.Name)),
		IRType:      typeName,
		Required:    field.Required,
		IsEnum:      isEnum,
		IsScalar:    isScalar,
		Description: codegen.DocText(field.Description, field.Comment),
		Secret:      field.Secret,
		OptionalBool: codegen.GoOptionalBoolIsPointer(
			typeName, field.Required, field.TypeRef.IsArray, field.TypeRef.IsMap, field.Default),
	}

	if isEnum {
		configField.EnumValues = enumConfig.Values
	}

	if isScalar {
		configField.ScalarPrimitive = scalarConfig.LanguagePrimitive
		configField.ScalarKind = scalarConfig.Primitive
	}

	goType, err := mapConfigGoType(typeName, isEnum, isScalar, scalarConfig)
	if err != nil {
		return ConfigField{}, err
	}
	configField.GoType = goType

	defaultValue, hasDefault := extractDefaultValueIR(field, enumConfig)
	configField.DefaultValue = defaultValue
	configField.HasDefault = hasDefault

	return configField, nil
}

// enumMapping holds enum value information.
type enumMapping struct {
	Values      []string
	ValueByName map[string]string
}

// scalarInfo holds scalar type information for env var fields.
type scalarInfo struct {
	Name              string
	LanguagePrimitive string
	GoType            string
	Primitive         string
}

// buildEnumMapIR creates a map of enum types to their values from codegen.EnumInfo.
func buildEnumMapIR(enums []codegen.EnumInfo) map[string]enumMapping {
	result := make(map[string]enumMapping)

	for _, e := range enums {
		values := make([]string, len(e.Values))
		valueByName := make(map[string]string, len(e.Values))

		for i, v := range e.Values {
			cleanValue := trimQuotes(v.Value)
			values[i] = cleanValue
			valueByName[v.Name] = cleanValue
		}

		result[e.Name] = enumMapping{
			Values:      values,
			ValueByName: valueByName,
		}
	}

	return result
}

func buildImportedEnumMapIR(schema *ir.Schema, dependencies map[string]*ir.Schema) (map[string]enumMapping, error) {
	result := make(map[string]enumMapping)
	if len(schema.Imports) == 0 {
		return result, nil
	}
	if len(dependencies) == 0 {
		return result, nil
	}

	imports := append([]ir.Import{}, schema.Imports...)
	sort.Slice(imports, func(i, j int) bool {
		return imports[i].Package < imports[j].Package
	})

	for _, imp := range imports {
		depName := dependencyServiceName(imp.Package)
		depSchema := dependencies[depName]
		if depSchema == nil {
			return nil, fmt.Errorf("envgen: dependency schema %q (package %q) not loaded", depName, imp.Package)
		}

		for _, symbol := range imp.Types {
			enumDef := depSchema.Enums[symbol]
			if enumDef == nil {
				continue
			}
			enumInfo := codegen.EnumInfo{
				Name:        enumDef.Name,
				Owner:       enumDef.Owner,
				Description: enumDef.Description,
				Comment:     enumDef.Comment,
				Values:      make([]codegen.EnumValueInfo, 0, len(enumDef.Values)),
			}
			for _, val := range enumDef.Values {
				value := val.Name
				if val.SerializedAs != "" {
					value = val.SerializedAs
				}
				enumInfo.Values = append(enumInfo.Values, codegen.EnumValueInfo{
					Name:        val.Name,
					Value:       value,
					Description: val.Description,
					Comment:     val.Comment,
				})
			}
			result[symbol] = buildEnumMapIR([]codegen.EnumInfo{enumInfo})[symbol]
		}
	}

	return result, nil
}

func dependencyServiceName(pkg string) string {
	if idx := strings.LastIndex(pkg, "/"); idx >= 0 {
		return pkg[idx+1:]
	}
	return pkg
}

// buildScalarMapIR creates a map of scalar types to their info from IR.
func buildScalarMapIR(schema *ir.Schema) map[string]scalarInfo {
	result := make(map[string]scalarInfo)

	for name, scalarDef := range schema.Scalars {
		info := scalarInfo{
			Name:              name,
			LanguagePrimitive: string(scalarDef.LanguagePrimitive),
			GoType:            "string",
			Primitive:         scalarDef.Primitive,
		}

		if info.LanguagePrimitive == "" {
			info.LanguagePrimitive = "string"
		}

		if goType, ok := scalarDef.TypeMappings["go"]; ok && goType != "" {
			info.GoType = goType
		}
		result[name] = info
	}

	return result
}

// extractDefaultValueIR extracts the default value from an IR FieldDef.
func extractDefaultValueIR(field *ir.FieldDef, enumConfig enumMapping) (string, bool) {
	if field.Default == nil {
		return "", false
	}

	raw := trimQuotes(*field.Default)
	if raw == "" {
		return raw, true
	}

	if mapped, ok := enumConfig.ValueByName[raw]; ok {
		return mapped, true
	}

	return raw, true
}

// mapConfigGoType maps an IR type to a Go type for configuration.
func mapConfigGoType(typeName string, isEnum bool, isScalar bool, scalarConfig scalarInfo) (string, error) {
	if isEnum {
		return normalizeGoTypeIdentifier(typeName), nil
	}

	// Handle custom scalars - use the scalar type from the types package
	// This enables validation using the scalar's Validate() method
	if isScalar {
		// Return the scalar type name (will be prefixed with types. in template)
		return normalizeGoTypeIdentifier(typeName), nil
	}

	switch typeName {
	case codegen.PrimitiveString:
		return "string", nil
	case codegen.PrimitiveNumber:
		// Env-var numbers load through the integer parser: every production
		// numeric env var (ports, limits, timeouts) is integral, and the
		// loader rejects fractional input loudly rather than truncating.
		return "int", nil
	case codegen.PrimitiveBoolean:
		return "bool", nil
	default:
		return normalizeGoTypeIdentifier(typeName), nil
	}
}

func normalizeGoTypeIdentifier(typeName string) string {
	typeName = strings.TrimSpace(typeName)
	if typeName == "" {
		return typeName
	}
	if !strings.Contains(typeName, ".") {
		return typeName
	}

	tokens := codegen.BuildScalarTokens(typeName)
	if strings.TrimSpace(tokens.Symbol) != "" {
		return tokens.Symbol
	}

	return strings.ReplaceAll(typeName, ".", "")
}

// toEnvGoName converts an environment variable key to a Go field name.
// Example: "DATABASE_URL" -> "DatabaseUrl"
func toEnvGoName(key string) string {
	parts := strings.Split(strings.ToLower(key), "_")
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, "")
}

// toLowerGoName converts a Go field name to a local variable name.
// Example: "DatabaseUrl" -> "databaseUrl"
func toLowerGoName(name string) string {
	if name == "" {
		return ""
	}
	return strings.ToLower(name[:1]) + name[1:]
}

// toPackageName converts a schema name to a valid Go package name.
// Example: "web-api" -> "webapi"
func toPackageName(s string) string {
	s = strings.ReplaceAll(s, "-", "")
	s = strings.ReplaceAll(s, "_", "")
	return strings.ToLower(s)
}

// trimQuotes removes surrounding quotes from a string value.
func trimQuotes(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, `"`)
	return value
}

// WriteConfig writes the generated configuration code to files.
func WriteConfig(output *ConfigOutput, outputDir string) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("failed to create output directory %s: %w", outputDir, err)
	}

	if err := generateFile("config.tmpl", filepath.Join(outputDir, "config.go"), output); err != nil {
		return fmt.Errorf("failed to generate config.go: %w", err)
	}

	if err := WriteValuesSchema(output, outputDir); err != nil {
		return fmt.Errorf("failed to generate values schema: %w", err)
	}

	return nil
}

// SetReplacePaths sets the go.mod replace directive paths for the scalar
// library and the schema IR, which the types module requires, relative to
// the output directory of the standalone module. Go reads replace directives
// only from the main module, so the types module's own do not apply here.
// An unset path emits no directive.
func SetReplacePaths(output *ConfigOutput, paths naming.LocalPaths, outputDir string) error {
	var err error
	if output.ScalarLibReplacePath, err = naming.RelPath(outputDir, paths.ScalarGo); err != nil {
		return fmt.Errorf("scalar library replace path: %w", err)
	}
	if output.SchemaIRReplacePath, err = naming.RelPath(outputDir, paths.SchemaIR); err != nil {
		return fmt.Errorf("schema-ir replace path: %w", err)
	}
	return nil
}

// SetReleasePins keeps the pins (naming.Naming.ReleasePins) of the runtime
// modules the standalone module reaches through its types module: the
// scalar library and the schema IR. go.mod replaces every version of each
// pinned module with its pin.
func SetReleasePins(output *ConfigOutput, pins naming.Pins) {
	output.Pins = pins.Of(output.Naming.ScalarGoModule, output.Naming.SchemaIRGoModule)
}

// WriteConfigModule writes a standalone generated environment config module.
func WriteConfigModule(output *ConfigOutput, outputDir string) error {
	if err := WriteConfig(output, outputDir); err != nil {
		return err
	}

	if err := generateFile("module.tmpl", filepath.Join(outputDir, "go.mod"), output); err != nil {
		return fmt.Errorf("failed to generate go.mod: %w", err)
	}

	return nil
}

func generateFile(templateName, outputPath string, data any) error {
	return codegen.GenerateFile(
		codegen.NewGoFileConfig(templatesFS, templateName, outputPath, data, templateFuncs()),
	)
}

// templateFuncs returns template functions.
func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"base":              path.Base,
		"title":             codegen.TitleCase,
		"toSnakeCase":       codegen.ToSnakeCase,
		"toEnvGoName":       toEnvGoName,
		"formatStringSlice": formatStringSlice,
		"loaderExpr":        buildLoaderExpr,
		"defaultExpr":       buildDefaultExpr,
		"assignsPointer":    assignsPointer,
		"isNamed":           isNamedConfigField,
		"primitiveAssign":   primitiveAssignExpr,
		"envConfigType":     func() string { return EnvConfigTypeName },
	}
}

func assignsPointer(field ConfigField) bool {
	if field.Required {
		return false
	}
	return field.IsEnum || field.IsScalar || isNamedConfigField(field)
}

func isNamedConfigField(field ConfigField) bool {
	return !field.IsEnum && !field.IsScalar && !codegen.IsLanguagePrimitive(field.IRType)
}

func primitiveAssignExpr(field ConfigField, valueExpr string) string {
	switch field.IRType {
	case codegen.PrimitiveNumber:
		return "float64(" + valueExpr + ")"
	default:
		return valueExpr
	}
}

// formatStringSlice formats a string slice as a Go literal.
func formatStringSlice(values []string) string {
	if len(values) == 0 {
		return "nil"
	}

	escaped := make([]string, len(values))
	for i, value := range values {
		escaped[i] = fmt.Sprintf("%q", value)
	}

	return fmt.Sprintf("[]string{%s}", strings.Join(escaped, ", "))
}

// languagePrimitiveLoaderName maps an IR primitive name to the loader
// dispatch key used by buildPrimitiveLoaderExpr. Numbers use the integer
// loader (see mapConfigGoType).
func languagePrimitiveLoaderName(name string) string {
	switch name {
	case codegen.PrimitiveNumber:
		return "Int"
	case codegen.PrimitiveBoolean:
		return "Boolean"
	default:
		return "String"
	}
}

// buildLoaderExpr builds the loader expression for a config field.
func buildLoaderExpr(field ConfigField) string {
	keyLiteral := fmt.Sprintf("%q", field.Key)

	// Handle enum fields
	if field.IsEnum {
		allowedLiteral := formatStringSlice(field.EnumValues)
		if field.Required && !field.HasDefault {
			return fmt.Sprintf("requireEnum(%s, %s)", keyLiteral, allowedLiteral)
		}
		defaultLiteral := `""`
		if field.HasDefault {
			defaultLiteral = fmt.Sprintf("%q", field.DefaultValue)
		}
		return fmt.Sprintf("enumOrDefault(%s, %s, %s)", keyLiteral, allowedLiteral, defaultLiteral)
	}

	// Handle scalar fields - they use their primitive type's loader.
	if field.IsScalar {
		return buildPrimitiveLoaderExpr(languagePrimitiveLoaderName(field.ScalarPrimitive), field)
	}

	// Handle basic types.
	return buildPrimitiveLoaderExpr(languagePrimitiveLoaderName(field.IRType), field)
}

// buildPrimitiveLoaderExpr builds the loader expression for a primitive type.
func buildPrimitiveLoaderExpr(primitiveType string, field ConfigField) string {
	keyLiteral := fmt.Sprintf("%q", field.Key)

	switch primitiveType {
	case "String", "string":
		if field.Required && !field.HasDefault {
			return fmt.Sprintf("requireString(%s)", keyLiteral)
		}
		defaultLiteral := `""`
		if field.HasDefault {
			defaultLiteral = fmt.Sprintf("%q", field.DefaultValue)
		}
		return fmt.Sprintf("stringOrDefault(%s, %s)", keyLiteral, defaultLiteral)

	case "Int", "int":
		if field.Required && !field.HasDefault {
			return fmt.Sprintf("requireInt(%s)", keyLiteral)
		}
		defaultInt := 0
		if field.HasDefault {
			parsed, _ := strconv.Atoi(field.DefaultValue)
			defaultInt = parsed
		}
		return fmt.Sprintf("intOrDefault(%s, %d)", keyLiteral, defaultInt)

	case "Boolean", "bool":
		if field.Required && !field.HasDefault {
			return fmt.Sprintf("requireBool(%s)", keyLiteral)
		}
		defaultBool := false
		if field.HasDefault {
			parsed, _ := strconv.ParseBool(field.DefaultValue)
			defaultBool = parsed
		}
		return fmt.Sprintf("boolOrDefault(%s, %t)", keyLiteral, defaultBool)
	}

	// Default to string for unknown types
	if field.Required && !field.HasDefault {
		return fmt.Sprintf("requireString(%s)", keyLiteral)
	}
	defaultLiteral := `""`
	if field.HasDefault {
		defaultLiteral = fmt.Sprintf("%q", field.DefaultValue)
	}
	return fmt.Sprintf("stringOrDefault(%s, %s)", keyLiteral, defaultLiteral)
}

// buildDefaultExpr builds the default value expression for a config field.
func buildDefaultExpr(field ConfigField) string {
	if !field.HasDefault {
		return ""
	}

	switch field.GoType {
	case "string":
		return fmt.Sprintf("%q", field.DefaultValue)
	case "int":
		return field.DefaultValue
	case "bool":
		return field.DefaultValue
	default:
		if field.IsEnum {
			return fmt.Sprintf("%q", field.DefaultValue)
		}
		return fmt.Sprintf("%q", field.DefaultValue)
	}
}
