// Package ormgen generates the Go ORM module (pgx repositories, typed
// filters, transaction plumbing) for a schema's database tables.
//
// This is the v2 port of the v1 ormgen generator onto the refactored Schema
// IR: tables are selected by TypeDef.Role == DBTable (instead of treating
// every object type in a DB-kind schema as a table), base classes are
// skipped because their fields arrive pre-flattened on the subclasses,
// table iteration is deterministic (sorted by type name), Go types come
// from the same scalar-symbol mapping typegen uses, and v1's builtin-scalar
// special cases are gone: the UUID and user-id types used for primary-key
// and foreign-key plumbing are resolved from the schema's own scalars
// instead of hardcoding types.UUID / types.UserID.
package ormgen

import (
	"embed"
	"fmt"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

//go:embed templates/*.tmpl
var templatesFS embed.FS

// ORMOutput contains all generated ORM code.
type ORMOutput struct {
	SchemaName               string
	ModulePath               string
	TypesModule              string
	ModuleDependencyReplaces []ModuleDependencyReplace
	Repositories             []Repository
	HasSoftDeletes           bool
	HasVersionedRepositories bool
	HasGenericJSON           bool
	Timestamp                string

	// HasArraysOfArrays is true when a column is an array of arrays (T[][]);
	// it emits the encoder those columns write through.
	HasArraysOfArrays bool

	// UUIDGoType is the Go type used for primary-key / foreign-key plumbing
	// (e.g. "types.IdentityUUID"). Resolved from the schema's UUID-like
	// scalar; all UUID-like superscalar aliases share one underlying type, so
	// a single plumbing type interoperates across tables.
	UUIDGoType string

	// UserIDGoType is the Go type carried in the ORM user context and
	// assigned to createdBy/updatedBy audit fields. Resolved from the audit
	// fields' own scalar, falling back to UUIDGoType.
	UserIDGoType string

	// Naming supplies the scalar and schema-ir module paths go.mod requires.
	Naming naming.Naming

	// ScalarLibReplacePath is the go.mod replace target for superscalar,
	// relative to the output directory. Empty omits the directive.
	ScalarLibReplacePath string

	// SchemaIRReplacePath is the go.mod replace target for schema-ir (a
	// transitive dependency via superscalar), relative to the output
	// directory. Empty omits the directive.
	SchemaIRReplacePath string
}

// ModuleDependencyReplace keeps local generated type dependencies resolvable
// when the ORM module is the main Go module; dependency replace directives are
// not inherited from the generated types module.
type ModuleDependencyReplace struct {
	Module  string
	RelPath string
}

// Repository represents a generated repository for a table.
type Repository struct {
	Name                   string // e.g. "OrderRepository"
	TypeName               string // e.g. "Order"
	TableName              string // e.g. "order"
	QuotedTableName        string // e.g. "order" or "\"user\""
	PrimaryKeyType         string // e.g. "types.IdentityUUID"
	PrimaryKeyCol          string // e.g. "id"
	QuotedPrimaryKey       string // e.g. "id" or "\"order\""
	PrimaryKeyIsUUID       bool   // gates id.ToUUID() coercion in SQL arguments
	PrimaryKeyField        string // schema field name, e.g. "id" or "orderId"
	LookupKeyType          string // GetManyByIDs key: UUIDGoType for a UUID key, else PrimaryKeyType
	HasSoftDelete          bool   // deletedAt field present
	HasDeletedBy           bool   // deletedBy field present (soft deletes stamp it)
	Versioned              bool   // history table/read methods should be generated
	HasPruneHistory        bool   // @versioned retentionDays declared; PruneHistory method generated
	PruneFunctionName      string // generated SQL prune function name (e.g. "order_prune_history")
	HistoryTableName       string
	QuotedHistoryTableName string
	HistoryOperationCol    string
	HistoryDataCol         string
	HistoryRecordedAtCol   string
	Fields                 []Field
	Relationships          []Relationship
	ReferencedBy           []InboundReference
	OrderedMembers         []ColumnMember // fields + to-one FKs in schema order (matches DDL column order)

	// Import requirements computed from the field/relationship shapes so the
	// repository template emits exactly the imports it uses.
	NeedsTime               bool
	NeedsSQLNull            bool
	NeedsReflect            bool
	NeedsPgtype             bool
	HasJSONUnionCollections bool
}

// HasArrayRelationships reports whether any relationship is a hasMany.
func (r Repository) HasArrayRelationships() bool {
	for _, rel := range r.Relationships {
		if rel.IsArray {
			return true
		}
	}
	return false
}

// Field represents a database column backed by a schema field.
type Field struct {
	Name               string // schema field name (e.g. "displayName")
	DBName             string // column name (e.g. "display_name")
	QuotedDBName       string // quoted column name
	IRType             string // schema type name (e.g. "Identity.UUID")
	GoType             string // base Go type matching typegen output (e.g. "types.IdentityUUID")
	IsRequired         bool
	IsArray            bool
	IsArrayOfArrays    bool // T[][] (IsArray is set too): a JSONB column, see IsJSONField
	IsMap              bool
	NullableMapValues  bool // optional non-union map: typegen emits map[string]*T (map[string][]*T for lists)
	IsUnion            bool // closed union; JSON decoding dispatches through types.<IRType>Wrapper
	IsPrimaryKey       bool
	IsAuditField       bool // createdAt/createdBy/updatedAt/updatedBy/deletedAt/deletedBy
	IsAuditTimestamp   bool // createdAt/updatedAt: filterable, unlike the rest of the audit set
	IsInternalMetadata bool // superschematic-managed metadata column such as _version
	IsAutoGenerated    bool
	HasDefault         bool
	IsJSONField        bool   // stored as JSONB (maps, @jsonField, T[][]); scanned via []byte + JSON codec
	IsScalarType       bool   // scalar or enum (value type in typegen output)
	JSONUnionDecoder   string // generated per-repository decoder for a closed-union JSON field
	IsNullableEnum     bool   // optional enum: typegen emits *Enum
	IsNullableScalar   bool   // optional non-integer scalar: typegen emits *Scalar
	IsUUIDScalar       bool   // non-array UUID-like scalar: values coerce via .ToUUID()
	IsDateTimeScalar   bool   // non-array datetime-like scalar: values coerce via time.Time()
	IsUUIDLike         bool
	IsStringLike       bool
	IsIntLike          bool
	IsFloatLike        bool
	IsBoolLike         bool
	IsDateTimeLike     bool
	IsDateLike         bool
	IsJSONLike         bool

	// PreservesExplicitJSONNull is true only for a direct Generic.JSON scalar.
	// Its four-byte JSON `null` token is a value, distinct from SQL NULL.
	PreservesExplicitJSONNull bool

	// OptionalNilCheck is true when the optional/auto-generated insert path
	// detects a set value with `input.X != nil` (pointer, slice, or map Go
	// representations); false means reflect.ValueOf(...).IsZero() is used.
	OptionalNilCheck bool

	// DerefValue is true when the field's Go representation is a pointer
	// that must be dereferenced before being passed as a SQL argument.
	DerefValue bool

	// EnumDefault is the declared @default of a required, non-array enum
	// field. CreateOne and CreateMany insert it when the caller leaves the
	// field at its Go zero value, the empty string, which is never an enum
	// member; without it a struct literal that omits the field inserts ''.
	EnumDefault string
}

// ArrayDepth is 0 for T, 1 for T[] and 2 for T[][], as ir.TypeRef reports it.
func (f Field) ArrayDepth() int {
	return ir.TypeRef{IsArray: f.IsArray, IsArrayOfArrays: f.IsArrayOfArrays}.ArrayDepth()
}

// ValueGoType is the Go type typegen emits for the field's value: GoType
// wrapped in its lists and map, without the pointer a nullable scalar adds.
// An optional non-union map's values are pointers (NullableMapValues).
func (f Field) ValueGoType() string {
	elem := f.GoType
	if f.NullableMapValues {
		elem = "*" + elem
	}
	value := codegen.WrapArray(elem, f.ArrayDepth(), func(elem string) string { return "[]" + elem })
	if f.IsMap {
		return "map[string]" + value
	}
	return value
}

// Relationship represents a foreign key relationship.
type Relationship struct {
	FieldName                 string // schema field name (e.g. "order")
	DBColumnName              string // FK column (e.g. "order_id")
	QuotedDBColumn            string
	TargetType                string // target type name (e.g. "Order")
	TargetTable               string // target table name (e.g. "order")
	QuotedTargetTable         string
	IsRequired                bool
	IsArray                   bool // true for hasMany relationships
	TargetPrimaryKeyIsPointer bool

	// For hasMany relationships, these deterministic names point to the
	// child's relation field back to this parent (e.g. "Connector") and the
	// corresponding filter field (e.g. "ConnectorID"). Empty means fallback
	// candidate probing is used at runtime.
	HasManyParentRelationField string
	HasManyParentFilterField   string
}

// InboundReference is a to-one relationship on another table that points at
// this one. It backs the generated existence filter: "rows that some other
// table does (or does not) reference". Without it a caller has to fetch a page
// of rows and drop the referenced ones in application code, which cannot page
// past rows it always drops.
type InboundReference struct {
	// FilterField is the generated filter field name, e.g.
	// "ReferencedByProposalSourceRef".
	FilterField string

	// SourceTypeName is the referencing type, for documentation.
	SourceTypeName string

	// FieldName is the referencing type's relation field.
	FieldName string

	QuotedSourceTable  string
	QuotedSourceColumn string

	// SourceHasSoftDelete gates the IncludeDeleted knob on the probe.
	SourceHasSoftDelete bool
}

// ColumnMember is either a Field or a to-one Relationship in schema order.
// The interleaved order matches the DDL column order, which the generated
// scan code depends on.
type ColumnMember struct {
	IsRelationship bool
	Field          *Field
	Relationship   *Relationship
}

// Options configures ORM generation.
type Options struct {
	// SchemaName is the service name (e.g. "web-db").
	SchemaName string

	// ModulePath is the Go module path for the generated ORM module.
	ModulePath string

	// TypesModule is the Go module path of the generated types module the
	// ORM compiles against.
	TypesModule string

	// Naming supplies the scalar and schema-ir module paths go.mod requires.
	// Empty fields fall back to naming.Default().
	Naming naming.Naming

	// Dependencies maps declared service dependencies to loaded schemas. ORM
	// generation uses it to identify imported closed unions in JSONB fields.
	Dependencies map[string]*ir.Schema

	// Clock stamps the generated output.
	Clock codegen.Clock
}

// Generate generates the Go ORM from a v2 IR schema. Returns nil when the
// schema declares no database tables.
func Generate(schema *ir.Schema, opts Options) (*ORMOutput, error) {
	if opts.Clock == nil {
		opts.Clock = codegen.DefaultClock()
	}
	opts.Naming = opts.Naming.OrDefault()

	tableTypes := tableTypeDefs(schema)
	if len(tableTypes) == 0 {
		return nil, nil
	}

	scalarMap := buildScalarLookup(schema)
	unionNames := collectUnionTypeNames(schema, opts.Dependencies)
	enumNames := collectEnumTypeNames(schema, opts.Dependencies)
	uuidGoType, err := resolveUUIDGoType(schema, tableTypes, scalarMap)
	if err != nil {
		return nil, err
	}

	repositories := make([]Repository, 0, len(tableTypes))
	for _, typeDef := range tableTypes {
		repo, err := extractRepository(typeDef, schema, scalarMap, unionNames, enumNames, uuidGoType)
		if err != nil {
			return nil, fmt.Errorf("failed to extract table from type %s: %w", typeDef.Name, err)
		}
		repositories = append(repositories, repo)
	}

	attachInboundReferences(repositories)

	hasSoftDeletes := false
	hasVersionedRepositories := false
	hasGenericJSON := false
	hasArraysOfArrays := false
	for _, repo := range repositories {
		if repo.HasSoftDelete {
			hasSoftDeletes = true
		}
		if repo.Versioned {
			hasVersionedRepositories = true
		}
		for _, field := range repo.Fields {
			if field.PreservesExplicitJSONNull {
				hasGenericJSON = true
			}
			if field.IsArrayOfArrays {
				hasArraysOfArrays = true
			}
		}
	}

	output := &ORMOutput{
		SchemaName:               opts.SchemaName,
		ModulePath:               opts.ModulePath,
		TypesModule:              opts.TypesModule,
		ModuleDependencyReplaces: dependencyTypeModuleReplaces(opts.Naming, opts.Dependencies),
		Naming:                   opts.Naming,
		Repositories:             repositories,
		HasSoftDeletes:           hasSoftDeletes,
		HasVersionedRepositories: hasVersionedRepositories,
		HasGenericJSON:           hasGenericJSON,
		HasArraysOfArrays:        hasArraysOfArrays,
		Timestamp:                opts.Clock.RFC3339(),
		UUIDGoType:               uuidGoType,
		UserIDGoType:             resolveUserIDGoType(tableTypes, scalarMap, uuidGoType),
	}

	return output, nil
}

// dependencyTypeModuleReplaces returns a replace line per declared
// dependency's Go types module. The ORM module is built as the main module,
// and replace directives are not inherited from the types module it
// requires, so a dependency the types module imports must be replaced here
// too.
func dependencyTypeModuleReplaces(n naming.Naming, dependencies map[string]*ir.Schema) []ModuleDependencyReplace {
	if len(dependencies) == 0 {
		return nil
	}
	n = n.OrDefault()
	names := make([]string, 0, len(dependencies))
	for name := range dependencies {
		names = append(names, name)
	}
	sort.Strings(names)
	replaces := make([]ModuleDependencyReplace, 0, len(names))
	for _, name := range names {
		replaces = append(replaces, ModuleDependencyReplace{
			Module:  n.GoTypesModule(name),
			RelPath: "../../types/go/" + name,
		})
	}
	return replaces
}

// SetReplacePaths sets the go.mod replace directive paths for the scalar
// library and the schema IR relative to the output directory. An unset path
// emits no directive.
func SetReplacePaths(output *ORMOutput, paths naming.LocalPaths, outputDir string) error {
	var err error
	if output.ScalarLibReplacePath, err = naming.RelPath(outputDir, paths.ScalarGo); err != nil {
		return fmt.Errorf("scalar library replace path: %w", err)
	}
	if output.SchemaIRReplacePath, err = naming.RelPath(outputDir, paths.SchemaIR); err != nil {
		return fmt.Errorf("schema-ir replace path: %w", err)
	}
	return nil
}

// scalarLookup carries per-scalar naming and trait info for field mapping.
type scalarLookup struct {
	symbol string
	traits codegen.ScalarTraits
}

func buildScalarLookup(schema *ir.Schema) map[string]scalarLookup {
	lookup := make(map[string]scalarLookup, len(schema.Scalars))
	for name, scalarDef := range schema.Scalars {
		tokens := codegen.BuildScalarTokens(name)
		symbol := strings.TrimSpace(tokens.Symbol)
		if symbol == "" {
			symbol = name
		}
		lookup[name] = scalarLookup{
			symbol: symbol,
			traits: codegen.BuildScalarTraits(scalarDef, tokens, ""),
		}
	}
	return lookup
}

// tableTypeDefs returns the schema's table-backed type definitions sorted by
// name: Role == DBTable, not a @jsonField payload, and not a base class
// (base-class fields arrive pre-flattened on the subclasses).
func tableTypeDefs(schema *ir.Schema) []*ir.TypeDef {
	baseTypes := codegen.BaseTypeNames(schema)

	var defs []*ir.TypeDef
	for _, typeDef := range schema.Types {
		if typeDef.Role != ir.RoleDBTable || typeDef.JsonField || baseTypes[typeDef.Name] {
			continue
		}
		if len(typeDef.Fields) == 0 {
			continue
		}
		defs = append(defs, typeDef)
	}

	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	return defs
}

// resolveUUIDGoType picks the Go type used for PK/FK plumbing: the scalar
// type of the first declared primary key that is UUID-like, then any
// UUID-like scalar in the schema, by the name the types package declares
// for it. The ORM's UUID filter, id lookups and user context use that type
// in every schema, and the types package declares only the scalars the
// schema has, so a schema without a UUID-like scalar is an error.
func resolveUUIDGoType(schema *ir.Schema, tableTypes []*ir.TypeDef, scalars map[string]scalarLookup) (string, error) {
	for _, typeDef := range tableTypes {
		for _, field := range typeDef.Fields {
			if !field.Key {
				continue
			}
			if s, ok := scalars[field.TypeRef.Name]; ok && s.traits.IsUUIDLike {
				return "types." + s.symbol, nil
			}
		}
	}

	names := make([]string, 0, len(scalars))
	for name := range scalars {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if scalars[name].traits.IsUUIDLike {
			return "types." + scalars[name].symbol, nil
		}
	}

	return "", fmt.Errorf("ormgen: schema %s has tables but no UUID scalar: the ORM keys its id lookups, UUID filters and user context by one; give a table a key field of a UUID scalar such as Identity.UUID", schema.Name)
}

// resolveUserIDGoType picks the Go type for the ORM user context from the
// createdBy/updatedBy/deletedBy audit fields, falling back to the UUID
// plumbing type.
func resolveUserIDGoType(tableTypes []*ir.TypeDef, scalars map[string]scalarLookup, uuidGoType string) string {
	for _, typeDef := range tableTypes {
		for _, field := range typeDef.Fields {
			switch field.Name {
			case "createdBy", "updatedBy", "deletedBy":
				if s, ok := scalars[field.TypeRef.Name]; ok {
					return "types." + s.symbol
				}
			}
		}
	}
	return uuidGoType
}

// collectEnumTypeNames returns locally declared enums plus imported enum
// symbols whose dependency schemas are available to ormgen.
func collectEnumTypeNames(schema *ir.Schema, dependencies map[string]*ir.Schema) map[string]bool {
	enums := make(map[string]bool, len(schema.Enums))
	for name := range schema.Enums {
		enums[name] = true
	}
	for _, imported := range schema.Imports {
		depName := imported.Package
		if slash := strings.LastIndex(depName, "/"); slash >= 0 {
			depName = depName[slash+1:]
		}
		dependency := dependencies[depName]
		if dependency == nil {
			continue
		}
		for _, name := range imported.Types {
			if dependency.Enums[name] != nil {
				enums[name] = true
			}
		}
	}
	return enums
}

// collectUnionTypeNames returns locally declared unions plus imported union
// symbols whose dependency schemas are available to ormgen.
func collectUnionTypeNames(schema *ir.Schema, dependencies map[string]*ir.Schema) map[string]bool {
	unions := make(map[string]bool, len(schema.Unions))
	for name := range schema.Unions {
		unions[name] = true
	}
	for _, imported := range schema.Imports {
		depName := imported.Package
		if slash := strings.LastIndex(depName, "/"); slash >= 0 {
			depName = depName[slash+1:]
		}
		dependency := dependencies[depName]
		if dependency == nil {
			continue
		}
		for _, name := range imported.Types {
			if unions[name] || schema.Types[name] != nil || schema.Enums[name] != nil || schema.Scalars[name] != nil {
				continue
			}
			if dependency.Unions[name] != nil {
				unions[name] = true
			}
		}
	}
	return unions
}

// isRelationalTarget reports whether typeName names a table-backed type in
// this schema.
func isRelationalTarget(typeName string, schema *ir.Schema) bool {
	targetTypeDef := schema.Types[typeName]
	return targetTypeDef != nil && targetTypeDef.Role == ir.RoleDBTable && !targetTypeDef.JsonField
}

// extractRepository extracts repository information from an IR TypeDef.
func extractRepository(typeDef *ir.TypeDef, schema *ir.Schema, scalars map[string]scalarLookup, unionNames, enumNames map[string]bool, uuidGoType string) (Repository, error) {
	tableName := codegen.ToSnakeCase(typeDef.Name)
	repo := Repository{
		Name:            typeDef.Name + "Repository",
		TypeName:        typeDef.Name,
		TableName:       tableName,
		QuotedTableName: sqlutil.QuoteIdentifier(tableName),
		Versioned:       typeDef.Versioned,
		Fields:          []Field{},
		Relationships:   []Relationship{},
		OrderedMembers:  []ColumnMember{},
	}
	if typeDef.Versioned {
		repo.HistoryTableName = tableName + "_history"
		repo.QuotedHistoryTableName = sqlutil.QuoteIdentifier(repo.HistoryTableName)
		repo.HistoryOperationCol = "operation"
		repo.HistoryDataCol = "data"
		repo.HistoryRecordedAtCol = "recorded_at"
		if cfg := typeDef.VersionedConfig; cfg != nil && cfg.RetentionDays != nil {
			repo.HasPruneHistory = true
			repo.PruneFunctionName = tableName + "_prune_history"
		}
	}

	for _, field := range typeDef.Fields {
		if field.Name == "deletedAt" {
			repo.HasSoftDelete = true
		}
		if field.Name == "deletedBy" {
			repo.HasDeletedBy = true
		}
	}

	for _, fieldDef := range typeDef.Fields {
		relationalTarget := !fieldDef.JsonField && !fieldDef.TypeRef.IsMap &&
			isRelationalTarget(fieldDef.TypeRef.Name, schema)

		if relationalTarget {
			// A relation cannot nest, so an array of arrays of a table type
			// has no column to become.
			if fieldDef.TypeRef.IsArrayOfArrays {
				return repo, fmt.Errorf(
					"field %s.%s is an array of arrays of table type %s, which cannot be a relation; store a list of lists of its keys or of a @jsonField type",
					typeDef.Name, fieldDef.Name, fieldDef.TypeRef.Name,
				)
			}
			// Many-to-many relationships live in a join table; the
			// repository surface has no support for them (matches v1).
			if fieldDef.TypeRef.IsArray && fieldDef.ManyToMany {
				continue
			}
			if fieldDef.TypeRef.IsArray && !fieldDef.HasMany {
				return repo, fmt.Errorf(
					"field %s.%s is a list of table type %s but lacks @hasMany or @manyToMany",
					typeDef.Name, fieldDef.Name, fieldDef.TypeRef.Name,
				)
			}

			rel := extractRelationship(fieldDef, typeDef.Name, schema, scalars, unionNames)
			repo.Relationships = append(repo.Relationships, rel)
			// Only to-one relationships are local FK columns; hasMany
			// relationships are hydrated separately and do not participate
			// in column selection / scan order.
			if !rel.IsArray {
				relIdx := len(repo.Relationships) - 1
				repo.OrderedMembers = append(repo.OrderedMembers, ColumnMember{
					IsRelationship: true,
					Relationship:   &repo.Relationships[relIdx],
				})
			}
			continue
		}

		field := extractField(fieldDef, schema, scalars, unionNames)
		if enumNames[field.IRType] && field.IsRequired && !field.IsArray && fieldDef.Default != nil {
			field.EnumDefault = *fieldDef.Default
		}
		if field.IsJSONField && field.IsUnion {
			field.JSONUnionDecoder = "decode" + typeDef.Name + codegen.ToPascalCase(field.Name) + "JSONUnion"
		}
		repo.Fields = append(repo.Fields, field)
		fieldIdx := len(repo.Fields) - 1
		repo.OrderedMembers = append(repo.OrderedMembers, ColumnMember{
			IsRelationship: false,
			Field:          &repo.Fields[fieldIdx],
		})

		if field.IsPrimaryKey {
			repo.PrimaryKeyField = field.Name
			repo.PrimaryKeyCol = field.DBName
			repo.QuotedPrimaryKey = field.QuotedDBName
			repo.PrimaryKeyType = field.GoType
			repo.PrimaryKeyIsUUID = field.IsUUIDScalar
		}
	}

	if repo.PrimaryKeyCol == "" {
		idField := Field{
			Name:            "id",
			DBName:          "id",
			QuotedDBName:    sqlutil.QuoteIdentifier("id"),
			IRType:          "id",
			GoType:          uuidGoType,
			IsRequired:      true,
			IsPrimaryKey:    true,
			IsAutoGenerated: true,
			IsScalarType:    true,
			IsUUIDScalar:    true,
			IsUUIDLike:      true,
		}

		repo.Fields = append([]Field{idField}, repo.Fields...)
		// Re-anchor OrderedMembers field pointers after the prepend.
		members := make([]ColumnMember, 0, len(repo.OrderedMembers)+1)
		members = append(members, ColumnMember{Field: &repo.Fields[0]})
		fieldIdx := 1
		for _, member := range repo.OrderedMembers {
			if member.IsRelationship {
				members = append(members, member)
				continue
			}
			members = append(members, ColumnMember{Field: &repo.Fields[fieldIdx]})
			fieldIdx++
		}
		repo.OrderedMembers = members
		repo.PrimaryKeyField = "id"
		repo.PrimaryKeyCol = "id"
		repo.QuotedPrimaryKey = sqlutil.QuoteIdentifier("id")
		repo.PrimaryKeyType = uuidGoType
		repo.PrimaryKeyIsUUID = true
	}
	repo.LookupKeyType = uuidGoType
	if !repo.PrimaryKeyIsUUID {
		repo.LookupKeyType = repo.PrimaryKeyType
	}
	if repo.Versioned {
		appendVersionField(&repo)
	}

	reanchorOrderedMembers(&repo)
	computeImportNeeds(&repo)
	return repo, nil
}

func appendVersionField(repo *Repository) {
	for _, field := range repo.Fields {
		if field.DBName == "_version" {
			return
		}
	}
	versionField := Field{
		Name:               "_version",
		DBName:             "_version",
		QuotedDBName:       sqlutil.QuoteIdentifier("_version"),
		IRType:             codegen.PrimitiveNumber,
		GoType:             "int64",
		IsRequired:         true,
		IsInternalMetadata: true,
		IsAutoGenerated:    true,
		IsScalarType:       true,
		IsIntLike:          true,
	}
	repo.Fields = append(repo.Fields, versionField)
	repo.OrderedMembers = append(repo.OrderedMembers, ColumnMember{
		IsRelationship: false,
		Field:          &repo.Fields[len(repo.Fields)-1],
	})
}

func reanchorOrderedMembers(repo *Repository) {
	fieldByDBName := make(map[string]*Field, len(repo.Fields))
	for i := range repo.Fields {
		fieldByDBName[repo.Fields[i].DBName] = &repo.Fields[i]
	}
	relationshipByDBName := make(map[string]*Relationship, len(repo.Relationships))
	for i := range repo.Relationships {
		relationshipByDBName[repo.Relationships[i].DBColumnName] = &repo.Relationships[i]
	}
	for i := range repo.OrderedMembers {
		member := &repo.OrderedMembers[i]
		if member.IsRelationship {
			if member.Relationship != nil {
				member.Relationship = relationshipByDBName[member.Relationship.DBColumnName]
			}
			continue
		}
		if member.Field != nil {
			member.Field = fieldByDBName[member.Field.DBName]
		}
	}
}

// extractField extracts column information from an IR FieldDef.
func extractField(fieldDef *ir.FieldDef, schema *ir.Schema, scalars map[string]scalarLookup, unionNames map[string]bool) Field {
	irType := fieldDef.TypeRef.Name
	isArray := fieldDef.TypeRef.IsArray
	isRequired := fieldDef.Required
	dbName := codegen.ToSnakeCase(fieldDef.Name)

	scalar, isScalar := scalars[irType]
	isUnion := unionNames[irType]
	_, isEnum := schema.Enums[irType]
	targetTypeDef := schema.Types[irType]
	isJSONPayloadType := targetTypeDef != nil && targetTypeDef.JsonField

	traits := scalar.traits
	if !isScalar {
		traits = codegen.GuessScalarTraits(irType)
	}

	// Arrays and maps are nilable in typegen output ([]T, map[string]T; an
	// optional non-union map holds *T values), so neither is a *T even when
	// the field itself is nullable. Optional integer-like scalars keep
	// value-type parity with the legacy generator; other optional scalars and
	// enums still use pointers.
	isMap := fieldDef.TypeRef.IsMap
	isNullableEnum := !isRequired && !isArray && !isMap && isEnum
	isNullableScalar := !isRequired && !isArray && !isMap && isScalar && !traits.IsIntegerLike
	preservesExplicitJSONNull := isScalar && irType == "Generic.JSON" && !isArray && !isMap

	field := Field{
		Name:                      fieldDef.Name,
		DBName:                    dbName,
		QuotedDBName:              sqlutil.QuoteIdentifier(dbName),
		IRType:                    irType,
		GoType:                    mapIRToGoType(irType, scalars),
		IsRequired:                isRequired,
		IsArray:                   isArray,
		IsArrayOfArrays:           fieldDef.TypeRef.IsArrayOfArrays,
		IsMap:                     isMap,
		NullableMapValues:         isMap && !isRequired && !isUnion,
		IsUnion:                   isUnion,
		IsPrimaryKey:              fieldDef.Key,
		IsAuditField:              isAuditField(fieldDef.Name),
		IsAuditTimestamp:          fieldDef.Name == "createdAt" || fieldDef.Name == "updatedAt",
		IsInternalMetadata:        fieldDef.InternalMetadata,
		IsAutoGenerated:           fieldDef.AutoGenerated,
		HasDefault:                fieldDef.Default != nil,
		IsJSONField:               fieldDef.JsonField || fieldDef.TypeRef.IsMap || fieldDef.TypeRef.IsArrayOfArrays || isJSONPayloadType || preservesExplicitJSONNull,
		IsScalarType:              isScalar || isEnum,
		IsNullableEnum:            isNullableEnum,
		IsNullableScalar:          isNullableScalar,
		IsUUIDScalar:              isScalar && !isArray && traits.IsUUIDLike,
		IsDateTimeScalar:          isScalar && !isArray && traits.IsDateTimeLike,
		IsUUIDLike:                traits.IsUUIDLike,
		IsStringLike:              traits.IsStringLike || irType == codegen.PrimitiveString,
		IsIntLike:                 traits.IsIntegerLike,
		IsFloatLike:               traits.IsFloatLike || irType == codegen.PrimitiveNumber,
		IsBoolLike:                traits.IsBooleanLike || irType == codegen.PrimitiveBoolean,
		IsDateTimeLike:            traits.IsDateTimeLike,
		IsDateLike:                traits.IsDateLike,
		IsJSONLike:                traits.IsJSONLike || traits.IsObjectLike || fieldDef.TypeRef.IsMap,
		PreservesExplicitJSONNull: preservesExplicitJSONNull,
	}

	nilCheckedJSONScalar := field.IsScalarType && field.IsJSONLike && !field.IsArray
	nullableComplex := !field.IsRequired && !field.IsArray && !field.IsMap && !field.IsUnion &&
		strings.HasPrefix(field.GoType, "types.") && !field.IsScalarType
	field.OptionalNilCheck = nilCheckedJSONScalar || nullableComplex || field.IsUnion || field.IsMap ||
		field.IsNullableEnum || field.IsNullableScalar
	field.DerefValue = nullableComplex || field.IsNullableEnum || field.IsNullableScalar

	return field
}

// extractRelationship extracts relationship information from an IR FieldDef.
func extractRelationship(fieldDef *ir.FieldDef, parentTypeName string, schema *ir.Schema, scalars map[string]scalarLookup, unionNames map[string]bool) Relationship {
	targetType := fieldDef.TypeRef.Name
	dbColumnName := codegen.ToSnakeCase(fieldDef.Name) + "_id"
	targetTableName := codegen.ToSnakeCase(targetType)
	hasManyParentRelationField := ""
	hasManyParentFilterField := ""
	if fieldDef.TypeRef.IsArray {
		hasManyParentRelationField, hasManyParentFilterField = inferHasManyParentMapping(targetType, parentTypeName, schema)
	}

	return Relationship{
		FieldName:                  fieldDef.Name,
		DBColumnName:               dbColumnName,
		QuotedDBColumn:             sqlutil.QuoteIdentifier(dbColumnName),
		TargetType:                 targetType,
		TargetTable:                targetTableName,
		QuotedTargetTable:          sqlutil.QuoteIdentifier(targetTableName),
		IsRequired:                 fieldDef.Required,
		IsArray:                    fieldDef.TypeRef.IsArray,
		TargetPrimaryKeyIsPointer:  targetPrimaryKeyIsPointer(targetType, schema, scalars, unionNames),
		HasManyParentRelationField: hasManyParentRelationField,
		HasManyParentFilterField:   hasManyParentFilterField,
	}
}

func targetPrimaryKeyIsPointer(targetType string, schema *ir.Schema, scalars map[string]scalarLookup, unionNames map[string]bool) bool {
	typeDef := schema.Types[targetType]
	if typeDef == nil {
		return false
	}
	for _, fieldDef := range typeDef.Fields {
		if fieldDef == nil || !fieldDef.Key {
			continue
		}
		field := extractField(fieldDef, schema, scalars, unionNames)
		return field.DerefValue || field.IsAutoGenerated
	}
	return false
}

// inferHasManyParentMapping finds the child's relation field that points back
// at the parent so generated hasMany hydration can target it deterministically.
func inferHasManyParentMapping(childTypeName string, parentTypeName string, schema *ir.Schema) (string, string) {
	childType := schema.Types[childTypeName]
	if childType == nil {
		return "", ""
	}

	for _, childField := range childType.Fields {
		if childField == nil || childField.TypeRef.IsArray {
			continue
		}
		if childField.TypeRef.Name != parentTypeName {
			continue
		}

		parentRelationField := codegen.ToPascalCase(childField.Name)
		filterBaseField := childField.Name
		if childField.Relation != nil && childField.Relation.Field != "" {
			filterBaseField = childField.Relation.Field
		}
		filterBaseField = strings.TrimSuffix(filterBaseField, "Id")
		filterBaseField = strings.TrimSuffix(filterBaseField, "ID")
		if filterBaseField == "" {
			filterBaseField = childField.Name
		}
		return parentRelationField, codegen.ToPascalCase(filterBaseField) + "ID"
	}

	return "", ""
}

// attachInboundReferences indexes every to-one foreign key by the table it
// points at, so each repository knows who references it. The edges already
// exist in the schema; this only makes them visible from the other side.
func attachInboundReferences(repositories []Repository) {
	byTable := make(map[string]int, len(repositories))
	for i := range repositories {
		byTable[repositories[i].TableName] = i
	}

	for i := range repositories {
		source := &repositories[i]
		for _, rel := range source.Relationships {
			if rel.IsArray {
				continue
			}
			target, ok := byTable[rel.TargetTable]
			if !ok {
				continue
			}
			repositories[target].ReferencedBy = append(repositories[target].ReferencedBy, InboundReference{
				FilterField:         "ReferencedBy" + source.TypeName + codegen.ToPascalCase(rel.FieldName),
				SourceTypeName:      source.TypeName,
				FieldName:           rel.FieldName,
				QuotedSourceTable:   source.QuotedTableName,
				QuotedSourceColumn:  rel.QuotedDBColumn,
				SourceHasSoftDelete: source.HasSoftDelete,
			})
		}
	}

	for i := range repositories {
		sort.Slice(repositories[i].ReferencedBy, func(a, b int) bool {
			return repositories[i].ReferencedBy[a].FilterField < repositories[i].ReferencedBy[b].FilterField
		})
	}
}

// isAuditField reports whether name is one of the audit columns.
func isAuditField(name string) bool {
	switch name {
	case "createdAt", "createdBy", "updatedAt", "updatedBy", "deletedAt", "deletedBy":
		return true
	}
	return false
}

// mapIRToGoType maps a schema type name to the base Go type typegen emits
// for it (without array/pointer wrapping, qualified with the types package).
func mapIRToGoType(irType string, scalars map[string]scalarLookup) string {
	switch irType {
	case codegen.PrimitiveString:
		return "string"
	case codegen.PrimitiveNumber:
		return "float64"
	case codegen.PrimitiveBoolean:
		return "bool"
	}

	if scalar, ok := scalars[irType]; ok {
		return "types." + scalar.symbol
	}

	// Enums, object types, and unions all live in the types package under
	// their own name.
	return "types." + irType
}

// computeImportNeeds derives the repository file's conditional imports from
// the shapes of its fields and relationships.
func computeImportNeeds(repo *Repository) {
	if repo.Versioned {
		repo.NeedsTime = true
	}
	if repo.HasSoftDelete {
		repo.NeedsTime = true
	}
	for _, rel := range repo.Relationships {
		if rel.IsArray {
			repo.NeedsReflect = true
		}
	}
	for i := range repo.Fields {
		field := &repo.Fields[i]
		if field.IsJSONField && field.IsUnion && (field.IsArray || field.IsMap) {
			repo.HasJSONUnionCollections = true
		}
		// time.Now() stamps updated_at; time.Time() conversions apply to
		// non-audit, non-PK datetime scalars in the insert/update paths.
		if field.Name == "updatedAt" {
			repo.NeedsTime = true
		}
		if field.IsDateTimeScalar && !field.IsAuditField && !field.IsPrimaryKey {
			repo.NeedsTime = true
		}
		if !field.IsJSONField && !field.IsArray && field.IsDateLike && !field.IsPrimaryKey && !field.IsAuditField {
			repo.NeedsPgtype = true
		}
		// A JSON column (a map among them) scans through []byte, never
		// sql.NullString.
		if !field.IsJSONField && !field.IsRequired && !field.IsPrimaryKey && !field.IsAuditField && !field.IsArray && field.GoType == "string" {
			repo.NeedsSQLNull = true
		}

		// The optional/auto-generated insert path falls back to
		// reflect.ValueOf(...).IsZero() when no nil check applies. This must match
		// the template's reflect-emission guard exactly (CreateOne / CreateMany
		// field-names / CreateMany values in repository.tmpl), which also excludes
		// IsInternalMetadata columns (e.g. `_version`) -- otherwise an entity whose
		// only optional insert field is internal metadata sets NeedsReflect=true
		// while the template emits no reflect.* call, yielding an unused import
		// (was papered over by a `var _ = reflect.ValueOf` band-aid).
		optionalInsert := !field.IsPrimaryKey && !field.IsInternalMetadata &&
			field.Name != "createdAt" && field.Name != "updatedAt" &&
			(!field.IsRequired || field.IsAutoGenerated)
		if optionalInsert && !field.OptionalNilCheck {
			repo.NeedsReflect = true
		}
	}
}
