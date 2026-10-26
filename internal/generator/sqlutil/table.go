package sqlutil

import (
	"fmt"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// A table as the ORMs read and write it (D79): the Go ORM (ormgen) and the
// TypeScript repositories (tsgen's ./orm entry) both take their tables from
// TablesOf, and their statements from TableStatementsOf, as they take a
// queue's from QueueStatements, so the two run the same SQL and cannot
// drift. The model holds what the statements are written from and what each
// column is (ColumnClass), which decides how a value binds, reads back,
// filters and sorts.

// SearchTextColumn is the generated column @searchField adds, which sqlgen
// writes and a filter's search text compares (D62).
const SearchTextColumn = "search_text"

// TableName is the table a table type is stored in: the name @table gives
// it (D85), else its name in snake case. Every generator that names a
// table names it here (D79).
func TableName(td *ir.TypeDef) string {
	if td.SQLName != "" {
		return td.SQLName
	}
	return codegen.ToSnakeCase(td.Name)
}

// TableNameOf is TableName of the type named typeName in schema, or the
// name in snake case when the schema holds no such type.
func TableNameOf(schema *ir.Schema, typeName string) string {
	if schema != nil {
		if td := schema.Types[typeName]; td != nil {
			return TableName(td)
		}
	}
	return codegen.ToSnakeCase(typeName)
}

// QueueTableName is the table of the queue td declares (D53): the name
// @table gives it (D85), else the class's name in snake case and
// ir.QueueTableSuffix, order_placed_queue. sqlgen's DDL and every queue
// client name it here, and its key indexes and @enqueueOnChange triggers
// are named from it.
func QueueTableName(td *ir.TypeDef) string {
	if td.SQLName != "" {
		return td.SQLName
	}
	return codegen.ToSnakeCase(td.Name) + ir.QueueTableSuffix
}

// ColumnName is the column a field is stored in: the name @column gives it
// (D85), else its name in snake case. Like TableName, it is the one place a
// column is named; a CHECK's name (CheckName) and an @index key's column
// (KeyColumnIn) follow from it, so the DDL's names (D83) and the ORMs'
// columns come from one place.
func ColumnName(f *ir.FieldDef) string {
	if f.SQLName != "" {
		return f.SQLName
	}
	return ColumnNameOf(f.Name)
}

// ColumnNameOf is the column of a field named field with no @column: its
// name in snake case.
func ColumnNameOf(field string) string {
	return codegen.ToSnakeCase(field)
}

// ForeignKeyColumn is the column a to-one relation field stores its
// target's key in: the name @column gives it (D85), else the field's
// column with _id.
func ForeignKeyColumn(f *ir.FieldDef) string {
	if f.SQLName != "" {
		return f.SQLName
	}
	return ColumnNameOf(f.Name) + "_id"
}

// IsTableRelation reports whether f is a to-one or list relation to a
// table of schema, which holds a foreign key and no value of its own.
func IsTableRelation(schema *ir.Schema, f *ir.FieldDef) bool {
	return !f.JsonField && !f.TypeRef.IsMap && relationalTarget(schema, f.TypeRef.Name)
}

// ColumnNameIn is the name of the column that holds f, a field of a table
// of schema: a to-one relation's foreign key (ForeignKeyColumn), any other
// field's column (ColumnName). ColumnOf adds its type.
func ColumnNameIn(schema *ir.Schema, f *ir.FieldDef) string {
	if IsTableRelation(schema, f) && !f.TypeRef.IsArray {
		return ForeignKeyColumn(f)
	}
	return ColumnName(f)
}

// FieldNamed is td's field named name, or nil.
func FieldNamed(td *ir.TypeDef, name string) *ir.FieldDef {
	if td == nil {
		return nil
	}
	for _, f := range td.Fields {
		if f != nil && f.Name == name {
			return f
		}
	}
	return nil
}

// KeyColumnIn is the column an @index key of td, a table of schema, names:
// its field's column (ColumnNameIn), or the key in snake case when td has no
// field of its name.
func KeyColumnIn(schema *ir.Schema, td *ir.TypeDef, key string) string {
	if f := FieldNamed(td, key); f != nil {
		return ColumnNameIn(schema, f)
	}
	return ColumnNameOf(key)
}

// AuditColumnIn is the column of td's audit field name (AuditCreatedAt,
// ...): its field's column, or the field's name in snake case when td
// has no such field.
func AuditColumnIn(td *ir.TypeDef, name string) string {
	if f := FieldNamed(td, name); f != nil {
		return ColumnName(f)
	}
	return ColumnNameOf(name)
}

// KeyColumnOf is the column of td's primary key: its @key field's, else
// its field named id's, else id, the column the generator adds.
func KeyColumnOf(td *ir.TypeDef) string {
	if td != nil {
		for _, f := range td.Fields {
			if f != nil && f.Key {
				return ColumnName(f)
			}
		}
	}
	if f := FieldNamed(td, "id"); f != nil {
		return ColumnName(f)
	}
	return "id"
}

// Qualify is column of table as a condition names it: each quoted where it
// must be (QuoteIdentifier), joined by a dot.
func Qualify(table, column string) string {
	return QuoteIdentifier(table) + "." + QuoteIdentifier(column)
}

// The audit fields a table may carry, by schema name. createdAt and
// updatedAt are stamped by the database and by an update; createdBy,
// updatedBy and deletedBy by the acting user; deletedAt by a soft delete.
const (
	AuditCreatedAt = "createdAt"
	AuditUpdatedAt = "updatedAt"
	AuditCreatedBy = "createdBy"
	AuditUpdatedBy = "updatedBy"
	AuditDeletedAt = "deletedAt"
	AuditDeletedBy = "deletedBy"
)

// AuditField reports whether name is one of the audit fields.
func AuditField(name string) bool {
	switch name {
	case AuditCreatedAt, AuditCreatedBy, AuditUpdatedAt, AuditUpdatedBy, AuditDeletedAt, AuditDeletedBy:
		return true
	}
	return false
}

// VersionColumn is the column a @versioned or @optimistic table keeps its
// row version in.
const VersionColumn = "_version"

// ScalarClass is what the ORMs know of a scalar: its traits and its SQL
// type mapping (POINT for Geo.Location, INTERVAL for Temporal.Duration).
type ScalarClass struct {
	Traits  codegen.ScalarTraits
	SQLType string
}

// ScalarClasses returns each scalar of schema by name, as the ORMs classify
// a field of it.
func ScalarClasses(schema *ir.Schema) map[string]ScalarClass {
	classes := make(map[string]ScalarClass, len(schema.Scalars))
	for name, def := range schema.Scalars {
		classes[name] = ScalarClass{
			Traits:  codegen.BuildScalarTraits(def, codegen.BuildScalarTokens(name), ""),
			SQLType: def.TypeMappings["sql"],
		}
	}
	return classes
}

// ColumnClass is what a column holds, as both ORMs bind, read back, filter
// and sort it. The flags with Like are the field type's traits; the others
// say how the column stores it.
type ColumnClass struct {
	UUIDLike     bool
	DateTimeLike bool
	DateLike     bool
	StringLike   bool
	IntLike      bool
	FloatLike    bool
	BoolLike     bool
	JSONLike     bool

	// JSON is a value the column holds as JSONB: a @jsonField field or
	// type, a map, a list of lists, or a single Generic.JSON.
	JSON bool
	// List is a list the column holds as a native Postgres array.
	List bool
	// Point is a POINT column, or each element of a POINT[]; Interval an
	// INTERVAL, or each element of an INTERVAL[].
	Point    bool
	Interval bool
	// Vector is a vector(n) column, an Embedding.Vector with dimensions
	// (D84), which both ORMs bind and read as pgvector's text, [1,2,3].
	// Decimal is a NUMERIC column, or each element of a NUMERIC[], a
	// Generic.Decimal whose text reads back canonical.
	Vector  bool
	Decimal bool
	// AnyJSON is a scalar that holds any JSON value (Generic.JSON).
	AnyJSON bool
	// Enum and Union are an enum and a union type, local or imported.
	Enum  bool
	Union bool
	// Scalar is a scalar type, as opposed to a primitive, an enum or an
	// object type.
	Scalar bool
	// SQLType is the column's SQL type, of each element for a list, as
	// ScalarSQLTypes maps the field's type.
	SQLType string
}

// ClassifyColumn classifies the column of the field f, whose type is a
// scalar of class scalar when isScalar, and an enum or a union when the
// flags say so. It is the Go ORM's classification, which the TypeScript
// repositories take too. It leaves SQLType to the caller, which knows the
// schema's SQL types (ScalarSQLTypes).
func ClassifyColumn(f *ir.FieldDef, schema *ir.Schema, scalar ScalarClass, isScalar, isEnum, isUnion bool) ColumnClass {
	ref := f.TypeRef
	traits := scalar.Traits
	if !isScalar {
		traits = codegen.GuessScalarTraits(ref.Name)
	}
	target := schema.Types[ref.Name]
	jsonPayload := target != nil && target.JsonField
	explicitJSONNull := isScalar && ref.Name == "Generic.JSON" && !ref.IsArray && !ref.IsMap
	stored := isScalar && !ref.IsMap && !f.JsonField && !ref.IsArrayOfArrays
	class := ColumnClass{
		UUIDLike:     traits.IsUUIDLike,
		DateTimeLike: traits.IsDateTimeLike,
		DateLike:     traits.IsDateLike,
		StringLike:   traits.IsStringLike || ref.Name == codegen.PrimitiveString,
		IntLike:      traits.IsIntegerLike,
		FloatLike:    traits.IsFloatLike || ref.Name == codegen.PrimitiveNumber,
		BoolLike:     traits.IsBooleanLike || ref.Name == codegen.PrimitiveBoolean,
		JSONLike:     traits.IsJSONLike || traits.IsObjectLike || ref.IsMap,
		JSON:         f.JsonField || ref.IsMap || ref.IsArrayOfArrays || jsonPayload || explicitJSONNull,
		Point:        stored && strings.EqualFold(scalar.SQLType, "POINT"),
		Interval:     stored && strings.EqualFold(scalar.SQLType, "INTERVAL"),
		Vector:       stored && !ref.IsArray && ref.Name == ir.VectorScalarName && f.ValidateDimensions != nil,
		Decimal:      stored && strings.EqualFold(scalar.SQLType, "NUMERIC"),
		AnyJSON:      isScalar && traits.IsAnyJSON,
		Enum:         isEnum,
		Union:        isUnion,
		Scalar:       isScalar,
	}
	class.List = ref.IsArray && !class.JSON
	return class
}

// FilterKind is the kind of a column's filter, which decides its
// operations (FilterOps). FilterNone is a column a filter does not compare:
// a list, a JSON value.
type FilterKind string

const (
	FilterNone     FilterKind = ""
	FilterUUID     FilterKind = "uuid"
	FilterString   FilterKind = "string"
	FilterInt      FilterKind = "int"
	FilterFloat    FilterKind = "float"
	FilterBool     FilterKind = "bool"
	FilterDateTime FilterKind = "datetime"
	FilterDate     FilterKind = "date"
	FilterDuration FilterKind = "duration"
	FilterPoint    FilterKind = "point"
	// FilterJSON is a Generic.JSON column's: equality of JSON values, as
	// JSONB compares them, and IS NULL (D79).
	FilterJSON FilterKind = "json"
	// FilterDecimal is a NUMERIC column's (D84): its values compare as
	// numbers, each bound as its text.
	FilterDecimal FilterKind = "decimal"
)

// FilterKind is the kind of the filter on a column of the class: the first
// that holds of a list or a vector (none), a column of any JSON value
// (Generic.JSON's JSONB equality), a POINT, an INTERVAL, a UUID, a time, a
// date, text, an integer, a number and a boolean; any other value but JSON
// compares as text, and other JSON, an object's or a map's, not at all.
func (c ColumnClass) FilterKind(isArray bool) FilterKind {
	switch {
	case isArray, c.Vector:
		return FilterNone
	case c.AnyJSON:
		return FilterJSON
	case c.Point:
		return FilterPoint
	case c.Interval:
		return FilterDuration
	case c.Decimal:
		return FilterDecimal
	case c.UUIDLike:
		return FilterUUID
	case c.DateTimeLike:
		return FilterDateTime
	case c.DateLike:
		return FilterDate
	case c.StringLike:
		return FilterString
	case c.IntLike:
		return FilterInt
	case c.FloatLike:
		return FilterFloat
	case c.BoolLike:
		return FilterBool
	case !c.JSONLike && !c.JSON:
		return FilterString
	}
	return FilterNone
}

// The operations of a filter, as both ORMs name them: Eq, In, ILike, Gt,
// Gte, Lt, Lte, Between and IsNull in Go, eq, in, ilike, gt, gte, lt, lte,
// between and isNull in TypeScript.
const (
	FilterEq      = "eq"
	FilterIn      = "in"
	FilterILike   = "ilike"
	FilterGt      = "gt"
	FilterGte     = "gte"
	FilterLt      = "lt"
	FilterLte     = "lte"
	FilterBetween = "between"
	FilterIsNull  = "isNull"
)

// filterOps are the operations of each kind of filter, in the order a
// filter writes their conditions.
var filterOps = map[FilterKind][]string{
	FilterUUID:     {FilterEq, FilterIn, FilterIsNull},
	FilterString:   {FilterEq, FilterIn, FilterILike, FilterIsNull},
	FilterInt:      {FilterEq, FilterGt, FilterGte, FilterLt, FilterLte, FilterBetween, FilterIn, FilterIsNull},
	FilterFloat:    {FilterEq, FilterGt, FilterGte, FilterLt, FilterLte, FilterBetween, FilterIn, FilterIsNull},
	FilterDateTime: {FilterEq, FilterGt, FilterGte, FilterLt, FilterLte, FilterBetween, FilterIsNull},
	FilterDate:     {FilterEq, FilterGt, FilterGte, FilterLt, FilterLte, FilterBetween, FilterIn, FilterIsNull},
	FilterDuration: {FilterEq, FilterGt, FilterGte, FilterLt, FilterLte, FilterBetween, FilterIsNull},
	FilterBool:     {FilterEq, FilterIsNull},
	FilterPoint:    {FilterIsNull},
	FilterJSON:     {FilterEq, FilterIn, FilterIsNull},
	FilterDecimal:  {FilterEq, FilterGt, FilterGte, FilterLt, FilterLte, FilterBetween, FilterIn, FilterIsNull},
}

// FilterOps are the operations of a filter of kind, in the order its
// conditions are written; none for FilterNone.
func FilterOps(kind FilterKind) []string {
	return filterOps[kind]
}

// FilterConditions are a column's conditions for each operation, over the
// column as Qualify names it. Each holds one placeholder, $%d, the
// parameter's number, but In, whose %s is its placeholders joined by ", ",
// and IsNull and IsNotNull, which hold none. Between is its two conditions,
// Gte then Lte.
type FilterConditions struct {
	Eq, Gt, Gte, Lt, Lte, ILike string
	In                          string
	IsNull, IsNotNull           string
}

// FilterConditionsOf are the conditions of the column qualified, as
// Qualify writes it, from the clauses (PostgresClauses).
func FilterConditionsOf(qualified string) FilterConditions {
	c := PostgresClauses()
	on := func(clause string) string { return strings.Replace(clause, "%s", qualified, 1) }
	return FilterConditions{
		Eq:        on(c.Eq),
		Gt:        on(c.Gt),
		Gte:       on(c.Gte),
		Lt:        on(c.Lt),
		Lte:       on(c.Lte),
		ILike:     on(c.ILike),
		In:        on(c.In),
		IsNull:    on(c.IsNull),
		IsNotNull: on(c.IsNotNull),
	}
}

// SearchCondition is the condition of one word of a search text (D62) on a
// table's search column, qualified: its pattern is the parameter.
func SearchCondition(qualified string) string {
	return strings.Replace(PostgresClauses().Search, "%s", qualified, 1)
}

// TableModel is a table as the ORMs read and write it.
type TableModel struct {
	// Type is the table type, Name its table, unquoted, and Quoted its
	// table as a statement names it.
	Type   string
	Name   string
	Quoted string

	// Key is the primary key's column, unquoted, and KeyField its field.
	Key      string
	KeyField string

	// Columns are the table's columns in the order every statement lists
	// them, the DDL's: its fields and the foreign keys of its to-one
	// relations in schema order, an added id first and _version last.
	Columns []ColumnModel

	// HasMany are the table's one-to-many relations, which hold no column.
	HasMany []RelationModel

	// SoftDelete is a table with a deletedAt field, which a delete stamps
	// rather than removes; DeletedBy one whose delete also stamps deletedBy.
	SoftDelete bool
	DeletedBy  bool

	// Versioned is a @versioned table, and HasVersion one with the _version
	// column, @versioned or @optimistic. HistoryActor is the column a
	// versioned table's delete tombstone takes its actor in, or "".
	Versioned    bool
	HasVersion   bool
	HistoryActor string

	// Search is a table with search fields, whose filter holds a search
	// text over its search_text column.
	Search bool

	// UniqueKeys are the unique keys an upsert may be keyed by: the
	// primary key, each @unique field and each unique @index.
	UniqueKeys []UniqueKey

	// ReferencedBy are the to-one relations of other tables, and of this
	// one, that point at the table, sorted by filter name.
	ReferencedBy []Reference
}

// ColumnModel is a column of a table.
type ColumnModel struct {
	// Field is the schema field the column stores, or "id" or "_version"
	// for a column the generator adds. For a relation it is the relation
	// field, "customer", whose column is customer_id.
	Field string
	// Name is the column, unquoted, Quoted as a statement names it and
	// Qualified with its table, as a condition names it.
	Name      string
	Quoted    string
	Qualified string

	// Def is the field, nil for an added column.
	Def *ir.FieldDef

	// Relation is the target type of a to-one relation, whose key the
	// column holds, and "" for any other column.
	Relation string

	Key           bool
	AutoGenerated bool
	Required      bool
	Internal      bool
	Secret        bool
	// Encrypted is an Encrypted<T> field's column (D85): its text is the
	// ciphertext's envelope, which the ORMs seal on write and open on read
	// under the row's key, and no filter holds it.
	Encrypted bool
	// Audit is the audit field the column is (AuditCreatedAt, ...), or "".
	Audit string
	// Default is the schema's default of the field, nil without one.
	Default *string
	// EnumDefault is the schema default of a required, single enum field,
	// which an insert writes when the input leaves the field unset.
	EnumDefault string

	Class ColumnClass
	// Type is the column's type as the DDL writes it (ColumnType): JSONB
	// for a list of lists, a map and a @jsonField value (D83), a list its
	// element's type with []. Empty for a column the generator adds.
	Type string
	// Filter is the kind of the column's filter: FilterNone for a column
	// the filter holds no condition on, and for an audit field but
	// createdAt and updatedAt, which the filter does not hold.
	Filter FilterKind
	// Sortable is a column FindPage orders by (D60, amended).
	Sortable bool
}

// EncryptedColumns are the table's Encrypted<T> columns, in column order
// (D85); none for most tables.
func (t *TableModel) EncryptedColumns() []ColumnModel {
	var out []ColumnModel
	for _, c := range t.Columns {
		if c.Encrypted {
			out = append(out, c)
		}
	}
	return out
}

// GeneratesKey reports whether the ORMs generate the key of a row a create
// leaves unset, rather than leave it to the database's default: a table
// with an Encrypted<T> column, whose ciphertext is bound to the row's key
// before the insert, and a UUID key (D85). Verification refuses any other
// key the database generates on such a table.
func (t *TableModel) GeneratesKey() bool {
	key := t.KeyColumn()
	return key != nil && key.Class.UUIDLike && len(t.EncryptedColumns()) > 0
}

// IsArray reports whether the column's field is a list.
func (c ColumnModel) IsArray() bool {
	return c.Def != nil && c.Def.TypeRef.IsArray
}

// RelationModel is a one-to-many relation of a table.
type RelationModel struct {
	Field  string
	Target string
}

// UniqueKey is a key an upsert may be keyed by: Fields are its schema
// fields, Name them joined by ",", Columns its columns, and Conflict the
// conflict target of an upsert on it, "(sku)", or with the predicate of a
// partial index, "(product_id, author_id) WHERE deleted_at IS NULL", or
// over an expression index's expression, "((lower(email)))". Keep
// is the assignment an upsert that sets nothing writes, so the conflicting
// row is returned: its first column set to itself.
type UniqueKey struct {
	Name     string
	Fields   []string
	Columns  []string
	Conflict string
	Keep     string
}

// Reference is a to-one relation of another table, Source, through its
// field Field, whose column is Column, that points at a table: a filter of
// the table finds the rows such a row does, or does not, point at.
type Reference struct {
	// Filter is the filter's field: ReferencedBy, then Source, then the
	// field in Pascal case.
	Filter       string
	Source       string
	Field        string
	SourceTable  string
	SourceColumn string
	// SourceSoftDelete is a source table with deletedAt: a probe counts its
	// live rows alone unless it asks for deleted ones too. SourceDeletedAt
	// is its deletedAt column, quoted where it must be.
	SourceSoftDelete bool
	SourceDeletedAt  string
}

// TableTypes are the types of schema the ORMs keep tables of, sorted by
// name: those of role DBTable, but a @jsonField type, a base class (whose
// fields arrive on each subclass), a type with no field and an engine type,
// which the engine keeps (D58).
func TableTypes(schema *ir.Schema) []*ir.TypeDef {
	schema = codegen.WithoutEngineTypes(schema)
	bases := codegen.BaseTypeNames(schema)
	var defs []*ir.TypeDef
	for _, td := range schema.Types {
		if td.Role != ir.RoleDBTable || td.JsonField || bases[td.Name] || len(td.Fields) == 0 {
			continue
		}
		defs = append(defs, td)
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	return defs
}

// relationalTarget reports whether typeName names a table type of schema.
func relationalTarget(schema *ir.Schema, typeName string) bool {
	td := schema.Types[typeName]
	return td != nil && td.Role == ir.RoleDBTable && !td.JsonField
}

// EnumNames are the enums of schema and the enums it imports from the
// dependencies it names.
func EnumNames(schema *ir.Schema, dependencies map[string]*ir.Schema) map[string]bool {
	enums := make(map[string]bool, len(schema.Enums))
	for name := range schema.Enums {
		enums[name] = true
	}
	for _, imported := range schema.Imports {
		dependency := dependencies[importedSchema(imported.Package)]
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

// UnionNames are the unions of schema and the unions it imports from the
// dependencies it names.
func UnionNames(schema *ir.Schema, dependencies map[string]*ir.Schema) map[string]bool {
	unions := make(map[string]bool, len(schema.Unions))
	for name := range schema.Unions {
		unions[name] = true
	}
	for _, imported := range schema.Imports {
		dependency := dependencies[importedSchema(imported.Package)]
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

// importedSchema is the schema an import's package names, its last segment.
func importedSchema(pkg string) string {
	if slash := strings.LastIndex(pkg, "/"); slash >= 0 {
		return pkg[slash+1:]
	}
	return pkg
}

// TablesOf models every table of schema (TableTypes), in that order, its
// imported enums and unions read from dependencies.
func TablesOf(schema *ir.Schema, dependencies map[string]*ir.Schema) ([]TableModel, error) {
	schema = codegen.WithoutEngineTypes(schema)
	scalars := ScalarClasses(schema)
	sqlTypes := ScalarSQLTypes(schema)
	enums := EnumNames(schema, dependencies)
	unions := UnionNames(schema, dependencies)
	var tables []TableModel
	for _, td := range TableTypes(schema) {
		table, err := tableOf(td, schema, sqlTypes, scalars, enums, unions)
		if err != nil {
			return nil, fmt.Errorf("table %s: %w", td.Name, err)
		}
		tables = append(tables, table)
	}
	attachReferences(tables)
	return tables, nil
}

func tableOf(td *ir.TypeDef, schema *ir.Schema, sqlTypes map[string]string, scalars map[string]ScalarClass, enums, unions map[string]bool) (TableModel, error) {
	name := TableName(td)
	table := TableModel{
		Type:       td.Name,
		Name:       name,
		Quoted:     QuoteIdentifier(name),
		Versioned:  td.Versioned,
		HasVersion: td.Versioned || td.Optimistic,
	}
	for _, f := range td.Fields {
		switch f.Name {
		case AuditDeletedAt:
			table.SoftDelete = true
		case AuditDeletedBy:
			table.DeletedBy = true
		}
	}
	for _, f := range td.Fields {
		ref := f.TypeRef
		if !f.JsonField && !ref.IsMap && relationalTarget(schema, ref.Name) {
			if ref.IsArrayOfArrays {
				return table, fmt.Errorf("field %s.%s is an array of arrays of table type %s, which cannot be a relation; store a list of lists of its keys or of a @jsonField type", td.Name, f.Name, ref.Name)
			}
			if ref.IsArray && f.ManyToMany {
				continue
			}
			if ref.IsArray && !f.HasMany {
				return table, fmt.Errorf("field %s.%s is a list of table type %s but lacks @hasMany or @manyToMany", td.Name, f.Name, ref.Name)
			}
			if ref.IsArray {
				table.HasMany = append(table.HasMany, RelationModel{Field: f.Name, Target: ref.Name})
				continue
			}
			column := ForeignKeyColumn(f)
			table.Columns = append(table.Columns, ColumnModel{
				Field:     f.Name,
				Name:      column,
				Quoted:    QuoteIdentifier(column),
				Qualified: Qualify(name, column),
				Def:       f,
				Relation:  ref.Name,
				Required:  f.Required,
				Class:     ColumnClass{UUIDLike: true, SQLType: "UUID"},
				Filter:    FilterUUID,
			})
			continue
		}
		scalar, isScalar := scalars[ref.Name]
		_, localEnum := schema.Enums[ref.Name]
		class := ClassifyColumn(f, schema, scalar, isScalar, localEnum || enums[ref.Name], unions[ref.Name])
		class.SQLType = ElementType(ref.Name, sqlTypes)
		column := ColumnName(f)
		c := ColumnModel{
			Field:         f.Name,
			Name:          column,
			Quoted:        QuoteIdentifier(column),
			Qualified:     Qualify(name, column),
			Def:           f,
			Key:           f.Key,
			AutoGenerated: f.AutoGenerated,
			Required:      f.Required,
			Internal:      f.InternalMetadata,
			Secret:        f.Secret || f.EncryptedAtRest,
			Encrypted:     f.EncryptedAtRest,
			Default:       f.Default,
			Class:         class,
			Type:          ColumnType(schema, f, sqlTypes),
		}
		if AuditField(f.Name) {
			c.Audit = f.Name
		}
		if class.Enum && f.Required && !ref.IsArray && f.Default != nil {
			c.EnumDefault = *f.Default
		}
		if (c.Audit == "" || c.Audit == AuditCreatedAt || c.Audit == AuditUpdatedAt) && !c.Encrypted {
			c.Filter = class.FilterKind(ref.IsArray)
		}
		c.Sortable = c.Key || sortableColumn(c, ir.SortValue(schema, ref))
		if f.SearchField {
			table.Search = true
		}
		if f.Key {
			table.Key, table.KeyField = column, f.Name
		}
		table.Columns = append(table.Columns, c)
	}
	if table.Key == "" {
		id := ColumnModel{
			Field:         "id",
			Name:          "id",
			Quoted:        QuoteIdentifier("id"),
			Qualified:     Qualify(name, "id"),
			Key:           true,
			AutoGenerated: true,
			Required:      true,
			Class:         ColumnClass{UUIDLike: true, Scalar: true, SQLType: "UUID"},
			Filter:        FilterUUID,
			Sortable:      true,
		}
		table.Columns = append([]ColumnModel{id}, table.Columns...)
		table.Key, table.KeyField = "id", "id"
	}
	if table.HasVersion && table.Column(VersionColumn) == nil {
		table.Columns = append(table.Columns, ColumnModel{
			Field:         VersionColumn,
			Name:          VersionColumn,
			Quoted:        QuoteIdentifier(VersionColumn),
			Qualified:     Qualify(name, VersionColumn),
			AutoGenerated: true,
			Required:      true,
			Internal:      true,
			Class:         ColumnClass{IntLike: true, SQLType: "BIGINT"},
			Filter:        FilterInt,
		})
	}
	if table.Versioned {
		table.HistoryActor = VersionedHistory(td, func(column string) bool { return table.Column(column) != nil }).Actor
	}
	keys, err := uniqueKeys(td, &table)
	if err != nil {
		return table, err
	}
	table.UniqueKeys = keys
	return table, nil
}

// sortableColumn reports whether FindPage orders a table's rows by a
// column other than the key (D60, amended): sortValue is ir.SortValue of
// its type, one value of text, a time or a number. The column must also
// hold it alone and never null, be no enum, boolean, JSON or point, and be
// the table's own data: no internal metadata, no audit field but createdAt
// and updatedAt, and no @secret, whose values an order would reveal.
func sortableColumn(c ColumnModel, sortValue bool) bool {
	cl := c.Class
	if c.Relation != "" || !sortValue || !c.Required || c.IsArray() || (c.Def != nil && c.Def.TypeRef.IsMap) || cl.Union || cl.JSON || cl.JSONLike {
		return false
	}
	return !cl.Enum && !cl.BoolLike && !cl.Point && !c.Secret && !c.Internal &&
		(c.Audit == "" || c.Audit == AuditCreatedAt || c.Audit == AuditUpdatedAt)
}

// VectorColumns are the table's vector(n) columns (ColumnClass.Vector), in
// column order: the fields nearest finds neighbours by (D84).
func (t *TableModel) VectorColumns() []ColumnModel {
	var out []ColumnModel
	for _, c := range t.Columns {
		if c.Class.Vector {
			out = append(out, c)
		}
	}
	return out
}

// Column returns the column named name, or nil.
func (t *TableModel) Column(name string) *ColumnModel {
	for i := range t.Columns {
		if t.Columns[i].Name == name {
			return &t.Columns[i]
		}
	}
	return nil
}

// AuditQuoted is the column of the audit field name as a statement names
// it: the table's, as @column names it (D85), else the field's name in
// snake case.
func (t *TableModel) AuditQuoted(name string) string {
	if c := t.AuditColumn(name); c != nil {
		return c.Quoted
	}
	return QuoteIdentifier(ColumnNameOf(name))
}

// KeyColumn returns the primary key's column.
func (t *TableModel) KeyColumn() *ColumnModel {
	return t.Column(t.Key)
}

// AuditColumn returns the column of the audit field name, or nil.
func (t *TableModel) AuditColumn(name string) *ColumnModel {
	for i := range t.Columns {
		if t.Columns[i].Audit == name {
			return &t.Columns[i]
		}
	}
	return nil
}

// SortColumns are the columns FindPage orders by: the key first, then each
// sortable column in order.
func (t *TableModel) SortColumns() []ColumnModel {
	var out []ColumnModel
	for _, c := range t.Columns {
		if c.Key {
			out = append([]ColumnModel{c}, out...)
		} else if c.Sortable {
			out = append(out, c)
		}
	}
	return out
}

// columnOfKey is the column an @index key names: its field's column, or the
// foreign key of a to-one relation field (IndexKeyMatches).
func (t *TableModel) columnOfKey(key string) *ColumnModel {
	for i := range t.Columns {
		if t.Columns[i].Field == key || IndexKeyMatches(key, t.Columns[i].Name) {
			return &t.Columns[i]
		}
	}
	return nil
}

// uniqueKeys are the table's unique keys: its primary key, each @unique
// field and each unique @index, in that order, a set of fields once.
func uniqueKeys(td *ir.TypeDef, table *TableModel) ([]UniqueKey, error) {
	var keys []UniqueKey
	seen := map[string]bool{}
	// idx is the unique @index a key is, nil for the primary key and a
	// @unique field.
	add := func(fields []string, idx *ir.IndexDef) error {
		key := UniqueKey{Name: strings.Join(fields, ","), Fields: fields}
		if seen[key.Name] {
			return nil
		}
		seen[key.Name] = true
		deletedAt := false
		for _, field := range fields {
			column := table.columnOfKey(field)
			if column == nil {
				return fmt.Errorf("unique key %q names no column of table %s", key.Name, table.Name)
			}
			key.Columns = append(key.Columns, column.Name)
			deletedAt = deletedAt || column.Audit == AuditDeletedAt
		}
		quoted := make([]string, len(key.Columns))
		for i, column := range key.Columns {
			quoted[i] = QuoteIdentifier(column)
		}
		// The conflict target infers the index sqlgen writes: over its
		// expression, when it has one (D83), and naming its predicate, the
		// @index's where and, on a soft-deletable table, the live rows
		// alone (IndexPredicate).
		key.Conflict = "(" + strings.Join(quoted, ", ") + ")"
		if idx != nil {
			if idx.Expression != "" {
				key.Conflict = "((" + idx.Expression + "))"
			}
			live := ""
			if table.SoftDelete && !deletedAt {
				live = table.AuditQuoted(AuditDeletedAt) + " IS NULL"
			}
			if predicate := IndexPredicate(idx.Where, live); predicate != "" {
				key.Conflict += " WHERE " + predicate
			}
		}
		key.Keep = quoted[0] + " = EXCLUDED." + quoted[0]
		keys = append(keys, key)
		return nil
	}
	if err := add([]string{table.KeyField}, nil); err != nil {
		return nil, err
	}
	for _, f := range td.Fields {
		if f.Unique && !f.Key {
			if err := add([]string{f.Name}, nil); err != nil {
				return nil, err
			}
		}
	}
	for i := range td.Indexes {
		if idx := &td.Indexes[i]; idx.Unique && len(idx.Keys) > 0 {
			if err := add(append([]string(nil), idx.Keys...), idx); err != nil {
				return nil, err
			}
		}
	}
	return keys, nil
}

// IndexPredicate is the predicate of a partial index, as sqlgen writes it
// after WHERE and an upsert's conflict target names it to infer the index
// (D83): an @index's where, and live, the predicate of a unique index over
// a soft-deletable table's live rows, ANDed with it; "" for a full index.
func IndexPredicate(where, live string) string {
	switch {
	case where == "":
		return live
	case live == "":
		return where
	}
	return "(" + where + ") AND " + live
}

// UniqueKeyNamed returns the unique key named name (its fields joined by
// ","), or nil.
func (t *TableModel) UniqueKeyNamed(name string) *UniqueKey {
	for i := range t.UniqueKeys {
		if t.UniqueKeys[i].Name == name {
			return &t.UniqueKeys[i]
		}
	}
	return nil
}

// attachReferences gives each table the to-one relations that point at it.
func attachReferences(tables []TableModel) {
	byType := make(map[string]int, len(tables))
	for i := range tables {
		byType[tables[i].Type] = i
	}
	for i := range tables {
		source := &tables[i]
		for _, c := range source.Columns {
			if c.Relation == "" {
				continue
			}
			target, ok := byType[c.Relation]
			if !ok {
				continue
			}
			tables[target].ReferencedBy = append(tables[target].ReferencedBy, Reference{
				Filter:           "ReferencedBy" + source.Type + codegen.ToPascalCase(c.Field),
				Source:           source.Type,
				Field:            c.Field,
				SourceTable:      source.Quoted,
				SourceColumn:     c.Quoted,
				SourceSoftDelete: source.SoftDelete,
				SourceDeletedAt:  source.AuditQuoted(AuditDeletedAt),
			})
		}
	}
	for i := range tables {
		refs := tables[i].ReferencedBy
		sort.Slice(refs, func(a, b int) bool { return refs[a].Filter < refs[b].Filter })
	}
}
