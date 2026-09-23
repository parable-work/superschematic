// Package rustgen generates the Rust type crate for a schema: scalar
// aliases (scalars.rs), enums (enums.rs), serde structs (types.rs), union
// enums (unions.rs), and the Cargo.toml / lib.rs crate scaffolding.
//
// This is the v2 port of the v1 rustgen generator onto the refactored Schema
// IR: types are selected by TypeDef.Role, inherited fields arrive
// pre-flattened, imported definitions come from the schema's named imports
// plus loaded dependency schemas (no wildcard-import or owner-path
// heuristics), union serde tagging derives from the IR's discriminator
// detection instead of GraphQL's __typename, and node comments become doc
// comments.
package rustgen

import (
	"embed"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/rustutil"
	ir "github.com/parable-work/superschematic/ir"
)

//go:embed templates/*.tmpl
var templatesFS embed.FS

// localObjectRoles are the type roles emitted as Rust structs.
var localObjectRoles = []ir.Role{ir.RoleDBTable, ir.RoleAPIView, ir.RoleEmbeddedStruct}

// ScalarInfo holds information about a scalar type for Rust generation.
type ScalarInfo struct {
	// Name is the identifier-safe Rust alias symbol (e.g. "IdentityUUID").
	Name string

	// CanonicalName is the schema's scalar identity (e.g. "Identity.UUID").
	CanonicalName string

	// RustType is the aliased Rust type expression.
	RustType string

	Doc                string
	HasCustomNormalize bool
	HasCustomValidate  bool
	HasCustomParse     bool
}

// FieldInfo holds information about a struct field for Rust generation.
type FieldInfo struct {
	Name        string
	RustName    string
	Type        string
	RustType    string
	SerdeRename string
	Required    bool
	Secret      bool
	IsArray     bool
	IsMap       bool
	IsScalar    bool
	IsUnion     bool
	Doc         string

	// SkipSerializing is true for union discriminator fields with a
	// renderable default. The enum wrapper writes the tag key, and the
	// default reconstructs the member field during deserialization.
	SkipSerializing bool

	// HasDefault is true when @default was declared on the field and a
	// Rust literal could be produced for it.
	HasDefault bool

	// DefaultLiteral is the Rust expression returned by the per-field
	// default helper, e.g. "8080_i64", "\"hello\".to_string()".
	DefaultLiteral string

	// DefaultFnName is the snake_case name of the generated free function
	// used by serde via #[serde(default = "...")].
	DefaultFnName string
}

// TypeInfo holds information about a complex type for Rust generation.
type TypeInfo struct {
	Name   string
	Owner  string
	Role   ir.Role
	Doc    string
	Fields []FieldInfo

	// HasDefaults is true when at least one field on this type has a
	// usable DefaultLiteral and every remaining required field has a Rust
	// type known to implement Default. The template uses this to decide
	// whether to emit a Default impl alongside the struct.
	HasDefaults bool

	// DenyUnknownFields emits #[serde(deny_unknown_fields)] on the struct,
	// for @denyUnknownFields and for @strictJSON.
	DenyUnknownFields bool
}

// UnionInfo holds information about a union type for Rust generation.
type UnionInfo struct {
	Name  string
	Doc   string
	Types []string

	// Discriminator is the shared member field used for serde's internal
	// tagging. Unions without a discriminator are emitted as untagged.
	Discriminator string

	// Members pairs each member type with its serde tag value. TagValue is
	// the member's discriminator literal from @default.
	Members []UnionMemberInfo
}

// UnionMemberInfo holds per-member serde tagging metadata.
type UnionMemberInfo struct {
	Name     string
	TagValue string
}

// ImportedTypeInfo holds alias metadata for definitions owned by a
// dependency crate. Object types, enums, and unions all become `pub type`
// aliases.
type ImportedTypeInfo struct {
	Name        string
	Doc         string
	ImportAlias string
}

// TypeImport describes a crate import required by imported aliases.
type TypeImport struct {
	Alias          string
	CrateName      string
	CrateModule    string
	DependencyName string
}

// CargoDependency describes a path dependency entry in Cargo.toml.
type CargoDependency struct {
	Name         string
	RelativePath string
}

// ExternalCrateDep describes a third-party crate dependency required by
// generated types.
type ExternalCrateDep struct {
	Name     string
	Version  string
	Features []string
}

// ModuleOutput contains all generated code for a Rust types crate.
type ModuleOutput struct {
	CrateName         string
	SchemaName        string
	Scalars           []ScalarInfo
	Types             []TypeInfo
	ImportedTypes     []ImportedTypeInfo
	TypeImports       []TypeImport
	CargoDependencies []CargoDependency
	ExternalCrateDeps []ExternalCrateDep
	Unions            []UnionInfo
	Enums             []codegen.EnumInfo
	CompositeDefaults []CompositeDefaultInfo
	Timestamp         string

	HasCustomNormalize bool
	HasCustomValidate  bool
	HasCustomParse     bool

	// UsesScalarLib is true when any generated type references the scalar
	// runtime crate (Naming.ScalarRustCrate).
	UsesScalarLib bool

	// ScalarLibDepPath is the Cargo.toml path entry for the scalar runtime
	// crate (an extension crate), computed relative to the output
	// directory via SetScalarLibPath.
	ScalarLibDepPath string

	// Naming supplies the crate prefix and the scalar crate coordinates.
	Naming naming.Naming

	// UsesHashMap is true when any field is a map type; types.rs imports
	// std::collections::HashMap for it.
	UsesHashMap bool

	// UsesUnions is true when any struct field references a union type;
	// types.rs imports crate::unions only in that case to avoid an
	// unused-import warning.
	UsesUnions bool
}

// CompositeDefaultInfo is one generated fresh-value accessor.
type CompositeDefaultInfo struct {
	FunctionName string
	Type         string
	JSONLiteral  string
}

// Options configures Rust type generation.
type Options struct {
	// SchemaName is the service name (e.g. "web-db").
	SchemaName string

	// Dependencies maps dependency service names to their loaded schemas,
	// used to classify imported symbols.
	Dependencies map[string]*ir.Schema

	// DependencyCrates maps dependency service names to their generated
	// crate names. Defaults to Naming.RustTypesCrate(name).
	DependencyCrates map[string]string

	// Naming supplies the crate prefix and the scalar crate coordinates.
	// Empty fields fall back to naming.Default().
	Naming naming.Naming

	// Clock stamps the generated output.
	Clock codegen.Clock
}

// Generate generates Rust types from a v2 IR schema.
func Generate(schema *ir.Schema, opts Options) (*ModuleOutput, error) {
	if opts.Clock == nil {
		opts.Clock = codegen.DefaultClock()
	}
	opts.Naming = opts.Naming.OrDefault()

	output := &ModuleOutput{
		CrateName:  opts.Naming.RustTypesCrate(opts.SchemaName),
		SchemaName: opts.SchemaName,
		Naming:     opts.Naming,
		Timestamp:  opts.Clock.RFC3339(),
	}

	extraction := codegen.ExtractionConfig{
		Language:            "rust",
		PrimitiveMapper:     inferRustType,
		FieldTypeMapper:     fieldTypeMapperRust,
		FieldNameMapper:     toRustFieldName,
		ScalarPostProcessor: refineRustScalarType,
	}

	codegenScalars := codegen.ExtractScalars(schema, extraction)
	output.Scalars = convertScalars(codegenScalars, opts.Naming.ScalarRustCrateIdent())
	for _, scalar := range output.Scalars {
		if scalar.HasCustomNormalize {
			output.HasCustomNormalize = true
		}
		if scalar.HasCustomValidate {
			output.HasCustomValidate = true
		}
		if scalar.HasCustomParse {
			output.HasCustomParse = true
		}
	}

	imported, err := resolveImports(schema, opts)
	if err != nil {
		return nil, err
	}
	output.ImportedTypes = imported.types
	output.TypeImports = imported.imports
	output.CargoDependencies = imported.cargoDependencies

	codegenUnions := codegen.ExtractUnions(schema)
	if err := validateUnionDiscriminatorDefaults(schema, codegenUnions); err != nil {
		return nil, err
	}
	output.Enums = codegen.ExtractEnums(schema)
	output.Unions = convertUnions(codegenUnions)
	for _, def := range codegen.ExtractCompositeDefaults(schema) {
		output.CompositeDefaults = append(output.CompositeDefaults, CompositeDefaultInfo{
			FunctionName: "default_" + codegen.ToSnakeCase(def.Name),
			Type:         def.Type,
			JSONLiteral:  strconv.Quote(def.CanonicalJSON),
		})
	}

	enumLookup := buildEnumLookup(output.Enums, imported.enumNames)
	// Imported enums participate in @default rendering (variant lookup needs
	// their values) but stay out of output.Enums so they are not re-emitted.
	defaultEnums := append(append([]codegen.EnumInfo{}, output.Enums...), imported.enums...)

	// Union member structs must not serialize their discriminator field:
	// the internally-tagged enum wrapper already writes the tag, and a
	// second copy of the key breaks serde round-tripping.
	discriminators := map[string]string{}
	for _, u := range output.Unions {
		if u.Discriminator == "" {
			continue
		}
		for _, member := range u.Types {
			discriminators[member] = u.Discriminator
		}
	}

	objectTypes := codegen.ExtractTypes(schema, codegenScalars, extraction, localObjectRoles...)
	inputTypes := codegen.ExtractTypes(schema, codegenScalars, extraction, ir.RoleAPIInput)

	output.Types = convertTypes(objectTypes, defaultEnums, enumLookup, discriminators)
	output.Types = append(output.Types, convertTypes(inputTypes, defaultEnums, enumLookup, discriminators)...)
	if err := validateRenderedUnionDiscriminatorDefaults(output.Types, output.Unions); err != nil {
		return nil, err
	}

	output.UsesHashMap = hasMapFields(output.Types)
	output.UsesUnions = hasUnionFields(output.Types)
	output.ExternalCrateDeps = collectExternalCrateDeps(output)
	output.UsesScalarLib = usesScalarLib(output)

	return output, nil
}

// SetScalarLibPath computes the Cargo.toml path entry for the scalar
// library's crate relative to outputDir. An unset path emits no path entry.
func SetScalarLibPath(output *ModuleOutput, paths naming.LocalPaths, outputDir string) error {
	rel, err := naming.RelPath(outputDir, paths.ScalarRust)
	if err != nil {
		return fmt.Errorf("scalar library crate path: %w", err)
	}
	output.ScalarLibDepPath = rel
	return nil
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

// convertScalars converts codegen.ScalarInfo to rustgen.ScalarInfo.
func convertScalars(codegenScalars []codegen.ScalarInfo, scalarCrate string) []ScalarInfo {
	scalars := make([]ScalarInfo, len(codegenScalars))
	for i, s := range codegenScalars {
		name := strings.TrimSpace(s.Tokens.Symbol)
		if name == "" {
			name = s.Name
		}
		scalars[i] = ScalarInfo{
			Name:               name,
			CanonicalName:      s.Name,
			RustType:           remapScalarLibType(s.TargetType, s, scalarCrate),
			Doc:                codegen.DocText(s.Description, s.Comment),
			HasCustomNormalize: s.HasCustomNormalize,
			HasCustomValidate:  s.HasCustomValidate,
			HasCustomParse:     s.HasCustomParse,
		}
	}
	return scalars
}

// convertUnions converts codegen.UnionInfo to rustgen.UnionInfo.
func convertUnions(codegenUnions []codegen.UnionInfo) []UnionInfo {
	unions := make([]UnionInfo, len(codegenUnions))
	for i, u := range codegenUnions {
		members := make([]UnionMemberInfo, len(u.Types))
		for j, typeName := range u.Types {
			tagValue := ""
			if u.Discriminator != "" && j < len(u.Members) {
				tagValue = u.Members[j].DiscriminatorValue
			}
			members[j] = UnionMemberInfo{Name: typeName, TagValue: tagValue}
		}
		unions[i] = UnionInfo{
			Name:          u.Name,
			Doc:           u.Doc(),
			Types:         u.Types,
			Discriminator: u.Discriminator,
			Members:       members,
		}
	}
	return unions
}

func validateUnionDiscriminatorDefaults(schema *ir.Schema, unions []codegen.UnionInfo) error {
	for _, union := range unions {
		if union.Discriminator == "" {
			continue
		}
		for _, memberName := range union.Types {
			member := schema.Types[memberName]
			for _, field := range member.Fields {
				if field.Name == union.Discriminator && field.InternalMetadata {
					if field.Default == nil {
						return fmt.Errorf(
							"union %q member %q discriminator %q must declare @default",
							union.Name,
							memberName,
							union.Discriminator,
						)
					}
					break
				}
			}
		}
	}
	return nil
}

func validateRenderedUnionDiscriminatorDefaults(types []TypeInfo, unions []UnionInfo) error {
	typesByName := make(map[string]TypeInfo, len(types))
	for _, typeInfo := range types {
		typesByName[typeInfo.Name] = typeInfo
	}

	for _, union := range unions {
		if union.Discriminator == "" {
			continue
		}
		for i, memberName := range union.Types {
			member, ok := typesByName[memberName]
			if !ok {
				continue
			}
			for _, field := range member.Fields {
				if field.Name != union.Discriminator || field.HasDefault {
					continue
				}
				defaultValue := ""
				if i < len(union.Members) {
					defaultValue = union.Members[i].TagValue
				}
				return fmt.Errorf(
					"union %q member %q discriminator %q @default %q cannot be rendered as Rust",
					union.Name,
					memberName,
					union.Discriminator,
					defaultValue,
				)
			}
		}
	}
	return nil
}

// convertTypes converts codegen.TypeInfo to rustgen.TypeInfo.
func convertTypes(codegenTypes []codegen.TypeInfo, enums []codegen.EnumInfo, enumLookup codegen.EnumLookup, discriminators map[string]string) []TypeInfo {
	types := make([]TypeInfo, len(codegenTypes))
	for i, t := range codegenTypes {
		types[i] = TypeInfo{
			Name:   t.Name,
			Owner:  t.Owner,
			Role:   t.Role,
			Doc:    t.Doc(),
			Fields: make([]FieldInfo, len(t.Fields)),
			// @strictJSON rejects undeclared keys in every language; in Rust
			// that is the attribute @denyUnknownFields sets.
			DenyUnknownFields: t.DenyUnknownFields || t.StrictJSON,
		}

		hasDefaults := false
		for j, f := range t.Fields {
			// Relation fields are optional in serialization types because
			// the related object may not be loaded.
			required := f.Required && !f.IsRelation

			rustType := f.TargetType
			if rustType == t.Name {
				rustType = "Box<" + rustType + ">"
			}
			if !required {
				rustType = rustutil.WrapOptionalType(rustType)
			}

			serdeRename := ""
			normalizedFieldName := strings.TrimPrefix(f.TargetName, "r#")
			if normalizedFieldName != f.Name {
				serdeRename = f.Name
			}

			field := FieldInfo{
				Name:        f.Name,
				RustName:    f.TargetName,
				Type:        f.Type,
				RustType:    rustType,
				SerdeRename: serdeRename,
				Required:    required,
				Secret:      f.Secret,
				IsArray:     f.IsArray,
				IsMap:       f.IsMap,
				IsScalar:    f.IsScalar,
				IsUnion:     f.IsUnion,
				Doc:         f.Doc(),
			}

			if f.Default != nil {
				kind := codegen.ClassifyDefault(f, enumLookup)
				if kind != codegen.DefaultLiteralUnknown {
					if literal, ok := formatRustDefaultLiteral(*f.Default, kind, field, enums); ok && literal != "" {
						field.HasDefault = true
						field.DefaultLiteral = literal
						field.DefaultFnName = rustDefaultFnName(t.Name, field)
						hasDefaults = true
					}
				}
			}

			// Only suppress serialization when the default resolved:
			// skipping without a default would make the field impossible
			// to populate on deserialize.
			if field.HasDefault && discriminators[t.Name] == f.Name {
				field.SkipSerializing = true
			}

			types[i].Fields[j] = field
		}

		// Only emit `impl Default for {Name}` when every required field
		// without an @default uses a Rust type known to implement Default.
		// Generated structs / enums in this module are not guaranteed to
		// derive Default, so we conservatively skip the impl rather than
		// produce code that fails to compile downstream.
		canImplDefault := true
		for _, field := range types[i].Fields {
			if field.HasDefault {
				continue
			}
			if !rustTypeHasDefault(field.RustType) {
				canImplDefault = false
				break
			}
		}
		types[i].HasDefaults = hasDefaults && canImplDefault
	}
	return types
}

// rustTypeHasDefault reports whether the given Rust type expression is known
// to implement Default. Only Rust primitives, String, Option<T>, Vec<T>,
// HashMap<K,V>, and Box<T> are considered safe; generated struct / enum types
// are excluded because the codegen pipeline does not derive Default on them.
func rustTypeHasDefault(rustType string) bool {
	rustType = strings.TrimSpace(rustType)
	if rustType == "" {
		return false
	}
	if strings.HasPrefix(rustType, "Option<") {
		return true
	}
	if strings.HasPrefix(rustType, "Vec<") {
		return true
	}
	if strings.HasPrefix(rustType, "HashMap<") {
		return true
	}
	if strings.HasPrefix(rustType, "Box<") {
		return true
	}
	switch rustType {
	case "bool",
		"i8", "i16", "i32", "i64", "i128", "isize",
		"u8", "u16", "u32", "u64", "u128", "usize",
		"f32", "f64",
		"char", "String":
		return true
	}
	return false
}

// inferRustType infers a Rust type from the host-language primitive when the
// scalar has no Rust TypeMappings entry.
func inferRustType(primitive ir.LanguagePrimitive, scalarName string) string {
	traits := codegen.GuessScalarTraits(scalarName)
	switch primitive {
	case ir.LanguageString:
		if traits.IsJSONLike || traits.IsLocationLike || traits.IsObjectLike {
			return "serde_json::Value"
		}
		return "String"
	case ir.LanguageNumber:
		if traits.IsIntegerLike {
			return "i64"
		}
		return "f64"
	case ir.LanguageBoolean:
		return "bool"
	case ir.LanguageObject:
		return "serde_json::Value"
	}
	return "String"
}

func refineRustScalarType(scalar *codegen.ScalarInfo, scalarDef *ir.ScalarDef) {
	if _, hasMapping := scalarDef.TypeMappings["rust"]; hasMapping {
		return
	}
	semanticPrimitive := strings.ToLower(strings.TrimSpace(scalarDef.Primitive))
	if scalar.Traits.IsIntegerLike ||
		semanticPrimitive == "int" ||
		semanticPrimitive == "int64" ||
		semanticPrimitive == "integer" {
		scalar.TargetType = "i64"
	}
}

// remapScalarLibType keeps scalars with a rich superscalar Rust representation
// on the shared type so generated crates agree with each other and with the
// other languages' superscalar bindings. scalarCrate is the scalar runtime
// crate as Rust spells it (Naming.ScalarRustCrateIdent):
//   - UUID-like scalars -> <scalarCrate>::Uuid (canonical base62 serde)
//   - Temporal.DateTime -> <scalarCrate>::DateTime (chrono<Utc> newtype
//     with canonical Go-RFC3339Nano serde; the analog of Go's `time.Time`)
func remapScalarLibType(targetType string, scalar codegen.ScalarInfo, scalarCrate string) string {
	if scalar.Traits.IsUUIDLike || strings.TrimSpace(targetType) == "uuid::Uuid" {
		return scalarCrate + "::Uuid"
	}
	if scalar.Name == "Temporal.DateTime" {
		return scalarCrate + "::DateTime"
	}
	// Generic.StringMap declares ("rust", "std::collections::HashMap<String, String>")
	// in the superscalar catalog, but the loader only plumbs go/typescript/sql/
	// json_schema TypeMappings through to codegen, so without this carve-out the
	// alias degrades to the String primitive fallback and generated SDKs reject
	// real map payloads (e.g. supportedAuthStrategies[].extraHeaders in the sync
	// manifest) with "invalid type: map, expected a string". The durable fix is
	// plumbing the catalog's rust type_mappings through ScalarMetadata + loader.
	if scalar.Name == "Generic.StringMap" {
		return "std::collections::HashMap<String, String>"
	}
	return targetType
}

// fieldTypeMapperRust maps IR type references to Rust types.
func fieldTypeMapperRust(typeName string, isArray bool, isMap bool, isRequired bool, scalarMap codegen.ScalarMap) string {
	valueType := fieldValueTypeMapperRust(typeName, isArray, isMap, isRequired, scalarMap)
	if !isMap {
		return valueType
	}
	return "HashMap<String, " + valueType + ">"
}

func fieldValueTypeMapperRust(typeName string, isArray bool, inMap bool, isRequired bool, scalarMap codegen.ScalarMap) string {
	if isArray {
		elemType := fieldValueTypeMapperRust(typeName, false, false, true, scalarMap)
		if inMap && !isRequired {
			elemType = rustutil.WrapOptionalType(elemType)
		}
		return "Vec<" + elemType + ">"
	}

	var resolvedType string
	if scalar, ok := scalarMap[typeName]; ok {
		if symbol := strings.TrimSpace(scalar.Tokens.Symbol); symbol != "" {
			resolvedType = symbol
		} else {
			resolvedType = typeName
		}
	} else {
		switch typeName {
		case codegen.PrimitiveString:
			resolvedType = "String"
		case codegen.PrimitiveNumber:
			resolvedType = "f64"
		case codegen.PrimitiveBoolean:
			resolvedType = "bool"
		default:
			resolvedType = typeName
		}
	}

	if inMap && !isRequired {
		return rustutil.WrapOptionalType(resolvedType)
	}

	return resolvedType
}

// toRustFieldName converts a schema field name to a snake_case Rust field
// name, prefixing keywords with r#.
func toRustFieldName(name string) string {
	base := codegen.ToSnakeCase(name)
	if base == "" {
		base = "value"
	}

	var b strings.Builder
	for _, r := range base {
		switch {
		case r == '_':
			b.WriteRune(r)
		case unicode.IsLetter(r), unicode.IsDigit(r):
			b.WriteRune(r)
		}
	}

	fieldName := rustutil.CollapseUnderscores(strings.Trim(b.String(), "_"))
	if fieldName == "" {
		fieldName = "value"
	}
	if fieldName[0] >= '0' && fieldName[0] <= '9' {
		fieldName = "_" + fieldName
	}
	if rustutil.IsRustKeyword(fieldName) {
		// crate / self / Self / super cannot be raw identifiers.
		switch fieldName {
		case "crate", "self", "Self", "super":
			return fieldName + "_"
		}
		return "r#" + fieldName
	}
	return fieldName
}

func hasMapFields(types []TypeInfo) bool {
	for _, typ := range types {
		for _, field := range typ.Fields {
			if field.IsMap {
				return true
			}
		}
	}
	return false
}

func hasUnionFields(types []TypeInfo) bool {
	for _, typ := range types {
		for _, field := range typ.Fields {
			if field.IsUnion {
				return true
			}
		}
	}
	return false
}

var knownExternalCrates = map[string]ExternalCrateDep{
	"uuid":       {Name: "uuid", Version: "1", Features: []string{"serde", "v4"}},
	"chrono":     {Name: "chrono", Version: "0.4", Features: []string{"serde"}},
	"serde_json": {Name: "serde_json", Version: "1.0"},
}

// hardcodedCrates lists crate names already present in the Cargo.toml
// template; the scalar runtime crate joins them per output.
var hardcodedCrates = map[string]struct{}{
	"serde": {},
}

func usesScalarLib(output *ModuleOutput) bool {
	marker := output.Naming.ScalarRustCrateIdent() + "::"
	for _, scalar := range output.Scalars {
		if strings.Contains(scalar.RustType, marker) {
			return true
		}
	}
	for _, typ := range output.Types {
		for _, field := range typ.Fields {
			if strings.Contains(field.RustType, marker) {
				return true
			}
		}
	}
	return false
}

func collectExternalCrateDeps(output *ModuleOutput) []ExternalCrateDep {
	crateNames := make(map[string]struct{})
	if len(output.CompositeDefaults) > 0 {
		crateNames["serde_json"] = struct{}{}
	}

	for _, scalar := range output.Scalars {
		extractCrateNames(scalar.RustType, crateNames)
	}
	for _, typ := range output.Types {
		for _, field := range typ.Fields {
			extractCrateNames(field.RustType, crateNames)
		}
	}

	var deps []ExternalCrateDep
	scalarCrate := output.Naming.ScalarRustCrateIdent()
	for name := range crateNames {
		if _, hardcoded := hardcodedCrates[name]; hardcoded || name == scalarCrate {
			continue
		}
		if dep, ok := knownExternalCrates[name]; ok {
			deps = append(deps, dep)
		}
	}

	sort.Slice(deps, func(i, j int) bool { return deps[i].Name < deps[j].Name })
	return deps
}

// extractCrateNames finds external crate references (identifiers preceding
// `::`) in a Rust type string. It only considers identifiers at
// type-expression boundaries (after `<`, `,`, `(`, whitespace, or at the
// start of the string). Local path prefixes like `crate::`, `self::`, and
// `super::` are skipped.
func extractCrateNames(typeStr string, crateNames map[string]struct{}) {
	for i := 0; i < len(typeStr); {
		idx := strings.Index(typeStr[i:], "::")
		if idx < 0 {
			break
		}
		absIdx := i + idx

		start := absIdx
		for start > 0 && isCrateNameChar(typeStr[start-1]) {
			start--
		}

		if start < absIdx {
			if start == 0 || isTypeBoundary(typeStr[start-1]) {
				name := typeStr[start:absIdx]
				if name != "crate" && name != "self" && name != "super" {
					crateNames[name] = struct{}{}
				}
			}
		}
		i = absIdx + 2
	}
}

func isTypeBoundary(c byte) bool {
	return c == '<' || c == ',' || c == '(' || c == ' ' || c == '\t'
}

func isCrateNameChar(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_'
}
