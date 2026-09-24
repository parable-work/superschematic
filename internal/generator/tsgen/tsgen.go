// Package tsgen generates the TypeScript type package for a schema: pure
// type declarations (types/), per-scalar and per-type validators
// (validators/), and secret-masking helpers (mask/), plus the package.json /
// tsconfig.json scaffolding to compile it.
//
// This is the v2 port of the v1 tsgen generator onto the refactored Schema
// IR: types are selected by TypeDef.Role, inherited fields arrive
// pre-flattened, imported definitions come from the schema's named imports
// plus loaded dependency schemas (no wildcard-import heuristics), scalar
// custom-implementation flags come from the IR rather than filesystem
// detection, and node comments become doc comments.
package tsgen

import (
	"embed"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

//go:embed templates/*.tmpl
var templatesFS embed.FS

// localObjectRoles are the type roles emitted as TypeScript interfaces.
var localObjectRoles = []ir.Role{ir.RoleDBTable, ir.RoleAPIView, ir.RoleEmbeddedStruct}

// ScalarInfo holds information about a scalar type for TypeScript generation.
type ScalarInfo struct {
	Name                         string
	Symbol                       string
	Module                       string
	TSType                       string
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

	// HasJSONParse marks a custom-parse scalar with a JSON shape (a map such
	// as Generic.StringMap): superscalar's parser takes a value or its JSON
	// text and returns the decoded value, so the validator runs it instead of
	// string checks.
	HasJSONParse bool

	// IsIntegerLike marks number scalars with integer semantics; validators
	// emit a Number.isInteger check for them.
	IsIntegerLike bool

	// HasParseFromJSON marks DateTime-like scalars with a superscalar parse
	// implementation; parseFromJSON helpers convert their wire strings into
	// runtime Date values.
	HasParseFromJSON bool
}

// FieldInfo holds information about a struct field for TypeScript generation.
// The schema field name is the JSON name; TypeScript keeps it as-is.
type FieldInfo struct {
	Name             string
	TSName           string
	Type             string
	TSType           string
	Required         bool
	InternalMetadata bool
	Secret           bool
	IsArray          bool
	IsMap            bool
	IsScalar         bool
	Doc              string
	ScalarInfo       *ScalarInfo
	Validations      []codegen.ValidationRule

	// IsArrayOfArrays marks T[][]: IsArray is also set, TSType is T[][] and
	// Type is the innermost element type. Validators check every innermost
	// element, list bounds apply to the outer list, and an inner list must
	// be an array, never null.
	IsArrayOfArrays bool

	// HasDefault is true when @default was declared on the field and a
	// TypeScript literal could be produced for it.
	HasDefault bool

	// DefaultLiteral is the TypeScript expression for the field's @default.
	DefaultLiteral string
}

// TypeInfo holds information about a complex type for TypeScript generation.
type TypeInfo struct {
	Name   string
	Owner  string
	Role   ir.Role
	Doc    string
	Fields []FieldInfo

	// StrictJSON is set by @strictJSON: the validator rejects an undeclared
	// key and validates nested object fields, and parse<Type>FromJSON runs
	// the strict JSON parser.
	StrictJSON bool

	// HasDefaults is true when at least one field has a usable
	// DefaultLiteral. Templates use this flag to decide whether to emit a
	// make<Type>() factory and merge defaults during JSON parsing.
	HasDefaults bool
}

// UnionInfo holds information about a union type for TypeScript generation.
type UnionInfo struct {
	Name  string
	Doc   string
	Types []string
}

// ImportedTypeInfo holds alias metadata for definitions owned by a
// dependency package.
type ImportedTypeInfo struct {
	Name          string
	Doc           string
	ImportAlias   string
	ImportPackage string
	IsEnum        bool
	IsObject      bool
}

// TypeImport describes a type-only import required by imported aliases.
type TypeImport struct {
	Alias          string
	Path           string
	DependencyName string
}

// PackageDependency describes an imported types package that package.json
// must install before tsc can resolve package subpath imports.
type PackageDependency struct {
	Name string
	Spec string
}

// ModuleOutput contains all generated code for a types module.
type ModuleOutput struct {
	PackageName         string
	SchemaName          string
	Scalars             []ScalarInfo
	Types               []TypeInfo
	ImportedTypes       []ImportedTypeInfo
	TypeImports         []TypeImport
	PackageDependencies []PackageDependency
	Unions              []UnionInfo
	Enums               []codegen.EnumInfo
	CompositeDefaults   []CompositeDefaultInfo
	Timestamp           string
	HasVersionedTypes   bool

	// ScalarLibSpec is the package.json dependency spec for the TypeScript
	// superscalar runtime package, computed relative to the output directory
	// via SetScalarLibSpec.
	ScalarLibSpec string

	// Naming supplies the scalar package name the templates import from.
	Naming naming.Naming
}

// CompositeDefaultInfo is one generated fresh-value accessor.
type CompositeDefaultInfo struct {
	FunctionName string
	Type         string
	JSONLiteral  string
}

// UnionMemberTypeNames returns the sorted unique member type names across all
// unions, for the type-only import at the top of unions.ts. Members resolve
// against types.ts, which exports both local types and imported aliases.
func (o *ModuleOutput) UnionMemberTypeNames() []string {
	seen := map[string]bool{}
	var names []string
	for _, u := range o.Unions {
		for _, member := range u.Types {
			if seen[member] {
				continue
			}
			seen[member] = true
			names = append(names, member)
		}
	}
	sort.Strings(names)
	return names
}

// ParseableTypeNames returns the set of type names the generated TypeScript
// package can parse from JSON (every type gets a parse<Type>FromJSON helper).
// The SDK generator uses it to emit typed response parsing.
func ParseableTypeNames(output *ModuleOutput) map[string]bool {
	names := make(map[string]bool, len(output.Types))
	for _, t := range output.Types {
		names[t.Name] = true
	}
	return names
}

// SetScalarLibSpec computes the package.json dependency spec for the
// scalar library's npm package as a file: path relative to outputDir. An
// unset path leaves the spec empty so package.json names the published
// version instead.
func SetScalarLibSpec(output *ModuleOutput, paths naming.LocalPaths, outputDir string) error {
	rel, err := naming.RelPath(outputDir, paths.ScalarTypeScript)
	if err != nil {
		return fmt.Errorf("scalar library package path: %w", err)
	}
	if rel == "" {
		output.ScalarLibSpec = ""
		return nil
	}
	output.ScalarLibSpec = "file:" + rel
	return nil
}

// Options configures TypeScript type generation.
type Options struct {
	// SchemaName is the service name (e.g. "web-db").
	SchemaName string

	// Dependencies maps dependency service names to their loaded schemas,
	// used to classify imported symbols.
	Dependencies map[string]*ir.Schema

	// DependencyPackages maps dependency service names to their generated
	// npm package names. Defaults to Naming.NpmTypesPackage(name).
	DependencyPackages map[string]string

	// Naming supplies the npm scope and scalar package name. Empty fields
	// fall back to naming.Default().
	Naming naming.Naming

	// Clock stamps the generated output.
	Clock codegen.Clock
}

// Generate generates TypeScript types from a v2 IR schema.
func Generate(schema *ir.Schema, opts Options) (*ModuleOutput, error) {
	if opts.Clock == nil {
		opts.Clock = codegen.DefaultClock()
	}
	opts.Naming = opts.Naming.OrDefault()

	output := &ModuleOutput{
		PackageName: opts.Naming.NpmTypesPackage(opts.SchemaName),
		SchemaName:  opts.SchemaName,
		Naming:      opts.Naming,
		Timestamp:   opts.Clock.RFC3339(),
	}

	extraction := codegen.ExtractionConfig{
		Language:        "typescript",
		PrimitiveMapper: inferTSType,
		FieldTypeMapper: fieldTypeMapperTS,
		FieldNameMapper: func(name string) string { return name },
		ScalarPostProcessor: func(scalar *codegen.ScalarInfo, _ *ir.ScalarDef) {
			if scalar.Traits.IsDateTimeLike {
				scalar.TargetType = "JSDate"
			}
		},
	}

	codegenScalars := codegen.ExtractScalars(schema, extraction)
	output.Scalars = convertScalars(codegenScalars)

	imported, err := resolveImports(schema, opts)
	if err != nil {
		return nil, err
	}
	output.ImportedTypes = imported.types
	output.TypeImports = imported.imports
	output.PackageDependencies = imported.packageDependencies

	output.Enums = codegen.ExtractEnums(schema)
	output.Unions = convertUnions(codegen.ExtractUnions(schema))
	for _, def := range codegen.ExtractCompositeDefaults(schema) {
		output.CompositeDefaults = append(output.CompositeDefaults, CompositeDefaultInfo{
			FunctionName: "default" + codegen.ToPascalCase(def.Name),
			Type:         def.Type,
			JSONLiteral:  strconv.Quote(def.CanonicalJSON),
		})
	}

	enumLookup := buildEnumLookup(output.Enums, imported.enumNames)

	objectTypes := codegen.ExtractTypes(schema, codegenScalars, extraction, localObjectRoles...)
	objectTypes = codegen.AddVersionFields(objectTypes, extraction)
	inputTypes := codegen.ExtractTypes(schema, codegenScalars, extraction, ir.RoleAPIInput)

	output.Types = convertTypes(objectTypes, output.Scalars, enumLookup)
	output.Types = append(output.Types, convertTypes(inputTypes, output.Scalars, enumLookup)...)
	for _, typeInfo := range output.Types {
		if typeInfo.Role != ir.RoleDBTable {
			continue
		}
		for _, field := range typeInfo.Fields {
			if field.Name == "_version" && field.InternalMetadata {
				output.HasVersionedTypes = true
				break
			}
		}
	}

	return output, nil
}

// buildEnumLookup reports whether a type name refers to a local or imported enum.
func buildEnumLookup(localEnums []codegen.EnumInfo, importedEnumNames map[string]bool) codegen.EnumLookup {
	known := make(map[string]struct{}, len(localEnums)+len(importedEnumNames))
	for _, e := range localEnums {
		known[e.Name] = struct{}{}
	}
	for name := range importedEnumNames {
		known[name] = struct{}{}
	}
	return func(typeName string) bool {
		_, ok := known[typeName]
		return ok
	}
}

// convertScalars converts codegen.ScalarInfo to tsgen.ScalarInfo.
func convertScalars(codegenScalars []codegen.ScalarInfo) []ScalarInfo {
	scalars := make([]ScalarInfo, len(codegenScalars))
	for i, s := range codegenScalars {
		scalars[i] = ScalarInfo{
			Name:                         s.Name,
			Symbol:                       s.Tokens.Symbol,
			Module:                       s.Tokens.Module,
			TSType:                       s.TargetType,
			Primitive:                    s.Primitive,
			Doc:                          codegen.DocText(s.Description, s.Comment),
			MaxLength:                    s.MaxLength,
			MinLength:                    s.MinLength,
			Pattern:                      s.Pattern,
			Format:                       s.Format,
			CaseInsensitive:              s.CaseInsensitive,
			ReservedWords:                s.ReservedWords,
			ReservedWordsCaseInsensitive: s.ReservedWordsCaseInsensitive,
			ReservedWordsMatchPartial:    s.ReservedWordsMatchPartial,
			Minimum:                      s.Minimum,
			Maximum:                      s.Maximum,
			Example:                      s.Example,
			HasCustomNormalize:           s.HasCustomNormalize,
			HasCustomValidate:            s.HasCustomValidate,
			HasCustomParse:               s.HasCustomParse,
			HasJSONParse:                 s.HasCustomParse && s.Traits.IsJSONLike,
			IsIntegerLike:                s.Traits.IsIntegerLike,
			HasParseFromJSON:             s.HasCustomParse && s.TargetType == "JSDate",
		}
	}
	return scalars
}

// convertUnions converts codegen.UnionInfo to tsgen.UnionInfo.
func convertUnions(codegenUnions []codegen.UnionInfo) []UnionInfo {
	unions := make([]UnionInfo, len(codegenUnions))
	for i, u := range codegenUnions {
		unions[i] = UnionInfo{
			Name:  u.Name,
			Doc:   u.Doc(),
			Types: u.Types,
		}
	}
	return unions
}

// convertTypes converts codegen.TypeInfo to tsgen.TypeInfo.
func convertTypes(codegenTypes []codegen.TypeInfo, scalars []ScalarInfo, enumLookup codegen.EnumLookup) []TypeInfo {
	scalarMap := make(map[string]*ScalarInfo, len(scalars))
	for i := range scalars {
		scalarMap[scalars[i].Name] = &scalars[i]
	}

	types := make([]TypeInfo, len(codegenTypes))
	for i, t := range codegenTypes {
		fields := convertFields(t.Fields, scalarMap, enumLookup)
		hasDefaults := false
		for _, f := range fields {
			if f.HasDefault && f.DefaultLiteral != "" {
				hasDefaults = true
				break
			}
		}
		types[i] = TypeInfo{
			Name:        t.Name,
			Owner:       t.Owner,
			Role:        t.Role,
			Doc:         t.Doc(),
			Fields:      fields,
			StrictJSON:  t.StrictJSON,
			HasDefaults: hasDefaults,
		}
	}
	return types
}

// convertFields converts codegen.FieldInfo to tsgen.FieldInfo.
func convertFields(codegenFields []codegen.FieldInfo, scalarMap map[string]*ScalarInfo, enumLookup codegen.EnumLookup) []FieldInfo {
	fields := make([]FieldInfo, len(codegenFields))
	for i, f := range codegenFields {
		var scalarInfo *ScalarInfo
		if f.IsScalar {
			scalarInfo = scalarMap[f.Type]
		}

		field := FieldInfo{
			Name:             f.Name,
			TSName:           f.TargetName,
			Type:             f.Type,
			TSType:           f.TargetType,
			Required:         f.Required,
			InternalMetadata: f.InternalMetadata,
			Secret:           f.Secret,
			IsArray:          f.IsArray,
			IsArrayOfArrays:  f.IsArrayOfArrays,
			IsMap:            f.IsMap,
			IsScalar:         f.IsScalar,
			Doc:              f.Doc(),
			ScalarInfo:       scalarInfo,
			Validations:      f.Validations,
		}

		if f.Default != nil {
			kind := codegen.ClassifyDefault(f, enumLookup)
			if kind != codegen.DefaultLiteralUnknown {
				if literal, ok := formatTSDefaultLiteral(*f.Default, kind, field); ok && literal != "" {
					field.HasDefault = true
					field.DefaultLiteral = literal
				}
			}
		}

		fields[i] = field
	}
	return fields
}

// inferTSType infers a TypeScript type from the host-language primitive when
// the scalar has no TypeScript TypeMappings entry.
func inferTSType(primitive ir.LanguagePrimitive, scalarName string) string {
	traits := codegen.GuessScalarTraits(scalarName)
	switch primitive {
	case ir.LanguageString:
		if traits.IsDateTimeLike {
			return "JSDate"
		}
		if traits.IsJSONLike || traits.IsObjectLike {
			return "Record<string, any>"
		}
		if traits.IsLocationLike {
			return "{ lat: number; lon: number }"
		}
		return "string"
	case ir.LanguageNumber:
		return "number"
	case ir.LanguageBoolean:
		return "boolean"
	case ir.LanguageObject:
		return "Record<string, any>"
	}
	return "string"
}

// fieldTypeMapperTS maps IR type references to TypeScript types.
func fieldTypeMapperTS(typeName string, arrayDepth int, isMap bool, isRequired bool, scalarMap codegen.ScalarMap) string {
	valueType := fieldValueTypeMapperTS(typeName, arrayDepth, isMap, isRequired, scalarMap)
	if !isMap {
		return valueType
	}
	return "Record<string, " + valueType + ">"
}

func fieldValueTypeMapperTS(typeName string, arrayDepth int, inMap bool, isRequired bool, scalarMap codegen.ScalarMap) string {
	if arrayDepth > 0 {
		elemType := fieldValueTypeMapperTS(typeName, 0, false, true, scalarMap)
		if inMap && !isRequired {
			elemType = "(" + elemType + " | null)"
		}
		return codegen.WrapArray(elemType, arrayDepth, func(elem string) string { return elem + "[]" })
	}

	var resolvedType string
	if scalar, ok := scalarMap[typeName]; ok {
		resolvedType = scalar.TargetType
	} else {
		switch typeName {
		case codegen.PrimitiveString:
			resolvedType = "string"
		case codegen.PrimitiveNumber:
			resolvedType = "number"
		case codegen.PrimitiveBoolean:
			resolvedType = "boolean"
		default:
			resolvedType = typeName
		}
	}

	if inMap && !isRequired {
		return resolvedType + " | null"
	}

	return resolvedType
}

// builtinTSTypeNames are TypeScript type names not exported from the types package.
var builtinTSTypeNames = map[string]bool{
	"string": true, "number": true, "boolean": true,
	"Date": true, "Object": true,
}

// isImportableType returns true if the type name can be imported (not a
// primitive, generic, array, or composite type expression).
func isImportableType(typeName string) bool {
	if builtinTSTypeNames[typeName] {
		return false
	}
	if strings.Contains(typeName, "<") || strings.Contains(typeName, ">") {
		return false
	}
	if strings.HasSuffix(typeName, "[]") {
		return false
	}
	if strings.Contains(typeName, "|") || strings.Contains(typeName, "&") || strings.Contains(typeName, "{") {
		return false
	}
	return true
}
