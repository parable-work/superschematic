// Package typegen generates the Go type library for a schema: scalar
// aliases onto superscalar, enums, structs with validators and JSON/YAML
// codecs, and discriminated-union wrappers.
//
// This is the v2 port of the v1 typegen generator onto the refactored Schema
// IR: types are selected by TypeDef.Role instead of the Object/Input split,
// inherited fields arrive pre-flattened, imported definitions come from the
// schema's named imports plus the loaded dependency schemas, and node
// comments become doc comments.
package typegen

import (
	"embed"
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

//go:embed templates/*.tmpl
var templatesFS embed.FS

// localObjectRoles are the type roles emitted as plain (non-input) structs.
var localObjectRoles = []ir.Role{ir.RoleDBTable, ir.RoleAPIView, ir.RoleEmbeddedStruct}

// ScalarInfo holds information about a scalar type (Go-specific wrapper).
type ScalarInfo struct {
	Name                         string
	GoType                       string
	Primitive                    ir.LanguagePrimitive
	Doc                          string
	MaxLength                    int
	MinLength                    int
	Pattern                      string
	Format                       string
	CaseInsensitive              bool
	ReservedWords                []string
	ReservedWordsCaseInsensitive bool
	ReservedWordsMatchPartial    bool
	Minimum                      *int64
	Maximum                      *int64
	Example                      string
	HasCustomNormalize           bool
	HasCustomValidate            bool
	HasCustomParse               bool
	Tokens                       codegen.ScalarTokens
	Traits                       codegen.ScalarTraits

	// ParseTarget is the superscalar function the generated Parse alias binds
	// to (e.g. "ParseIdentityUUID", "ParseUUID"). Empty means no Parse alias
	// is emitted for this scalar.
	ParseTarget string

	// ParseAsInt64 means the superscalar parse target returns a canonical string
	// that must be parsed into this module's int64-backed scalar alias.
	ParseAsInt64 bool

	// ParseAsJSON means the superscalar parse target returns canonical JSON
	// text that must be decoded into this module's scalar alias (a map or
	// other JSON-like Go type).
	ParseAsJSON bool
}

// FieldInfo holds information about a struct field (Go-specific wrapper).
// The schema field name is the JSON name; there are no custom JSON tags.
type FieldInfo struct {
	Name             string
	GoName           string
	Type             string
	GoType           string
	Required         bool
	InternalMetadata bool
	IsInput          bool
	UsesWrapper      bool
	Secret           bool
	IsArray          bool
	IsMap            bool
	IsScalar         bool
	IsUnion          bool
	Doc              string
	ScalarInfo       *ScalarInfo
	Validations      []codegen.ValidationRule

	// IsArrayOfArrays marks T[][]; IsArray is then true too. GoType is
	// [][]T, or InputField[[][]T] for an optional input field. An inner list
	// is never null, so Validate rejects a nil inner slice, and list bounds
	// apply to the outer list.
	IsArrayOfArrays bool

	// HasDefault is true when a @default was declared on the field and a Go
	// literal could be produced for it.
	HasDefault bool

	// DefaultLiteral is the Go expression used to initialize the field with
	// its declared default value. Only populated when HasDefault is true.
	DefaultLiteral string
}

// ArrayDepth is 0 for T, 1 for T[] and 2 for T[][].
func (f FieldInfo) ArrayDepth() int {
	return ir.TypeRef{IsArray: f.IsArray, IsArrayOfArrays: f.IsArrayOfArrays}.ArrayDepth()
}

// TypeInfo holds information about a complex type.
type TypeInfo struct {
	Name        string
	Owner       string
	Role        ir.Role
	Doc         string
	Fields      []FieldInfo
	IsInput     bool
	IsJsonField bool
	StrictJSON  bool
	// OpenAPISchemaJSON is the quoted standalone OpenAPI schema of a
	// @strictJSON type in a General schema; the template emits a
	// <Type>OpenAPISchema() accessor for it. Empty emits none.
	OpenAPISchemaJSON string

	// HasDefaults is true when at least one field on this type has a usable
	// DefaultLiteral. Templates use this flag to decide whether to emit a
	// New<Type>() constructor and a defaults-aware UnmarshalJSON.
	HasDefaults bool
}

// UnionInfo holds information about a union type.
type UnionInfo struct {
	Name          string
	Doc           string
	Types         []string
	Discriminator string
	Members       []UnionMemberInfo

	// DispatchByDiscriminator is true when the wrapper unmarshaler can pick
	// the correct member type by looking up Discriminator in the payload and
	// matching its value against each member's DiscriminatorValue. Requires
	// a non-empty Discriminator and a non-empty DiscriminatorValue on every
	// member. Otherwise the generated wrapper falls back to shape-based
	// dispatch (strict-only, no lenient fallback).
	DispatchByDiscriminator bool
}

// UnionMemberInfo carries per-member metadata used by the unions template.
type UnionMemberInfo struct {
	Name               string
	DiscriminatorValue string
}

// TypePairField describes how to convert one field from input to output type.
type TypePairField struct {
	GoName          string
	InputGoType     string
	GoType          string
	Required        bool
	UsesWrapper     bool
	IsArray         bool
	IsArrayOfArrays bool
	IsScalar        bool
	IsPaired        bool
	PairedTypeName  string
}

// TypePair describes a matched input/output type pair for ToType() generation.
type TypePair struct {
	InputName  string
	OutputName string
	Fields     []TypePairField
}

// ImportedEnumInfo holds alias metadata for enums owned by a dependency module.
type ImportedEnumInfo struct {
	Name        string
	Doc         string
	ImportAlias string
	Values      []codegen.EnumValueInfo
}

// ImportedTypeInfo holds alias metadata for object/input types owned by a
// dependency module.
type ImportedTypeInfo struct {
	Name        string
	Doc         string
	ImportAlias string
	IsInput     bool
}

// ImportedUnionInfo holds alias metadata for unions owned by a dependency module.
type ImportedUnionInfo struct {
	Name        string
	Doc         string
	ImportAlias string
}

// ModuleImport describes a Go import required by dependency aliases.
type ModuleImport struct {
	Alias string
	Path  string
}

// ModuleDependencyReplace holds a go.mod replace directive for a sibling type module.
type ModuleDependencyReplace struct {
	Module  string
	RelPath string
}

// EnumInfo is the enum data consumed by the enums template, with doc text
// resolved from description-or-comment.
type EnumInfo struct {
	Name   string
	Doc    string
	Values []codegen.EnumValueInfo
}

// ModuleOutput contains all generated code for a types module.
type ModuleOutput struct {
	PackageName              string
	SchemaName               string
	ModulePath               string
	Scalars                  []ScalarInfo
	Types                    []TypeInfo
	Unions                   []UnionInfo
	Enums                    []EnumInfo
	ImportedEnums            []ImportedEnumInfo
	ImportedTypes            []ImportedTypeInfo
	ImportedUnions           []ImportedUnionInfo
	Imports                  []ModuleImport
	ModuleDependencies       []string
	ModuleDependencyReplaces []ModuleDependencyReplace
	TypePairs                []TypePair
	CompositeDefaults        []CompositeDefaultInfo
	HasInputFieldWrappers    bool
	HasVersionedTypes        bool
	HasInt64ScalarParsers    bool
	HasJSONScalarParsers     bool

	// Naming supplies the scalar and schema-ir module paths the templates
	// import and require.
	Naming               naming.Naming
	Timestamp            string
	ScalarLibReplacePath string
	SchemaIRReplacePath  string
}

// CompositeDefaultInfo is one generated fresh-value accessor.
type CompositeDefaultInfo struct {
	AccessorName string
	Type         string
	JSONLiteral  string
}

// Options configures Go type generation.
type Options struct {
	// SchemaName is the service name (e.g. "web-db").
	SchemaName string

	// ModulePath is the Go module path for the generated module.
	ModulePath string

	// PackageName is the Go package name (defaults to "types").
	PackageName string

	// Dependencies maps dependency service names to their loaded schemas,
	// used to classify imported symbols (enum vs type vs union vs scalar).
	Dependencies map[string]*ir.Schema

	// DependencyModules maps dependency service names to their generated Go
	// module paths.
	DependencyModules map[string]string

	// Naming supplies the scalar and schema-ir module paths go.mod requires.
	// Empty fields fall back to naming.Default().
	Naming naming.Naming

	// Clock stamps the generated output.
	Clock codegen.Clock
}

// Generate generates Go types from a v2 IR schema.
func Generate(schema *ir.Schema, opts Options) (*ModuleOutput, error) {
	if opts.Clock == nil {
		opts.Clock = codegen.DefaultClock()
	}
	opts.Naming = opts.Naming.OrDefault()

	output := &ModuleOutput{
		PackageName: "types",
		SchemaName:  opts.SchemaName,
		ModulePath:  opts.ModulePath,
		Naming:      opts.Naming,
		Timestamp:   opts.Clock.RFC3339(),
	}
	if opts.PackageName != "" {
		output.PackageName = opts.PackageName
	}

	extraction := codegen.ExtractionConfig{
		Language:        "go",
		PrimitiveMapper: inferGoType,
		FieldTypeMapper: fieldTypeMapperGo,
		FieldNameMapper: codegen.ToPascalCase,
	}

	codegenScalars := codegen.ExtractScalars(schema, extraction)
	output.Scalars = convertScalars(codegenScalars)
	for _, scalar := range output.Scalars {
		if scalar.ParseAsInt64 {
			output.HasInt64ScalarParsers = true
		}
		if scalar.ParseAsJSON {
			output.HasJSONScalarParsers = true
		}
	}

	imported, err := resolveImports(schema, opts)
	if err != nil {
		return nil, err
	}
	output.ImportedEnums = imported.enums
	output.ImportedTypes = imported.types
	output.ImportedUnions = imported.unions
	output.Imports = imported.imports
	output.ModuleDependencies = imported.moduleDependencies(opts.ModulePath)
	// Go does not inherit replace directives from a dependency's go.mod.
	// Replace every declared schema dependency, including one this module
	// reaches only through another generated types module.
	output.ModuleDependencyReplaces = moduleDependencyReplaces(
		allDependencyModulePaths(opts.ModulePath, output.ModuleDependencies, opts.DependencyModules),
	)

	output.Enums = convertEnums(codegen.ExtractEnums(schema))

	output.Unions = convertUnions(codegen.ExtractUnions(schema))
	for _, def := range codegen.ExtractCompositeDefaults(schema) {
		output.CompositeDefaults = append(output.CompositeDefaults, CompositeDefaultInfo{
			AccessorName: "Default" + codegen.ToPascalCase(def.Name),
			Type:         def.Type,
			JSONLiteral:  strconv.Quote(def.CanonicalJSON),
		})
	}

	enumLookup := buildEnumLookup(output.Enums, output.ImportedEnums)

	objectTypes := codegen.ExtractTypes(schema, codegenScalars, extraction, localObjectRoles...)
	objectTypes = codegen.AddVersionFields(objectTypes, extraction)
	inputTypes := codegen.ExtractTypes(schema, codegenScalars, extraction, ir.RoleAPIInput)

	importAliases := importAliasReplacements(output.Imports)
	unionNames := knownUnionNames(output.Unions, output.ImportedUnions)
	output.Types = convertTypes(objectTypes, output.Scalars, false, enumLookup, importAliases, unionNames)
	output.Types = append(output.Types, convertTypes(inputTypes, output.Scalars, true, enumLookup, importAliases, unionNames)...)
	// A strict General type is a closed payload contract; give it its
	// standalone OpenAPI schema so a Go program can hand the contract to a
	// consumer that validates or documents it.
	if schema.Kind == ir.SchemaKindGeneral {
		for i := range output.Types {
			if !output.Types[i].StrictJSON {
				continue
			}
			openAPISchema, err := apigen.TypeOpenAPISchema(output.Types[i].Name, schema, opts.Dependencies)
			if err != nil {
				return nil, err
			}
			encoded, err := json.Marshal(openAPISchema)
			if err != nil {
				return nil, fmt.Errorf("encode OpenAPI schema for %s: %w", output.Types[i].Name, err)
			}
			output.Types[i].OpenAPISchemaJSON = strconv.Quote(string(encoded))
		}
	}
	output.TypePairs = buildTypePairs(output.Types, output.ImportedTypes)

	for _, typeInfo := range output.Types {
		if typeInfo.Role == ir.RoleDBTable {
			for _, field := range typeInfo.Fields {
				if field.Name == "_version" && field.InternalMetadata {
					output.HasVersionedTypes = true
					break
				}
			}
		}
		for _, field := range typeInfo.Fields {
			if field.UsesWrapper {
				output.HasInputFieldWrappers = true
			}
		}
	}

	return output, nil
}

// IsEmpty reports whether the module has nothing to emit.
func (o *ModuleOutput) IsEmpty() bool {
	return len(o.Scalars) == 0 && len(o.Types) == 0 && len(o.Unions) == 0 &&
		len(o.Enums) == 0 && len(o.ImportedEnums) == 0 && len(o.ImportedTypes) == 0 &&
		len(o.ImportedUnions) == 0
}

// SetReplacePaths sets the go.mod replace directive paths for the scalar
// library and the schema IR relative to the output directory where the
// generated module will live. An unset path emits no directive.
func SetReplacePaths(output *ModuleOutput, paths naming.LocalPaths, outputDir string) error {
	var err error
	if output.ScalarLibReplacePath, err = naming.RelPath(outputDir, paths.ScalarGo); err != nil {
		return fmt.Errorf("scalar library replace path: %w", err)
	}
	if output.SchemaIRReplacePath, err = naming.RelPath(outputDir, paths.SchemaIR); err != nil {
		return fmt.Errorf("schema-ir replace path: %w", err)
	}
	return nil
}

// fieldTypeMapperGo maps IR type references to Go types.
func fieldTypeMapperGo(typeName string, arrayDepth int, isMap bool, isRequired bool, scalarMap codegen.ScalarMap) string {
	valueType := fieldValueTypeMapperGo(typeName, arrayDepth, isMap, isRequired, scalarMap)
	if !isMap {
		return valueType
	}
	return "map[string]" + valueType
}

func fieldValueTypeMapperGo(typeName string, arrayDepth int, inMap bool, isRequired bool, scalarMap codegen.ScalarMap) string {
	if arrayDepth > 0 {
		elemRequired := true
		if inMap {
			elemRequired = isRequired
		}
		elemType := fieldValueTypeMapperGo(typeName, 0, inMap, elemRequired, scalarMap)
		return codegen.WrapArray(elemType, arrayDepth, func(elem string) string { return "[]" + elem })
	}

	if scalar, ok := scalarMap[typeName]; ok {
		scalarType := strings.TrimSpace(scalar.Tokens.Symbol)
		if scalarType == "" {
			scalarType = typeName
		}
		if !isRequired {
			return "*" + scalarType
		}
		return scalarType
	}

	var goType string
	isPrimitive := true
	switch typeName {
	case codegen.PrimitiveString:
		goType = "string"
	case codegen.PrimitiveNumber:
		goType = "float64"
	case codegen.PrimitiveBoolean:
		goType = "bool"
	default:
		goType = typeName
		isPrimitive = false
	}

	if inMap && !isRequired {
		return "*" + goType
	}

	if !isRequired && !isPrimitive {
		return "*" + goType
	}

	return goType
}

// inferGoType infers a Go type from the host-language primitive when the
// scalar has no Go TypeMappings entry.
func inferGoType(primitive ir.LanguagePrimitive, scalarName string) string {
	traits := codegen.GuessScalarTraits(scalarName)
	switch primitive {
	case ir.LanguageString:
		if traits.IsUUIDLike {
			return "UUID"
		}
		if traits.IsDateTimeLike {
			return "time.Time"
		}
		if traits.IsJSONLike || traits.IsObjectLike || traits.IsLocationLike {
			return "interface{}"
		}
		return "string"
	case ir.LanguageNumber:
		return "float64"
	case ir.LanguageBoolean:
		return "bool"
	case ir.LanguageObject:
		return "interface{}"
	}
	return "string"
}

// convertScalars converts codegen.ScalarInfo to typegen.ScalarInfo.
func convertScalars(codegenScalars []codegen.ScalarInfo) []ScalarInfo {
	scalars := make([]ScalarInfo, len(codegenScalars))
	for i, cs := range codegenScalars {
		goType := cs.TargetType
		if cs.Traits.IsUUIDLike && goType == "uuid.UUID" && cs.Tokens.Symbol != "UUID" {
			goType = "UUID"
		}

		scalar := ScalarInfo{
			Name:                         cs.Name,
			GoType:                       goType,
			Primitive:                    cs.Primitive,
			Doc:                          codegen.DocText(cs.Description, cs.Comment),
			MaxLength:                    cs.MaxLength,
			MinLength:                    cs.MinLength,
			Pattern:                      cs.Pattern,
			Format:                       cs.Format,
			CaseInsensitive:              cs.CaseInsensitive,
			ReservedWords:                cs.ReservedWords,
			ReservedWordsCaseInsensitive: cs.ReservedWordsCaseInsensitive,
			ReservedWordsMatchPartial:    cs.ReservedWordsMatchPartial,
			Minimum:                      cs.Minimum,
			Maximum:                      cs.Maximum,
			Example:                      cs.Example,
			HasCustomNormalize:           cs.HasCustomNormalize,
			HasCustomValidate:            cs.HasCustomValidate,
			HasCustomParse:               cs.HasCustomParse,
			Tokens:                       cs.Tokens,
			Traits:                       cs.Traits,
		}
		scalar.ParseTarget = scalarLibParseTarget(scalar)
		scalar.ParseAsInt64 = scalar.ParseTarget != "" && scalar.Traits.IsIntegerLike
		scalar.ParseAsJSON = scalar.HasCustomParse && scalar.Traits.IsJSONLike
		scalars[i] = scalar
	}
	return scalars
}

// scalarLibParseTarget returns the superscalar function name the generated
// Parse<Symbol> alias binds to, or "" when no Parse alias applies.
//
// A custom-parse scalar binds to a function the superscalar Go binding
// exports for it. The binding generates Parse<Symbol> for every non-object
// scalar and returns the canonical string, which the template converts:
// strconv for an integer, json for a JSON shape, a plain conversion
// otherwise. A Go type with its own typed parser (UUID, DateTime,
// Duration) binds to that parser instead. The binding exports no parser
// named after the last identity segment (ParseSeconds, ParseUserID), so the
// name must never be built from it.
func scalarLibParseTarget(scalar ScalarInfo) string {
	if scalar.HasCustomParse {
		switch {
		case scalar.Traits.IsJSONLike, scalar.Traits.IsIntegerLike:
			return "Parse" + scalar.Tokens.Symbol
		case scalar.Traits.IsUUIDLike:
			return "ParseUUID"
		case scalar.Traits.IsDateTimeLike:
			return "ParseDateTime"
		case scalar.Traits.IsDurationLike:
			return "ParseDuration"
		}
		return "Parse" + scalar.Tokens.Symbol
	}
	if scalar.Primitive == ir.LanguageString && !scalar.Traits.IsObjectLike {
		return "Parse" + scalar.Tokens.Symbol
	}
	if scalar.GoType == "int64" && scalar.Primitive == ir.LanguageNumber {
		return "Parse" + scalar.Tokens.Symbol
	}
	if scalar.Traits.IsUUIDLike {
		return "ParseUUID"
	}
	if scalar.Traits.IsDateTimeLike {
		return "ParseDateTime"
	}
	if scalar.Traits.IsDurationLike {
		return "ParseDuration"
	}
	return ""
}

func convertEnums(enums []codegen.EnumInfo) []EnumInfo {
	converted := make([]EnumInfo, len(enums))
	for i, e := range enums {
		converted[i] = EnumInfo{
			Name:   e.Name,
			Doc:    e.Doc(),
			Values: e.Values,
		}
	}
	return converted
}

// convertUnions converts codegen.UnionInfo to typegen.UnionInfo.
func convertUnions(codegenUnions []codegen.UnionInfo) []UnionInfo {
	unions := make([]UnionInfo, len(codegenUnions))
	for i, cu := range codegenUnions {
		members := make([]UnionMemberInfo, len(cu.Members))
		dispatchByDiscriminator := cu.Discriminator != "" && len(cu.Members) > 0
		for j, m := range cu.Members {
			members[j] = UnionMemberInfo{
				Name:               m.Name,
				DiscriminatorValue: m.DiscriminatorValue,
			}
			if m.DiscriminatorValue == "" {
				dispatchByDiscriminator = false
			}
		}
		unions[i] = UnionInfo{
			Name:                    cu.Name,
			Doc:                     cu.Doc(),
			Types:                   cu.Types,
			Discriminator:           cu.Discriminator,
			Members:                 members,
			DispatchByDiscriminator: dispatchByDiscriminator,
		}
	}
	return unions
}

// convertTypes converts codegen.TypeInfo to typegen.TypeInfo.
// knownUnionNames returns the local and imported union names. A field typed
// with an imported union is a union field too, though codegen, which sees
// only this schema, does not mark it.
func knownUnionNames(local []UnionInfo, imported []ImportedUnionInfo) map[string]bool {
	names := make(map[string]bool, len(local)+len(imported))
	for _, union := range local {
		names[union.Name] = true
	}
	for _, union := range imported {
		names[union.Name] = true
	}
	return names
}

func convertTypes(codegenTypes []codegen.TypeInfo, scalars []ScalarInfo, isInput bool, enumLookup codegen.EnumLookup, importAliases map[string]string, unionNames map[string]bool) []TypeInfo {
	scalarMap := buildScalarMap(scalars)
	types := make([]TypeInfo, len(codegenTypes))
	for i, ct := range codegenTypes {
		fields := convertFields(ct.Fields, scalarMap, isInput, enumLookup, importAliases, unionNames)
		hasDefaults := false
		for _, f := range fields {
			if f.HasDefault && f.DefaultLiteral != "" {
				hasDefaults = true
				break
			}
		}
		types[i] = TypeInfo{
			Name:        ct.Name,
			Owner:       ct.Owner,
			Role:        ct.Role,
			Doc:         ct.Doc(),
			Fields:      fields,
			IsInput:     isInput,
			IsJsonField: ct.JsonField,
			StrictJSON:  ct.StrictJSON,
			HasDefaults: hasDefaults,
		}
	}
	return types
}

// buildEnumLookup returns a closure that reports whether a type name refers
// to an enum declared locally or imported from a dependency module.
func buildEnumLookup(localEnums []EnumInfo, importedEnums []ImportedEnumInfo) codegen.EnumLookup {
	known := make(map[string]struct{}, len(localEnums)+len(importedEnums))
	for _, e := range localEnums {
		known[e.Name] = struct{}{}
	}
	for _, e := range importedEnums {
		known[e.Name] = struct{}{}
	}
	return func(typeName string) bool {
		_, ok := known[typeName]
		return ok
	}
}

// convertFields converts codegen.FieldInfo to typegen.FieldInfo.
func convertFields(codegenFields []codegen.FieldInfo, scalarMap map[string]*ScalarInfo, isInput bool, enumLookup codegen.EnumLookup, importAliases map[string]string, unionNames map[string]bool) []FieldInfo {
	fields := make([]FieldInfo, len(codegenFields))
	for i, cf := range codegenFields {
		isUnion := cf.IsUnion || unionNames[cf.Type]
		var scalarInfo *ScalarInfo
		if cf.IsScalar && cf.ScalarInfo != nil {
			scalarInfo = scalarMap[cf.ScalarInfo.Name]
		}
		usesWrapper := isInput && !cf.Required
		// The pointer edits below never apply to a list: the elements of
		// T[] and T[][] are value types, so [][]*T cannot arise.
		goType := normalizeImportedGoType(cf.TargetType, importAliases)
		if !isInput && cf.IsScalar && !cf.Required && cf.ScalarInfo != nil && cf.ScalarInfo.Traits.IsIntegerLike {
			goType = strings.TrimPrefix(goType, "*")
		}
		// A Go union is an interface and already nilable. A pointer to it is
		// not idiomatic and cannot hold the union wrapper's Value.
		if !isInput && isUnion && !cf.Required && !cf.IsArray && !cf.IsMap {
			goType = strings.TrimPrefix(goType, "*")
		}
		// A map of union values stores the interface values directly, input
		// or not; the optional nested-type path would add a pointer per value.
		if isUnion && cf.IsMap && !cf.Required {
			goType = strings.Replace(goType, "]*", "]", 1)
		}
		if usesWrapper {
			innerType := goType
			if cf.IsScalar || (isUnion && !cf.IsArray && !cf.IsMap) {
				innerType = strings.TrimPrefix(innerType, "*")
			}
			goType = fmt.Sprintf("InputField[%s]", innerType)
		}

		field := FieldInfo{
			Name:             cf.Name,
			GoName:           cf.TargetName,
			Type:             cf.Type,
			GoType:           goType,
			Required:         cf.Required,
			InternalMetadata: cf.InternalMetadata,
			IsInput:          isInput,
			UsesWrapper:      usesWrapper,
			Secret:           cf.Secret,
			IsArray:          cf.IsArray,
			IsArrayOfArrays:  cf.IsArrayOfArrays,
			IsMap:            cf.IsMap,
			IsScalar:         cf.IsScalar,
			IsUnion:          isUnion,
			Doc:              cf.Doc(),
			ScalarInfo:       scalarInfo,
			Validations:      cf.Validations,
		}

		if cf.Default != nil {
			kind := codegen.ClassifyDefault(cf, enumLookup)
			if kind != codegen.DefaultLiteralUnknown {
				if literal, ok := formatGoDefaultLiteral(*cf.Default, kind, field); ok && literal != "" {
					field.HasDefault = true
					field.DefaultLiteral = literal
				}
			}
		}

		fields[i] = field
	}
	return fields
}

func importAliasReplacements(imports []ModuleImport) map[string]string {
	replacements := make(map[string]string, len(imports))
	for _, imp := range imports {
		serviceName := path.Base(imp.Path)
		if serviceName != "." && serviceName != "/" && imp.Alias != "" {
			replacements[serviceName] = imp.Alias
		}
	}
	return replacements
}

func normalizeImportedGoType(goType string, importAliases map[string]string) string {
	for serviceName, alias := range importAliases {
		goType = strings.ReplaceAll(goType, serviceName+".", alias+".")
	}
	return goType
}

func buildScalarMap(scalars []ScalarInfo) map[string]*ScalarInfo {
	m := make(map[string]*ScalarInfo, len(scalars))
	for i := range scalars {
		m[scalars[i].Name] = &scalars[i]
	}
	return m
}

// allDependencyModulePaths returns the sorted, de-duplicated module paths of
// the dependencies this module imports and of every declared dependency,
// without the module itself.
func allDependencyModulePaths(selfModulePath string, used []string, declared map[string]string) []string {
	unique := make(map[string]struct{}, len(used)+len(declared))
	for _, modulePath := range used {
		if modulePath != "" && modulePath != selfModulePath {
			unique[modulePath] = struct{}{}
		}
	}
	for _, modulePath := range declared {
		if modulePath != "" && modulePath != selfModulePath {
			unique[modulePath] = struct{}{}
		}
	}
	modules := make([]string, 0, len(unique))
	for modulePath := range unique {
		modules = append(modules, modulePath)
	}
	sort.Strings(modules)
	return modules
}

// moduleDependencyReplaces builds go.mod replace directives for sibling type
// modules. All generated type packages live under <root>/types/go/<name>/,
// so the relative path is always ../<last-path-segment>.
func moduleDependencyReplaces(deps []string) []ModuleDependencyReplace {
	if len(deps) == 0 {
		return nil
	}

	replaces := make([]ModuleDependencyReplace, 0, len(deps))
	for _, dep := range deps {
		replaces = append(replaces, ModuleDependencyReplace{
			Module:  dep,
			RelPath: "../" + filepath.Base(dep),
		})
	}
	return replaces
}

// buildTypePairs discovers matching input/output type pairs and builds
// conversion metadata for ToType() generation.
func buildTypePairs(types []TypeInfo, importedTypes []ImportedTypeInfo) []TypePair {
	objectMap := make(map[string]*TypeInfo)
	inputMap := make(map[string]*TypeInfo)
	for i := range types {
		t := &types[i]
		if t.IsInput {
			inputMap[t.Name] = t
		} else {
			objectMap[t.Name] = t
		}
	}
	for _, imp := range importedTypes {
		if imp.IsInput {
			inputMap[imp.Name] = nil
		} else {
			objectMap[imp.Name] = nil
		}
	}

	pairedInputNames := make(map[string]bool)
	for inputName := range inputMap {
		objectName := strings.TrimSuffix(inputName, "Input")
		if objectName != inputName {
			if _, exists := objectMap[objectName]; exists {
				pairedInputNames[inputName] = true
			}
		}
	}

	var pairs []TypePair
	for inputName := range pairedInputNames {
		inputType := inputMap[inputName]
		objectName := strings.TrimSuffix(inputName, "Input")
		objectType := objectMap[objectName]
		if inputType == nil || objectType == nil {
			continue
		}
		if !objectType.IsJsonField {
			continue
		}

		objectFieldMap := make(map[string]*FieldInfo)
		for i := range objectType.Fields {
			objectFieldMap[objectType.Fields[i].Name] = &objectType.Fields[i]
		}

		var fields []TypePairField
		for _, inputField := range inputType.Fields {
			objectField, ok := objectFieldMap[inputField.Name]
			if !ok {
				continue
			}

			pf := TypePairField{
				GoName:          inputField.GoName,
				InputGoType:     inputField.GoType,
				GoType:          objectField.GoType,
				Required:        inputField.Required,
				UsesWrapper:     inputField.UsesWrapper,
				IsArray:         inputField.IsArray,
				IsArrayOfArrays: inputField.IsArrayOfArrays,
				IsScalar:        inputField.IsScalar,
			}

			innerInputType := unwrapGoType(inputField.GoType)
			innerObjectType := unwrapGoType(objectField.GoType)
			if innerInputType != innerObjectType {
				pairedObjName := strings.TrimSuffix(innerInputType, "Input")
				if pairedObjName != innerInputType && pairedObjName == innerObjectType {
					pf.IsPaired = true
					pf.PairedTypeName = innerObjectType
				}
			}

			fields = append(fields, pf)
		}

		pairs = append(pairs, TypePair{
			InputName:  inputName,
			OutputName: objectName,
			Fields:     fields,
		})
	}

	sort.Slice(pairs, func(i, j int) bool {
		return pairs[i].InputName < pairs[j].InputName
	})

	return pairs
}

// unwrapGoType strips pointer, slice, and InputField wrappers from a Go type string.
func unwrapGoType(goType string) string {
	s := goType
	if strings.HasPrefix(s, "InputField[") {
		s = strings.TrimPrefix(s, "InputField[")
		s = strings.TrimSuffix(s, "]")
	}
	for {
		prev := s
		s = strings.TrimPrefix(s, "[]")
		s = strings.TrimPrefix(s, "*")
		if s == prev {
			break
		}
	}
	return s
}
