// Package pygen generates the Python type package for a schema: pydantic v2
// models (types.py), scalar aliases with validation (scalars.py), enums
// (enums.py), union aliases (unions.py), and the pyproject/setup packaging
// scaffolding.
//
// This is the v2 port of the v1 pygen generator onto the refactored Schema
// IR: types are selected by TypeDef.Role, inherited fields arrive
// pre-flattened, imported definitions come from the schema's named imports
// plus loaded dependency schemas (no wildcard-import or owner-path
// heuristics), scalar custom-implementation flags come from the IR rather
// than filesystem detection, and node comments become doc comments.
package pygen

import (
	"embed"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

//go:embed templates/*.tmpl
var templatesFS embed.FS

// localObjectRoles are the type roles emitted as pydantic models.
var localObjectRoles = []ir.Role{ir.RoleDBTable, ir.RoleAPIView, ir.RoleEmbeddedStruct}

var pythonKeywords = map[string]struct{}{
	"False": {}, "None": {}, "True": {}, "and": {}, "as": {}, "assert": {},
	"async": {}, "await": {}, "break": {}, "class": {}, "continue": {}, "def": {},
	"del": {}, "elif": {}, "else": {}, "except": {}, "finally": {}, "for": {},
	"from": {}, "global": {}, "if": {}, "import": {}, "in": {}, "is": {},
	"lambda": {}, "nonlocal": {}, "not": {}, "or": {}, "pass": {}, "raise": {},
	"return": {}, "try": {}, "while": {}, "with": {}, "yield": {}, "match": {},
	"case": {},
}

// ImportedTypeInfo describes a definition owned by a dependency package that
// this module re-binds by name (object type, enum, or union).
type ImportedTypeInfo struct {
	Name       string
	ModuleName string
}

// TypeImport groups the imported symbol names per dependency Python module.
type TypeImport struct {
	ModuleName string
	TypeNames  []string
}

// ModuleOutput contains all generated code for a Python types module.
type ModuleOutput struct {
	SchemaName        string
	PythonModuleName  string
	Scalars           []codegen.ScalarInfo
	Types             []codegen.TypeInfo
	ImportedTypes     []ImportedTypeInfo
	TypeImports       []TypeImport
	DependencyModules []string
	Unions            []codegen.UnionInfo
	Enums             []codegen.EnumInfo
	CompositeDefaults []CompositeDefaultInfo
	Timestamp         string

	// ImportedEnumNames lists imported enum names so templates and default
	// classification can distinguish dependency-owned enums from models.
	ImportedEnumNames []string

	// HasDateTime is true when a scalar maps to Python datetime; scalars.py
	// imports the datetime module for it.
	HasDateTime bool

	// HasGenericJSON is true when the module uses Generic.JSON; scalars.py
	// imports json and math for its host-value validator.
	HasGenericJSON bool

	// HasJSONParse is true when a custom parser takes and returns JSON
	// values (pythonParsesJSON); scalars.py imports json for it.
	HasJSONParse bool

	// Custom superscalar implementation flags (any scalar).
	HasCustomNormalize bool
	HasCustomValidate  bool
	HasCustomParse     bool

	// NeedsLiteralImport is true when generated types use typing.Literal for
	// discriminated-union member discriminator fields.
	NeedsLiteralImport bool

	// ModelRebuildTypes lists locally-defined pydantic models that must call
	// model_rebuild() after unions and dependency packages are imported.
	ModelRebuildTypes []string

	// UnionDependencyModules lists imported packages whose types must be
	// merged into the model_rebuild namespace.
	UnionDependencyModules []string

	// Naming supplies the scalar library's PyPI and import names.
	Naming naming.Naming
}

// CompositeDefaultInfo is one generated fresh-value accessor.
type CompositeDefaultInfo struct {
	FunctionName string
	Type         string
	JSONLiteral  string
}

// UnionMemberTypeNames returns the sorted unique member type names across
// all unions, for the import at the top of unions.py. Members resolve
// against types.py, which binds both local models and imported names.
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

// ExportedTypeNames returns the sorted names re-exported from types.py:
// locally generated models plus imported bindings.
func (o *ModuleOutput) ExportedTypeNames() []string {
	names := make([]string, 0, len(o.Types)+len(o.ImportedTypes))
	for _, t := range o.Types {
		names = append(names, t.Name)
	}
	for _, t := range o.ImportedTypes {
		names = append(names, t.Name)
	}
	sort.Strings(names)
	return names
}

// Options configures Python type generation.
type Options struct {
	// SchemaName is the service name (e.g. "web-db").
	SchemaName string

	// Dependencies maps dependency service names to their loaded schemas,
	// used to classify imported symbols.
	Dependencies map[string]*ir.Schema

	// Naming supplies the module prefix and scalar library coordinates.
	// Empty fields fall back to naming.Default().
	Naming naming.Naming

	// Clock stamps the generated output.
	Clock codegen.Clock
}

// moduleStem folds a schema name to Python identifier characters: letters
// and digits lower-cased, everything else '_', runs and edges trimmed.
func moduleStem(schemaName string) string {
	var b strings.Builder
	for _, r := range schemaName {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			b.WriteRune(unicode.ToLower(r))
		default:
			b.WriteRune('_')
		}
	}
	stem := strings.Trim(b.String(), "_")
	return strings.ReplaceAll(stem, "__", "_")
}

// Generate generates Python types from a v2 IR schema.
func Generate(schema *ir.Schema, opts Options) (*ModuleOutput, error) {
	if opts.Clock == nil {
		opts.Clock = codegen.DefaultClock()
	}
	opts.Naming = opts.Naming.OrDefault()

	output := &ModuleOutput{
		SchemaName:       opts.SchemaName,
		PythonModuleName: opts.Naming.PythonTypesModule(moduleStem(opts.SchemaName)),
		Naming:           opts.Naming,
		Timestamp:        opts.Clock.RFC3339(),
	}

	extraction := codegen.ExtractionConfig{
		Language:            "python",
		PrimitiveMapper:     inferPythonType,
		FieldTypeMapper:     fieldTypeMapperPython,
		FieldNameMapper:     toPythonFieldName,
		ScalarPostProcessor: refinePythonScalarType,
	}

	output.Scalars = codegen.ExtractScalars(schema, extraction)
	for _, scalar := range output.Scalars {
		if scalar.Name == genericJSONScalar {
			output.HasGenericJSON = true
		}
		if scalar.Traits.IsDateTimeLike {
			output.HasDateTime = true
		}
		if scalar.HasCustomNormalize {
			output.HasCustomNormalize = true
		}
		if scalar.HasCustomValidate {
			output.HasCustomValidate = true
		}
		if scalar.HasCustomParse {
			output.HasCustomParse = true
		}
		if pythonParsesJSON(scalar) {
			output.HasJSONParse = true
		}
	}

	imported, err := resolveImports(schema, opts)
	if err != nil {
		return nil, err
	}
	output.ImportedTypes = imported.types
	output.TypeImports = imported.imports
	for _, typeImport := range imported.imports {
		output.DependencyModules = append(output.DependencyModules, typeImport.ModuleName)
	}
	for name := range imported.enumNames {
		output.ImportedEnumNames = append(output.ImportedEnumNames, name)
	}
	sort.Strings(output.ImportedEnumNames)

	output.Enums = codegen.ExtractEnums(schema)
	output.Unions = codegen.ExtractUnions(schema)
	for _, def := range codegen.ExtractCompositeDefaults(schema) {
		output.CompositeDefaults = append(output.CompositeDefaults, CompositeDefaultInfo{
			FunctionName: "default_" + codegen.ToSnakeCase(def.Name),
			Type:         def.Type,
			JSONLiteral:  strconv.Quote(def.CanonicalJSON),
		})
	}

	objectTypes := codegen.ExtractTypes(schema, output.Scalars, extraction, localObjectRoles...)
	inputTypes := codegen.ExtractTypes(schema, output.Scalars, extraction, ir.RoleAPIInput)
	output.Types = append(objectTypes, inputTypes...)

	applyUnionDiscriminatorLiterals(output)
	collectModelRebuildTypes(output, imported.enumNames)

	return output, nil
}

// applyUnionDiscriminatorLiterals rewrites each discriminated-union member's
// discriminator field type to typing.Literal["<value>"] so pydantic v2 can
// dispatch by discriminator during validation.
func applyUnionDiscriminatorLiterals(output *ModuleOutput) {
	type memberDiscriminator struct {
		field string
		value string
	}
	discriminators := map[string]memberDiscriminator{}
	for _, union := range output.Unions {
		if union.Discriminator == "" {
			continue
		}
		for _, member := range union.Members {
			if member.DiscriminatorValue == "" {
				continue
			}
			discriminators[member.Name] = memberDiscriminator{
				field: union.Discriminator,
				value: member.DiscriminatorValue,
			}
		}
	}
	if len(discriminators) == 0 {
		return
	}

	for i := range output.Types {
		disc, ok := discriminators[output.Types[i].Name]
		if !ok {
			continue
		}
		for j := range output.Types[i].Fields {
			field := &output.Types[i].Fields[j]
			if field.Name != disc.field {
				continue
			}
			field.TargetType = fmt.Sprintf("Literal[%q]", disc.value)
			output.NeedsLiteralImport = true
		}
	}
}

// collectModelRebuildTypes finds local models that need model_rebuild() after
// import: models with union-typed fields (the union alias is defined in
// unions.py, after types.py) and models with fields typed by imported models
// (bound to Any shims until the dependency package is importable).
func collectModelRebuildTypes(output *ModuleOutput, importedEnumNames map[string]bool) {
	importedByName := make(map[string]ImportedTypeInfo, len(output.ImportedTypes))
	for _, imported := range output.ImportedTypes {
		importedByName[imported.Name] = imported
	}
	localEnums := make(map[string]bool, len(output.Enums))
	for _, e := range output.Enums {
		localEnums[e.Name] = true
	}

	depModules := map[string]struct{}{}
	var rebuildTypes []string

	for _, typeInfo := range output.Types {
		needsRebuild := false
		for _, field := range typeInfo.Fields {
			if field.IsUnion {
				needsRebuild = true
				continue
			}
			imported, ok := importedByName[field.Type]
			if !ok || field.IsScalar || localEnums[field.Type] || importedEnumNames[field.Type] {
				continue
			}
			needsRebuild = true
			depModules[imported.ModuleName] = struct{}{}
		}
		if needsRebuild {
			rebuildTypes = append(rebuildTypes, typeInfo.Name)
		}
	}

	sort.Strings(rebuildTypes)
	output.ModelRebuildTypes = rebuildTypes

	if len(depModules) == 0 {
		return
	}
	modules := make([]string, 0, len(depModules))
	for moduleName := range depModules {
		modules = append(modules, moduleName)
	}
	sort.Strings(modules)
	output.UnionDependencyModules = modules
}

// toPythonFieldName converts a schema field name to snake_case. Pydantic
// reserves leading underscores for private attributes, so wire names that
// begin with an underscore use a legal model attribute and retain their
// original spelling through the generated Field alias.
func toPythonFieldName(name string) string {
	snakeName := codegen.ToSnakeCase(name)
	if strings.HasPrefix(snakeName, "_") {
		snakeName = strings.TrimLeft(snakeName, "_") + "_"
	}
	if _, isKeyword := pythonKeywords[snakeName]; isKeyword {
		return snakeName + "_"
	}
	return snakeName
}

// inferPythonType infers a Python type from the host-language primitive when
// the scalar has no Python TypeMappings entry.
func inferPythonType(primitive ir.LanguagePrimitive, scalarName string) string {
	traits := codegen.GuessScalarTraits(scalarName)
	switch primitive {
	case ir.LanguageString:
		if traits.IsDateTimeLike {
			return "datetime.datetime"
		}
		if traits.IsJSONLike || traits.IsLocationLike || traits.IsObjectLike {
			return "dict"
		}
		return "str"
	case ir.LanguageNumber:
		if traits.IsIntegerLike {
			return "int"
		}
		return "float"
	case ir.LanguageBoolean:
		return "bool"
	case ir.LanguageObject:
		return "dict"
	}
	return "str"
}

func refinePythonScalarType(scalar *codegen.ScalarInfo, scalarDef *ir.ScalarDef) {
	if _, hasMapping := scalarDef.TypeMappings["python"]; hasMapping {
		return
	}
	semanticPrimitive := strings.ToLower(strings.TrimSpace(scalarDef.Primitive))
	if scalar.Traits.IsIntegerLike ||
		semanticPrimitive == "int" ||
		semanticPrimitive == "int64" ||
		semanticPrimitive == "integer" {
		scalar.TargetType = "int"
	}
}

// fieldTypeMapperPython maps IR type references to Python type strings.
func fieldTypeMapperPython(typeName string, isArray bool, isMap bool, isRequired bool, scalarMap codegen.ScalarMap) string {
	valueType := fieldValueTypeMapperPython(typeName, isArray, isMap, isRequired, scalarMap)
	if !isMap {
		return valueType
	}
	return "Dict[str, " + valueType + "]"
}

func fieldValueTypeMapperPython(typeName string, isArray bool, inMap bool, isRequired bool, scalarMap codegen.ScalarMap) string {
	if isArray {
		elemType := fieldValueTypeMapperPython(typeName, false, false, true, scalarMap)
		if inMap && !isRequired {
			elemType = elemType + " | None"
		}
		return "List[" + elemType + "]"
	}

	var resolvedType string
	if scalar, ok := scalarMap[typeName]; ok {
		if symbol := pythonScalarSymbol(*scalar); symbol != "" {
			resolvedType = symbol
		} else {
			resolvedType = scalar.Name
		}
	} else {
		switch typeName {
		case codegen.PrimitiveString:
			resolvedType = "str"
		case codegen.PrimitiveNumber:
			resolvedType = "float"
		case codegen.PrimitiveBoolean:
			resolvedType = "bool"
		default:
			resolvedType = typeName
		}
	}

	if inMap && !isRequired {
		return resolvedType + " | None"
	}

	return resolvedType
}

// pythonScalarSymbol returns the identifier-safe Python symbol for a scalar
// (e.g. "Identity.UUID" -> "IdentityUUID").
func pythonScalarSymbol(s codegen.ScalarInfo) string {
	if symbol := strings.TrimSpace(s.Tokens.Symbol); symbol != "" {
		return symbol
	}

	name := strings.TrimSpace(s.Name)
	if name == "" {
		return ""
	}

	var b strings.Builder
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	symbol := b.String()
	if symbol == "" {
		return ""
	}
	firstRune, _ := utf8.DecodeRuneInString(symbol)
	if firstRune != utf8.RuneError && unicode.IsDigit(firstRune) {
		return "Scalar" + symbol
	}
	return symbol
}

// pythonScalarModule returns the snake_case module stem for a scalar (e.g.
// "Identity.UUID" -> "identity_uuid"), used to name superscalar functions.
func pythonScalarModule(s codegen.ScalarInfo) string {
	if module := strings.TrimSpace(s.Tokens.Module); module != "" {
		return module
	}
	return codegen.ToSnakeCase(pythonScalarSymbol(s))
}
