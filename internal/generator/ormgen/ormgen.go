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
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/goutil"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
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
	// IndirectModules are the Go types modules the ORM reaches through its
	// types module (Options.ModuleClosure), required as indirect.
	IndirectModules          []string
	Repositories             []Repository
	HasSoftDeletes           bool
	HasVersionedRepositories bool
	HasGenericJSON           bool
	Timestamp                string

	// HasVersionFences is true when a repository has the _version column
	// (@versioned or @optimistic); it emits ErrVersionConflict.
	HasVersionFences bool

	// HasOptimisticTables is true when a repository is @optimistic:
	// it has the fenced methods and no history. The README says so.
	HasOptimisticTables bool

	// HasArraysOfArrays is true when a column is an array of arrays (T[][]);
	// it emits the encoder those columns write through.
	HasArraysOfArrays bool

	// HasJSONListFields is true when a JSONB column holds a list or a list
	// of lists of a non-union type, or a history row holds a native list; it
	// emits the decoder that refuses a null element in one
	// (Field.JSONListDepth, Field.NativeList).
	HasJSONListFields bool

	// HasElementPointerLists is true when a native array column is scanned
	// through pointer elements (Field.ScansElementPointers); it emits the
	// copy that refuses a nil one.
	HasElementPointerLists bool

	// HasGenericJSONLists is true when a column holds a Generic.JSON list
	// (Field.IsGenericJSONList); it emits the check that refuses a null
	// element in one before a write and after a native array is scanned.
	HasGenericJSONLists bool

	// GeoPointListTypes are the element types of the POINT[] list columns
	// (Field.IsGeoPointList), one per Go type and sorted; utils.go emits the
	// helpers that convert a list of each to and from []pgtype.Point.
	GeoPointListTypes []GeoPointListType

	// HasIntervalLists is true when a table has an INTERVAL[] list column
	// (Field.IsIntervalList); utils.go emits the helpers that convert a list
	// of durations to and from []time.Duration.
	HasIntervalLists bool

	// HasVectors is true when a table has a vector(n) column (D84); it
	// writes nearest.go, the vector's text form and each such
	// repository's Nearest. HasVectorHistory and HasDecimalHistory are true
	// when a versioned table has a vector or a NUMERIC column, whose
	// to_jsonb text and number utils.go's history helpers read.
	// HasDecimalFilters is true when a table has a single NUMERIC column,
	// whose filter is a DecimalFilter. NearestMetrics are the metrics
	// Nearest orders by, sqlutil's, and DefaultNearestK the rows it reads
	// unless told.
	HasVectors        bool
	HasVectorHistory  bool
	HasDecimalHistory bool
	HasDecimalFilters bool
	NearestMetrics    []sqlutil.NearestMetric
	DefaultNearestK   int

	// HasIntervalHistory is true when a versioned table has an INTERVAL or
	// INTERVAL[] column; utils.go emits the helpers that read the interval
	// text to_jsonb writes in a history row.
	HasIntervalHistory bool

	// UUIDGoType is the Go type used for primary-key / foreign-key plumbing
	// (e.g. "types.IdentityUUID"). Resolved from the schema's UUID-like
	// scalar; all UUID-like superscalar aliases share one underlying type, so
	// a single plumbing type interoperates across tables.
	UUIDGoType string

	// UserIDGoType is the Go type carried in the ORM user context and
	// assigned to createdBy/updatedBy audit fields. Resolved from the audit
	// fields' own scalar, falling back to UUIDGoType.
	UserIDGoType string

	// Actor is the system actor AsSystem acts as and whether an update
	// needs an acting user (D87).
	Actor sqlutil.Actor

	// Naming supplies the scalar and schema-ir module paths go.mod requires.
	Naming naming.Naming

	// HasHistoryActor is true when a versioned repository has a history
	// actor column (Repository.HistoryActorCol); it emits the helpers its
	// hard deletes set and clear Naming.HistoryActorSetting through.
	HasHistoryActor bool

	// ScalarLibReplacePath is the go.mod replace target for superscalar,
	// relative to the output directory. Empty omits the directive.
	ScalarLibReplacePath string

	// SchemaIRReplacePath is the go.mod replace target for schema-ir (a
	// transitive dependency via superscalar), relative to the output
	// directory. Empty omits the directive.
	SchemaIRReplacePath string

	// VersionGraphs are the schema's version graphs (D17, D19). Each gets a
	// versiongraph_<name>.go facade over the version-graph engine, and any
	// gets the declarations the facades share in database.go and a
	// requirement on the version-graph runtime's Go module.
	VersionGraphs []VersionGraph

	// VersionGraphReplacePath is the go.mod replace target for the
	// version-graph binding, relative to the output directory. Empty omits
	// the directive; it is only written when the schema has a graph.
	VersionGraphReplacePath string

	// Pins are the runtime modules go.mod takes from the module proxy, at
	// the release that generates it (SetReleasePins).
	Pins naming.Pins

	// Queues are the schema's queues, sorted by name (D53). Any writes
	// queues.go, which enqueues and claims their messages.
	Queues []Queue

	// HasSearch is true when a repository's table has search fields
	// (Repository.HasSearch); it writes search.go, the words of a search
	// text and each word's LIKE pattern, which the filters' SearchText
	// conditions take (D62, amended).
	HasSearch bool

	// HistoryActorCTE and ClearHistoryActor are the statements a hard
	// delete of a versioned table with an actor column runs
	// (sqlutil.HistoryActorCTE), over Naming.HistoryActorSetting.
	HistoryActorCTE   string
	ClearHistoryActor string

	// HasEncryptedFields is a schema with an Encrypted<T> field (D85): it
	// writes encrypted.go, the Database takes Options (WithFieldKeys), and
	// go.mod requires the schema runtime, whose fieldcrypt package seals
	// and opens the envelopes. SchemaRuntimeReplacePath is that module's
	// replace target, relative to the output directory; empty omits it.
	HasEncryptedFields       bool
	SchemaRuntimeReplacePath string

	// DefaultLimit is the rows a read or a Reencrypt batch takes unless
	// told (sqlutil.DefaultLimit).
	DefaultLimit int
}

// ModuleDependencyReplace keeps local generated type dependencies resolvable
// when the ORM module is the main Go module; dependency replace directives are
// not inherited from the generated types module.
type ModuleDependencyReplace struct {
	Module  string
	RelPath string
}

// GeoPointListType is the element type of a POINT[] list column. utils.go
// names its helpers after Symbol (Field.GeoPointListValue,
// Field.GeoPointListCopy, Field.GeoPointListDecodeCall).
type GeoPointListType struct {
	GoType string // e.g. "types.GeoLocation"
	Symbol string // e.g. "GeoLocation"
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
	DeletedAtCol           string // deletedAt's column, as @column names it (D85), which a history image keys
	HasDeletedBy           bool   // deletedBy field present (soft deletes stamp it)
	Versioned              bool   // history table/read methods should be generated
	HasVersion             bool   // _version column and the fenced UpdateOneIfVersion/DeleteOneIfVersion (@versioned or @optimistic)
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

	// HistoryActorCol is, on a versioned table, the column a delete
	// tombstone records its actor in: deleted_by when the table has one,
	// else updated_by, else empty. When it is set, generated hard deletes set
	// the history actor setting from the context user for the statement.
	HistoryActorCol string

	// HasSearch is true when a column of the table is a search field
	// (@searchField), as sqlgen's Table.SearchFields counts them, so the
	// table has the generated search_text column. Its filter then has
	// SearchText, whose words are each a condition on QuotedSearchColumn
	// (D62, amended).
	HasSearch bool

	// QuotedSearchColumn is the search_text column as SQL names it, quoted
	// when it must be; empty when the table has no search fields.
	QuotedSearchColumn string

	// SortFields are the fields FindPage orders the table's rows by (D60,
	// amended): the primary key first, then each Sortable field in schema
	// order. <Type>SortField names one of them, and the generated
	// <type>SortColumns, which maps each to its column, is the only text of
	// a sort that FindPage writes into SQL.
	SortFields []Field

	// Table is the table as sqlutil models it for both ORMs (D79), and SQL
	// its statements, which the repository runs as the TypeScript
	// repositories run them.
	Table *sqlutil.TableModel
	SQL   sqlutil.TableStatements

	// UniqueKeys are the keys UpsertOne may be keyed by (D79), each with
	// the name of its <Type>UniqueKey constant.
	UniqueKeys []UniqueKey

	// SearchCondition is the condition of one word of the filter's
	// SearchText, sqlutil.SearchConditionOf; empty without search fields.
	SearchCondition string

	// EncryptedFields are the table's Encrypted<T> fields (D85), which
	// encrypted.go seals and opens; GeneratesKey is a table whose create
	// generates a UUID key the input leaves unset, since each envelope is
	// bound to its row's key (sqlutil.TableModel.GeneratesKey); and
	// PrimaryKeyIsPointer a key the Go type holds as a pointer.
	EncryptedFields     []Field
	GeneratesKey        bool
	PrimaryKeyIsPointer bool

	// VectorFields are the table's vector(n) fields (D84), which Nearest
	// orders its rows by; a table with one gets Nearest, and findRows
	// collects the distance Nearest selects.
	VectorFields []Field

	// Import requirements computed from the field/relationship shapes so the
	// repository template emits exactly the imports it uses.
	NeedsTime               bool
	NeedsSQLNull            bool
	NeedsReflect            bool
	NeedsPgtype             bool
	HasJSONUnionCollections bool
}

// UniqueKey is a key UpsertOne may be keyed by: sqlutil's, with the Go
// constant that names it, <Type>UniqueBy<Fields>.
type UniqueKey struct {
	sqlutil.UniqueKey
	Const string
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
	IsNullableScalar   bool   // optional scalar typegen emits as *Scalar (an integer-like one only without a default, or DistinctNull)
	IsNullableBool     bool   // optional boolean without a default: typegen emits *bool
	IsUUIDScalar       bool   // non-array UUID-like scalar: values coerce via .ToUUID()
	IsDateTimeScalar   bool   // non-array datetime-like scalar: values coerce via time.Time()
	IsGeoPoint         bool   // single scalar stored as POINT (Geo.Location): values convert through pgtype.Point, x = Lon, y = Lat
	IsGeoPointList     bool   // list of a scalar stored as POINT, a native POINT[] column: values convert through []pgtype.Point
	IsInterval         bool   // single scalar stored as INTERVAL (Temporal.Duration): values convert through time.Duration, whole microseconds
	IsIntervalList     bool   // list of a scalar stored as INTERVAL, a native INTERVAL[] column: values convert through []time.Duration
	IsVector           bool   // an Embedding.Vector stored as vector(n) (D84): values cross as pgvector's text, [1,2,3], which pgx sends and reads for a type it does not know
	IsDecimal          bool   // a scalar stored as NUMERIC (Generic.Decimal, D84), or each element of a NUMERIC[]: superscalar's GenericDecimal binds and scans its canonical text
	Dimensions         int    // a vector's dimensions, from its vector(n) column; 0 for any other field
	IsUUIDLike         bool
	IsStringLike       bool
	IsIntLike          bool
	IsFloatLike        bool
	IsBoolLike         bool
	IsDateTimeLike     bool
	IsDateLike         bool
	IsJSONLike         bool
	IsEnum             bool // the field's type is an enum, local or imported
	IsSecret           bool // @secret: never sent to a client
	// IsEncrypted is an Encrypted<T> field (D85), whose column holds its
	// envelope: a write seals it and a read opens it (encrypted.go).
	IsEncrypted bool

	// Sortable is true when FindPage can order the table's rows by the
	// field's column (D60, amended), as sqlutil's column says
	// (sqlutil.ColumnModel.Sortable): the primary key, or a required column
	// of one value of text, times and UUIDs among it, or a number, as
	// ir.SortValue says of an API's sort field. A column that may be null,
	// or is an enum, a boolean, JSON, a map, a list, a union or a point, is
	// not, and neither is internal metadata or an audit field other than
	// createdAt and updatedAt. A @secret column is not either: a page's
	// order would show it.
	Sortable bool

	// PreservesExplicitJSONNull is true only for a direct Generic.JSON scalar.
	// Its four-byte JSON `null` token is a value, distinct from SQL NULL.
	PreservesExplicitJSONNull bool

	// IsGenericJSONList is true for a list (T[]) or a list of lists (T[][])
	// of a scalar that holds any JSON value (Generic.JSON). Its elements are
	// never null (D12, amended), but its Go element type can hold one: a nil
	// value, which is SQL NULL, or the JSON null token. The repository
	// refuses a null element before a write, and after it scans a native
	// JSONB[] column, where pgx reads one without an error.
	IsGenericJSONList bool

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

	// FilterKind is the kind of the field's filter (sqlutil.FilterKind),
	// whose operations the filter writes conditions for, in the order
	// sqlutil.FilterOps gives; empty for a field it holds no condition on.
	FilterKind sqlutil.FilterKind

	// SQL is the column's parts of the table's statements (D79).
	SQL sqlutil.ColumnStatements
}

// FilterCondition is one operation of a filter as query.go writes its
// condition (D79): Field is the filter's member of the <Type>Filter, f.Rating
// or f.CustomerID, Member the operation's member of it, SQL its condition
// and SQL2 the second one, Between's <= and IsNull's IS NOT NULL. Value is
// the Go expression of the value it binds, of the element Loop names for In,
// and Min and Max are Between's bounds.
type FilterCondition struct {
	Op       string
	Field    string
	Member   string
	SQL      string
	SQL2     string
	Value    string
	Loop     string
	Min, Max string
}

// FilterConditions are the conditions of the field's filter, in the order
// sqlutil.FilterOps gives its kind's operations.
func (f Field) FilterConditions() []FilterCondition {
	return filterConditions("f."+codegen.ToPascalCase(f.Name), f.FilterKind, f.SQL.Filter)
}

// FilterConditions are the conditions of the relationship's foreign key
// filter, f.<Field>ID, a UUID filter.
func (r Relationship) FilterConditions() []FilterCondition {
	return filterConditions("f."+codegen.ToPascalCase(r.FieldName)+"ID", sqlutil.FilterUUID, r.SQL.Filter)
}

// goFilterKind is the Go ORM's filter of a column of kind: kind, but for a
// Generic.JSON column (sqlutil.FilterJSON), whose StringFilter predates D79
// and stays the Go API: its Eq and In bind JSON text, which pgx sends as
// such, so they compare JSONB values as the TypeScript filter's eq and in
// do, and its ILike, which Postgres has no operator for on JSONB, fails.
func goFilterKind(kind sqlutil.FilterKind) sqlutil.FilterKind {
	if kind == sqlutil.FilterJSON {
		return sqlutil.FilterString
	}
	return kind
}

// filterConditions are the conditions of a filter of kind on the member
// field, over its column's conditions sql.
func filterConditions(field string, kind sqlutil.FilterKind, sql sqlutil.FilterConditions) []FilterCondition {
	uuid := kind == sqlutil.FilterUUID
	min, max := "Min", "Max"
	if kind == sqlutil.FilterDateTime || kind == sqlutil.FilterDate {
		min, max = "Start", "End"
	}
	var out []FilterCondition
	for _, op := range sqlutil.FilterOps(kind) {
		c := FilterCondition{Op: op, Field: field}
		switch op {
		case sqlutil.FilterEq:
			c.Member, c.SQL, c.Value = "Eq", sql.Eq, "*"+field+".Eq"
			if uuid {
				c.Value = field + ".Eq.ToUUID()"
			}
		case sqlutil.FilterGt:
			c.Member, c.SQL, c.Value = "Gt", sql.Gt, "*"+field+".Gt"
		case sqlutil.FilterGte:
			c.Member, c.SQL, c.Value = "Gte", sql.Gte, "*"+field+".Gte"
		case sqlutil.FilterLt:
			c.Member, c.SQL, c.Value = "Lt", sql.Lt, "*"+field+".Lt"
		case sqlutil.FilterLte:
			c.Member, c.SQL, c.Value = "Lte", sql.Lte, "*"+field+".Lte"
		case sqlutil.FilterILike:
			c.Member, c.SQL, c.Value = "ILike", sql.ILike, `"%"+*`+field+`.ILike+"%"`
		case sqlutil.FilterIn:
			c.Member, c.SQL, c.Loop, c.Value = "In", sql.In, "value", "value"
			if uuid {
				c.Loop, c.Value = "id", "id.ToUUID()"
			}
		case sqlutil.FilterBetween:
			c.Member, c.SQL, c.SQL2, c.Min, c.Max = "Between", sql.Gte, sql.Lte, min, max
		case sqlutil.FilterIsNull:
			c.Member, c.SQL, c.SQL2 = "IsNull", sql.IsNull, sql.IsNotNull
		}
		out = append(out, c)
	}
	return out
}

// ArrayDepth is 0 for T, 1 for T[] and 2 for T[][], as ir.TypeRef reports it.
func (f Field) ArrayDepth() int {
	return ir.TypeRef{IsArray: f.IsArray, IsArrayOfArrays: f.IsArrayOfArrays}.ArrayDepth()
}

// JSONListDepth is 1 for a JSONB column that holds a list (T[]) and 2 for
// one that holds a list of lists (T[][]), and 0 for any other column. A
// list of every element type counts, Generic.JSON included: a list element
// is never null (D12, amended). A union list is left out; its decoder reads
// each element through the union's wrapper, which refuses a null element
// itself.
func (f Field) JSONListDepth() int {
	if !f.IsJSONField || f.IsMap || f.IsUnion {
		return 0
	}
	return f.ArrayDepth()
}

// NativeList reports whether the field is a list stored in a native
// Postgres array column: a T[] without @jsonField. A T[][] is always a JSONB
// column. A history row is to_jsonb(row), where the column is a JSON list
// whose NULL element is null; the history decoder refuses one.
func (f Field) NativeList() bool {
	return f.IsArray && !f.IsJSONField
}

// NativeGenericJSONList reports whether the field is a Generic.JSON list
// stored in a native JSONB[] column.
func (f Field) NativeGenericJSONList() bool {
	return f.IsGenericJSONList && f.NativeList()
}

// ScansElementPointers reports whether the repository scans the field's
// native array into a list of pointers ([]*T) and copies it into the
// entity's []T, refusing a nil element: every native list but a
// Generic.JSON one and a POINT[] one. A list element is never null (D12,
// amended). pgx reads a SQL NULL element into a pointer as nil. Scanned into
// T itself, a NULL element of a type whose Scan method takes a nil source (a
// UUID, a timestamp, a string scalar such as Identity.Name) reads as T's zero
// value with no error, and one of a string, an enum or a number fails with
// pgx's own message. A Generic.JSON element's Go type holds a null itself, so
// the repository checks that scanned list instead (NativeGenericJSONList). A
// POINT[] is scanned into []pgtype.Point, whose NULL element is invalid
// (GeoPointListCopy). An INTERVAL[] is scanned into []*time.Duration
// (ListElementScanType).
func (f Field) ScansElementPointers() bool {
	return f.NativeList() && !f.IsGenericJSONList && !f.IsGeoPointList
}

// ListElementScanType is the Go type of the element pointers a native list
// is scanned through (ScansElementPointers): time.Duration for an INTERVAL[],
// which pgx reads as microseconds, a day 24 hours and a month 30 days, and
// the element's own Go type for any other.
func (f Field) ListElementScanType() string {
	if f.IsIntervalList {
		return "time.Duration"
	}
	return f.GoType
}

// ListCopyCall is the call that sets dst, a pointer to the field's list,
// from values, the element pointers scanned from its native array
// (ListElementScanType). It returns an error that names the first NULL
// element.
func (f Field) ListCopyCall(values, dst string) string {
	if f.IsIntervalList {
		return fmt.Sprintf("copyIntervalElements(%q, %s, %s)", f.Name, values, dst)
	}
	return fmt.Sprintf("copyListElements(%q, %s, %s)", f.Name, values, dst)
}

// GeoPointListSymbol is the Go type of a POINT[] list's element without its
// package, GeoLocation for types.GeoLocation. It names the helpers utils.go
// converts a list of that type with (ORMOutput.GeoPointListTypes).
func (f Field) GeoPointListSymbol() string {
	return strings.TrimPrefix(f.GoType, "types.")
}

// GeoPointListValue is the call that converts value, a Go expression of the
// POINT[] list's type, to the []pgtype.Point pgx writes: x is the longitude
// and y the latitude. A nil list stays nil, which pgx writes as NULL.
func (f Field) GeoPointListValue(value string) string {
	symbol := f.GeoPointListSymbol()
	return fmt.Sprintf("%s%sPoints(%s)", strings.ToLower(symbol[:1]), symbol[1:], value)
}

// GeoPointListCopy is the call that sets dst, a pointer to the field's list,
// from points, the []pgtype.Point scanned from its POINT[] column. It
// returns an error that names the first NULL element.
func (f Field) GeoPointListCopy(points, dst string) string {
	return fmt.Sprintf("copy%sPoints(%q, %s, %s)", f.GeoPointListSymbol(), f.Name, points, dst)
}

// GeoPointListDecodeCall is the call that decodes the field's POINT[] column
// from a history row, src, into dst. to_jsonb writes the column as a JSON
// list of "(x,y)" texts; the decoder refuses a null element.
func (f Field) GeoPointListDecodeCall(src, dst string) string {
	return fmt.Sprintf("unmarshal%sPoints(%s, %s, %q)", f.GeoPointListSymbol(), src, dst, f.Name)
}

// GenericJSONNullCheck is the call that returns an error naming the first
// null element of the Generic.JSON list value, a Go expression of the
// field's list type.
func (f Field) GenericJSONNullCheck(value string) string {
	if f.IsArrayOfArrays {
		return fmt.Sprintf("refuseNullGenericJSONGridElements(%q, %s)", f.Name, value)
	}
	return fmt.Sprintf("refuseNullGenericJSONElements(%q, %s)", f.Name, value)
}

// GenericJSONListFields returns the repository's Generic.JSON list fields,
// which CreateOne, CreateMany, UpdateOne and UpdateMany check for a null
// element before they write.
func (r Repository) GenericJSONListFields() []Field {
	var fields []Field
	for _, field := range r.Fields {
		if field.IsGenericJSONList {
			fields = append(fields, field)
		}
	}
	return fields
}

// JSONDecodeCall is the call that decodes the field's JSONB column payload,
// src, into dst. A list column's decoder refuses a null element, as the
// generated types' UnmarshalJSON does for a list field (D12, amended); a
// single Generic.JSON column's keeps the JSON null token as a value.
func (f Field) JSONDecodeCall(src, dst string) string {
	switch {
	case f.PreservesExplicitJSONNull:
		return "unmarshalGenericJSONFieldValue(" + src + ", " + dst + ")"
	case f.JSONListDepth() > 0:
		return fmt.Sprintf("unmarshalJSONListFieldValue(%s, %s, %q, %d)", src, dst, f.Name, f.JSONListDepth())
	default:
		return "unmarshalJSONFieldValue(" + src + ", " + dst + ")"
	}
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

// GoPointer reports whether the field's Go representation in the types
// package is a pointer: an auto-generated field's, or a nullable one's
// (DerefValue).
func (f Field) GoPointer() bool {
	return f.DerefValue || f.IsAutoGenerated
}

// KeyArg is the Go expression a FindPage keyset passes for value, a Go
// expression of the primary key's type, as the repository writes the
// column: a UUID through ToUUID and a time as a time.Time.
func (f Field) KeyArg(value string) string {
	switch {
	case f.IsUUIDScalar:
		return value + ".ToUUID()"
	case f.IsDateTimeScalar:
		return "time.Time(" + value + ")"
	default:
		return value
	}
}

// SortExample is the sort field a doc comment shows: the first after the
// primary key, or the primary key when there is none.
func (r Repository) SortExample() string {
	if len(r.SortFields) > 1 {
		return r.SortFields[1].Name
	}
	return r.SortFields[0].Name
}

// SortFieldList names the repository's sort fields by their schema names,
// as Parse<Type>Sort's error lists them: "id, createdAt and name".
func (r Repository) SortFieldList() string {
	names := make([]string, len(r.SortFields))
	for i, field := range r.SortFields {
		names[i] = field.Name
	}
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// PrimaryKeySortField is the repository's primary key field among its sort
// fields, which every key of a page holds.
func (r Repository) PrimaryKeySortField() Field {
	return r.SortFields[0]
}

// sortFields are the fields FindPage orders a table's rows by
// (Repository.SortFields): the primary key first, then each sortable
// field in schema order.
func sortFields(fields []Field) []Field {
	var sorted []Field
	for _, field := range fields {
		if field.IsPrimaryKey {
			sorted = append([]Field{field}, sorted...)
		} else if field.Sortable {
			sorted = append(sorted, field)
		}
	}
	return sorted
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

	// NestedByPointer makes a to-one relationship's Nested field selection
	// a pointer: the target's field selection holds this type's, directly
	// (a self-relation) or through a cycle of to-one relationships, so a
	// value would give the selection struct no finite size.
	NestedByPointer bool

	// For hasMany relationships, these deterministic names point to the
	// child's relation field back to this parent (e.g. "Connector") and the
	// corresponding filter field (e.g. "ConnectorID"). Empty means fallback
	// candidate probing is used at runtime.
	HasManyParentRelationField string
	HasManyParentFilterField   string

	// SQL is the foreign key column's parts of the table's statements
	// (D79), for a to-one relationship.
	SQL sqlutil.ColumnStatements
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

	// Probe is the filter's subquery (sqlutil.ReferenceProbeOf).
	Probe sqlutil.ReferenceProbe
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

	// ModuleClosure lists the Go types modules the types module imports,
	// directly or through one another (the dispatch layer walks the
	// dependency schemas for it). go.mod requires and replaces each: Go
	// takes neither from the types module's go.mod.
	ModuleClosure []string

	// SQLite is whether the DB service lists the sqlite dialect: the ORM
	// then writes queues_sqlite.go, each queue's claims on SQLite over
	// database/sql (D53, amended).
	SQLite bool

	// Actor is the DB service's system actor and whether an update needs
	// an acting user (D87). A zero Actor acts as
	// registry.DefaultSystemActorID.
	Actor sqlutil.Actor

	// Clock stamps the generated output.
	Clock codegen.Clock
}

// Generate generates the Go ORM from a v2 IR schema. Returns nil when the
// schema declares no database tables and no queues.
func Generate(schema *ir.Schema, opts Options) (*ORMOutput, error) {
	if opts.Clock == nil {
		opts.Clock = codegen.DefaultClock()
	}
	opts.Naming = opts.Naming.OrDefault()
	// An engine type gets no repository: the engine keeps its instances
	// (D58).
	schema = codegen.WithoutEngineTypes(schema)

	tableTypes := tableTypeDefs(schema)
	if len(tableTypes) == 0 && len(schema.Queues()) == 0 {
		return nil, nil
	}

	scalarMap := buildScalarLookup(schema)
	unionNames := sqlutil.UnionNames(schema, opts.Dependencies)
	enumNames := sqlutil.EnumNames(schema, opts.Dependencies)
	uuidGoType, err := resolveUUIDGoType(schema, tableTypes, scalarMap)
	if err != nil {
		return nil, err
	}
	// The tables as sqlutil models them, and their statements, which the
	// TypeScript repositories run too (D79).
	tables, err := sqlutil.TablesOf(schema, opts.Dependencies)
	if err != nil {
		return nil, err
	}

	repositories := make([]Repository, 0, len(tableTypes))
	for i, typeDef := range tableTypes {
		repo, err := extractRepository(typeDef, schema, scalarMap, unionNames, enumNames, uuidGoType)
		if err != nil {
			return nil, fmt.Errorf("failed to extract table from type %s: %w", typeDef.Name, err)
		}
		if err := attachTable(&repo, &tables[i]); err != nil {
			return nil, err
		}
		repositories = append(repositories, repo)
	}

	attachInboundReferences(repositories)
	markNestedCycles(repositories)

	graphs, err := versionGraphs(schema, repositories)
	if err != nil {
		return nil, err
	}
	queues, err := queuesOf(schema, scalarMap, unionNames, enumNames, opts.SQLite)
	if err != nil {
		return nil, err
	}
	// A queue's field reads and writes its column as a table's does, with
	// the same helpers.
	queueRepositories := make([]Repository, len(queues))
	for i, q := range queues {
		queueRepositories[i] = Repository{Fields: q.Fields}
	}

	hasSoftDeletes := false
	hasVersionedRepositories := false
	hasVersionFences := false
	hasOptimisticTables := false
	hasHistoryActor := false
	hasGenericJSON := false
	hasArraysOfArrays := false
	hasJSONListFields := false
	hasGenericJSONLists := false
	hasElementPointerLists := false
	hasIntervalLists := false
	hasIntervalHistory := false
	hasVectors, hasVectorHistory, hasDecimalHistory, hasDecimalFilters := false, false, false, false
	hasSearch := false
	hasEncryptedFields := false
	for _, repo := range repositories {
		if repo.HasSearch {
			hasSearch = true
		}
		if len(repo.EncryptedFields) > 0 {
			hasEncryptedFields = true
		}
	}
	geoPointListGoTypes := make(map[string]string)
	for _, repo := range append(slices.Clone(repositories), queueRepositories...) {
		if repo.HasSoftDelete {
			hasSoftDeletes = true
		}
		if repo.Versioned {
			hasVersionedRepositories = true
		}
		if repo.HasVersion {
			hasVersionFences = true
		}
		if repo.HasVersion && !repo.Versioned {
			hasOptimisticTables = true
		}
		if repo.HistoryActorCol != "" {
			hasHistoryActor = true
		}
		for _, field := range repo.Fields {
			if field.PreservesExplicitJSONNull {
				hasGenericJSON = true
			}
			if field.IsArrayOfArrays {
				hasArraysOfArrays = true
			}
			if field.JSONListDepth() > 0 || (field.NativeList() && !field.IsGeoPointList && !field.IsIntervalList && repo.Versioned) {
				hasJSONListFields = true
			}
			if field.ScansElementPointers() && !field.IsIntervalList {
				hasElementPointerLists = true
			}
			if field.IsIntervalList {
				hasIntervalLists = true
			}
			if (field.IsInterval || field.IsIntervalList) && repo.Versioned {
				hasIntervalHistory = true
			}
			if field.IsGenericJSONList {
				hasGenericJSONLists = true
			}
			if field.IsGeoPointList {
				geoPointListGoTypes[field.GeoPointListSymbol()] = field.GoType
			}
			if field.IsDecimal && !field.IsArray {
				hasDecimalFilters = true
			}
			if field.IsVector && repo.Versioned {
				hasVectorHistory = true
			}
			if field.IsDecimal && repo.Versioned {
				hasDecimalHistory = true
			}
		}
		if len(repo.VectorFields) > 0 {
			hasVectors = true
		}
	}
	geoPointListTypes := make([]GeoPointListType, 0, len(geoPointListGoTypes))
	for symbol, goType := range geoPointListGoTypes {
		geoPointListTypes = append(geoPointListTypes, GeoPointListType{GoType: goType, Symbol: symbol})
	}
	sort.Slice(geoPointListTypes, func(i, j int) bool { return geoPointListTypes[i].Symbol < geoPointListTypes[j].Symbol })

	output := &ORMOutput{
		SchemaName:               opts.SchemaName,
		ModulePath:               opts.ModulePath,
		TypesModule:              opts.TypesModule,
		ModuleDependencyReplaces: dependencyTypeModuleReplaces(opts.Naming, opts.Dependencies, opts.ModuleClosure),
		IndirectModules:          goutil.UniqueModules([]string{opts.TypesModule}, opts.ModuleClosure),
		Naming:                   opts.Naming,
		Repositories:             repositories,
		HasSoftDeletes:           hasSoftDeletes,
		HasVersionedRepositories: hasVersionedRepositories,
		HasVersionFences:         hasVersionFences,
		HasOptimisticTables:      hasOptimisticTables,
		HasHistoryActor:          hasHistoryActor,
		HasGenericJSON:           hasGenericJSON,
		HasArraysOfArrays:        hasArraysOfArrays,
		HasJSONListFields:        hasJSONListFields,
		HasGenericJSONLists:      hasGenericJSONLists,
		HasElementPointerLists:   hasElementPointerLists,
		GeoPointListTypes:        geoPointListTypes,
		HasIntervalLists:         hasIntervalLists,
		HasIntervalHistory:       hasIntervalHistory,
		Timestamp:                opts.Clock.RFC3339(),
		UUIDGoType:               uuidGoType,
		UserIDGoType:             resolveUserIDGoType(tableTypes, scalarMap, uuidGoType),
		Actor:                    actorOf(opts.Actor),
		VersionGraphs:            graphs,
		Queues:                   queues,
		HasSearch:                hasSearch,
		HasVectors:               hasVectors,
		HasVectorHistory:         hasVectorHistory,
		HasDecimalHistory:        hasDecimalHistory,
		HasDecimalFilters:        hasDecimalFilters,
		NearestMetrics:           sqlutil.NearestMetrics(),
		DefaultNearestK:          sqlutil.DefaultNearestK,
		HistoryActorCTE:          sqlutil.HistoryActorCTE(opts.Naming.HistoryActorSetting),
		ClearHistoryActor:        sqlutil.ClearHistoryActor(opts.Naming.HistoryActorSetting),
		HasEncryptedFields:       hasEncryptedFields,
		DefaultLimit:             sqlutil.DefaultLimit,
	}

	return output, nil
}

// actorOf is actor with the system actor defaulted: the ID the generated
// Go APIs export as SystemUserID, named "system".
func actorOf(actor sqlutil.Actor) sqlutil.Actor {
	if actor.SystemID == "" {
		actor.SystemID = "00000000-0000-4000-8000-000000000000"
	}
	if actor.SystemName == "" {
		actor.SystemName = "system"
	}
	return actor
}

// dependencyTypeModuleReplaces returns a replace line per declared
// dependency's Go types module and per module of closure, sorted by module.
// The ORM module is built as the main module, and replace directives are not
// inherited from the types module it requires, so every module the types
// module imports, directly or not, must be replaced here too.
func dependencyTypeModuleReplaces(n naming.Naming, dependencies map[string]*ir.Schema, closure []string) []ModuleDependencyReplace {
	n = n.OrDefault()
	relPaths := make(map[string]string, len(dependencies)+len(closure))
	for name := range dependencies {
		relPaths[n.GoTypesModule(name)] = "../../types/go/" + name
	}
	for _, modulePath := range closure {
		relPaths[modulePath] = "../../types/go/" + path.Base(modulePath)
	}
	if len(relPaths) == 0 {
		return nil
	}
	modules := make([]string, 0, len(relPaths))
	for modulePath := range relPaths {
		modules = append(modules, modulePath)
	}
	sort.Strings(modules)
	replaces := make([]ModuleDependencyReplace, 0, len(modules))
	for _, modulePath := range modules {
		replaces = append(replaces, ModuleDependencyReplace{Module: modulePath, RelPath: relPaths[modulePath]})
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
	if len(output.VersionGraphs) > 0 {
		if output.VersionGraphReplacePath, err = naming.RelPath(outputDir, paths.VersionGraphGo); err != nil {
			return fmt.Errorf("version-graph binding replace path: %w", err)
		}
	}
	if output.HasEncryptedFields {
		if output.SchemaRuntimeReplacePath, err = naming.RelPath(outputDir, paths.SchemaRuntimeGo); err != nil {
			return fmt.Errorf("schema runtime replace path: %w", err)
		}
	}
	return nil
}

// SetReleasePins keeps the pins (naming.Naming.ReleasePins) of the runtime
// modules the ORM reaches: the scalar library, the schema IR and, when the
// schema has a version graph, the version-graph binding. go.mod requires
// each pinned module at its pin and replaces every version of it with that
// pin.
func SetReleasePins(output *ORMOutput, pins naming.Pins) {
	modules := []string{output.Naming.ScalarGoModule, output.Naming.SchemaIRGoModule}
	if len(output.VersionGraphs) > 0 {
		modules = append(modules, output.Naming.VersionGraphGoModule)
	}
	if output.HasEncryptedFields {
		modules = append(modules, output.Naming.SchemaRuntimeGoModule)
	}
	output.Pins = pins.Of(modules...)
}

// scalarLookup carries per-scalar naming and trait info for field mapping.
type scalarLookup struct {
	symbol string
	traits codegen.ScalarTraits
	// sqlType is the scalar's sql type mapping, the column type sqlgen
	// stores a single value of it as (POINT for Geo.Location).
	sqlType string
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
			symbol:  symbol,
			traits:  codegen.BuildScalarTraits(scalarDef, tokens, ""),
			sqlType: scalarDef.TypeMappings["sql"],
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

// isRelationalTarget reports whether typeName names a table-backed type in
// this schema.
func isRelationalTarget(typeName string, schema *ir.Schema) bool {
	targetTypeDef := schema.Types[typeName]
	return targetTypeDef != nil && targetTypeDef.Role == ir.RoleDBTable && !targetTypeDef.JsonField
}

// extractRepository extracts repository information from an IR TypeDef.
func extractRepository(typeDef *ir.TypeDef, schema *ir.Schema, scalars map[string]scalarLookup, unionNames, enumNames map[string]bool, uuidGoType string) (Repository, error) {
	tableName := sqlutil.TableName(typeDef)
	repo := Repository{
		Name:            typeDef.Name + "Repository",
		TypeName:        typeDef.Name,
		TableName:       tableName,
		QuotedTableName: sqlutil.QuoteIdentifier(tableName),
		Versioned:       typeDef.Versioned,
		HasVersion:      typeDef.Versioned || typeDef.Optimistic,
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
			repo.DeletedAtCol = sqlutil.ColumnName(field)
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
		if enumNames[field.IRType] && !field.IsEnum {
			field.IsEnum = true
		}
		if fieldDef.SearchField {
			repo.HasSearch = true
			repo.QuotedSearchColumn = sqlutil.QuoteIdentifier(sqlgen.SearchTextColumn)
		}
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
			repo.PrimaryKeyIsPointer = field.DerefValue || field.IsAutoGenerated
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
			Sortable:        true,
			FilterKind:      sqlutil.FilterUUID,
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
		repo.PrimaryKeyIsPointer = true
	}
	repo.LookupKeyType = uuidGoType
	if !repo.PrimaryKeyIsUUID {
		repo.LookupKeyType = repo.PrimaryKeyType
	}
	if repo.HasVersion {
		appendVersionField(&repo)
	}
	if repo.Versioned {
		repo.HistoryActorCol = historyActorColumn(repo.Fields, typeDef)
	}

	repo.SortFields = sortFields(repo.Fields)

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
		FilterKind:         sqlutil.FilterInt,
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
	dbName := sqlutil.ColumnName(fieldDef)

	scalar, isScalar := scalars[irType]
	isUnion := unionNames[irType]
	_, isEnum := schema.Enums[irType]
	// What the column holds, as the TypeScript repositories classify it too
	// (D79): its traits, and whether it is JSONB, a POINT or an INTERVAL.
	// sqlgen stores a scalar mapped to POINT as POINT, and a list of it as
	// POINT[]; a list of lists, a map and @jsonField are JSONB. Likewise a
	// scalar mapped to INTERVAL (Temporal.Duration), whose Go type is a
	// time.Duration. Its Valuer and Scanner write and read Go's duration
	// text, which Postgres reads wrongly or refuses and does not write, so
	// its values cross as time.Duration, which pgx writes as an interval of
	// microseconds (D53, amended).
	class := sqlutil.ClassifyColumn(fieldDef, schema, sqlutil.ScalarClass{Traits: scalar.traits, SQLType: scalar.sqlType}, isScalar, isEnum, isUnion)
	traits := scalar.traits
	if !isScalar {
		traits = codegen.GuessScalarTraits(irType)
	}

	// Arrays and maps are nilable in typegen output ([]T, map[string]T; an
	// optional non-union map holds *T values), so neither is a *T even when
	// the field itself is nullable. An optional integer-like scalar with a
	// declared default is a value type, as typegen declares it, unless its
	// null is distinct (ir.FieldDef.DistinctNull); other optional scalars and
	// enums use pointers.
	isMap := fieldDef.TypeRef.IsMap
	isNullableEnum := !isRequired && !isArray && !isMap && isEnum
	isNullableScalar := !isRequired && !isArray && !isMap && isScalar &&
		(!traits.IsIntegerLike || fieldDef.DistinctNull || codegen.GoOptionalIntegerScalarIsPointer(isRequired, isArray, isMap, fieldDef.Default))
	isNullableBool := codegen.GoOptionalBoolIsPointer(irType, isRequired, isArray, isMap, fieldDef.Default)
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
		IsJSONField:               class.JSON,
		IsScalarType:              isScalar || isEnum,
		IsNullableEnum:            isNullableEnum,
		IsNullableScalar:          isNullableScalar,
		IsNullableBool:            isNullableBool,
		IsUUIDScalar:              isScalar && !isArray && class.UUIDLike,
		IsDateTimeScalar:          isScalar && !isArray && class.DateTimeLike,
		IsGeoPoint:                class.Point && !isArray,
		IsGeoPointList:            class.Point && isArray,
		IsInterval:                class.Interval && !isArray,
		IsIntervalList:            class.Interval && isArray,
		IsVector:                  class.Vector,
		IsDecimal:                 class.Decimal,
		Dimensions:                sqlutil.VectorDimensions(sqlutil.ColumnType(schema, fieldDef, sqlutil.ScalarSQLTypes(schema))),
		IsUUIDLike:                class.UUIDLike,
		IsStringLike:              class.StringLike,
		IsIntLike:                 class.IntLike,
		IsFloatLike:               class.FloatLike,
		IsBoolLike:                class.BoolLike,
		IsDateTimeLike:            class.DateTimeLike,
		IsDateLike:                class.DateLike,
		IsJSONLike:                class.JSONLike,
		IsEnum:                    isEnum,
		IsSecret:                  fieldDef.Secret || fieldDef.EncryptedAtRest,
		IsEncrypted:               fieldDef.EncryptedAtRest,
		PreservesExplicitJSONNull: preservesExplicitJSONNull,
		IsGenericJSONList:         class.AnyJSON && isArray && !isMap,
	}
	if (!field.IsAuditField || field.IsAuditTimestamp) && !field.IsEncrypted {
		field.FilterKind = goFilterKind(class.FilterKind(isArray))
	}

	nilCheckedJSONScalar := field.IsScalarType && field.IsJSONLike && !field.IsArray
	nullableComplex := !field.IsRequired && !field.IsArray && !field.IsMap && !field.IsUnion &&
		strings.HasPrefix(field.GoType, "types.") && !field.IsScalarType
	field.OptionalNilCheck = nilCheckedJSONScalar || nullableComplex || field.IsUnion || field.IsMap ||
		field.IsNullableEnum || field.IsNullableScalar || field.IsNullableBool
	field.DerefValue = nullableComplex || field.IsNullableEnum || field.IsNullableScalar || field.IsNullableBool

	return field
}

// extractRelationship extracts relationship information from an IR FieldDef.
func extractRelationship(fieldDef *ir.FieldDef, parentTypeName string, schema *ir.Schema, scalars map[string]scalarLookup, unionNames map[string]bool) Relationship {
	targetType := fieldDef.TypeRef.Name
	dbColumnName := sqlutil.ForeignKeyColumn(fieldDef)
	targetTableName := sqlutil.TableNameOf(schema, targetType)
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

// attachTable gives repo the table sqlutil models for it (D79): its
// statements, each column's parts of them, its filter kinds, its sort
// fields and its unique keys, which the TypeScript repositories take from
// the same model. The repository and the model must list the same columns in
// the same order, which every statement selects them in; a difference is a
// generator's bug, and fails the build rather than writing SQL the other
// language would not run.
func attachTable(repo *Repository, table *sqlutil.TableModel) error {
	if table.Type != repo.TypeName {
		return fmt.Errorf("ormgen: the repository of %s was given the table of %s", repo.TypeName, table.Type)
	}
	members := make([]string, 0, len(repo.OrderedMembers))
	for _, m := range repo.OrderedMembers {
		if m.IsRelationship {
			members = append(members, m.Relationship.DBColumnName)
		} else {
			members = append(members, m.Field.DBName)
		}
	}
	columns := make([]string, 0, len(table.Columns))
	for _, c := range table.Columns {
		columns = append(columns, c.Name)
	}
	if !slices.Equal(members, columns) {
		return fmt.Errorf("ormgen: the repository of %s lists the columns %v, and sqlutil's table %v", repo.TypeName, members, columns)
	}
	repo.Table = table
	repo.SQL = sqlutil.TableStatementsOf(table)
	if err := repo.SQL.CheckFormats(); err != nil {
		return err
	}
	for i := range repo.Fields {
		field := &repo.Fields[i]
		column := table.Column(field.DBName)
		if field.FilterKind != goFilterKind(column.Filter) {
			return fmt.Errorf("ormgen: %s.%s filters as %q, and sqlutil's column as %q", repo.TypeName, field.Name, field.FilterKind, column.Filter)
		}
		field.SQL = sqlutil.ColumnStatementsOf(column)
		field.Sortable = column.Sortable
	}
	for i := range repo.Relationships {
		if rel := &repo.Relationships[i]; !rel.IsArray {
			rel.SQL = sqlutil.ColumnStatementsOf(table.Column(rel.DBColumnName))
		}
	}
	repo.SortFields = sortFields(repo.Fields)
	repo.SearchCondition = sqlutil.SearchConditionOf(table)
	for _, field := range repo.Fields {
		if field.IsEncrypted {
			repo.EncryptedFields = append(repo.EncryptedFields, field)
		}
		if field.IsVector {
			repo.VectorFields = append(repo.VectorFields, field)
		}
	}
	repo.GeneratesKey = table.GeneratesKey()
	for _, key := range table.UniqueKeys {
		name := repo.TypeName + "UniqueBy"
		for _, field := range key.Fields {
			name += codegen.ToPascalCase(field)
		}
		repo.UniqueKeys = append(repo.UniqueKeys, UniqueKey{UniqueKey: key, Const: name})
	}
	return nil
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
				Probe: sqlutil.ReferenceProbeOf(repositories[target].Table, sqlutil.Reference{
					SourceTable:      source.QuotedTableName,
					SourceColumn:     rel.QuotedDBColumn,
					SourceSoftDelete: source.HasSoftDelete,
					SourceDeletedAt:  source.Table.AuditQuoted(sqlutil.AuditDeletedAt),
				}),
			})
		}
	}

	for i := range repositories {
		sort.Slice(repositories[i].ReferencedBy, func(a, b int) bool {
			return repositories[i].ReferencedBy[a].FilterField < repositories[i].ReferencedBy[b].FilterField
		})
	}
}

// markNestedCycles sets NestedByPointer on every to-one relationship whose
// target's field selection reaches back to the owner's.
func markNestedCycles(repositories []Repository) {
	toOne := make(map[string][]string, len(repositories))
	for _, repo := range repositories {
		for _, rel := range repo.Relationships {
			if !rel.IsArray {
				toOne[repo.TypeName] = append(toOne[repo.TypeName], rel.TargetType)
			}
		}
	}
	reaches := func(from, to string) bool {
		seen := map[string]bool{}
		stack := []string{from}
		for len(stack) > 0 {
			name := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if name == to {
				return true
			}
			if seen[name] {
				continue
			}
			seen[name] = true
			stack = append(stack, toOne[name]...)
		}
		return false
	}
	for i := range repositories {
		for j := range repositories[i].Relationships {
			rel := &repositories[i].Relationships[j]
			rel.NestedByPointer = !rel.IsArray && reaches(rel.TargetType, repositories[i].TypeName)
		}
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

// historyActorColumn returns the column a versioned table's delete
// tombstone records its actor in, as sqlgen's history trigger picks it
// (sqlutil.VersionedHistory): deletedBy's column when the table has one,
// else updatedBy's, else "". An actor column @versioned({ exclude }) leaves
// out of history records none.
func historyActorColumn(fields []Field, td *ir.TypeDef) string {
	return sqlutil.VersionedHistory(td, func(column string) bool {
		return slices.ContainsFunc(fields, func(field Field) bool { return field.DBName == column })
	}).Actor
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
		// A duration is written and read as a time.Duration.
		if field.IsInterval || field.IsIntervalList {
			repo.NeedsTime = true
		}
		if !field.IsJSONField && !field.IsArray && field.IsDateLike && !field.IsPrimaryKey && !field.IsAuditField {
			repo.NeedsPgtype = true
		}
		if field.IsGeoPoint || field.IsGeoPointList {
			repo.NeedsPgtype = true
		}
		// A JSON column (a map among them) scans through []byte, never
		// sql.NullString.
		if !field.IsJSONField && !field.IsRequired && !field.IsPrimaryKey && !field.IsAuditField && !field.IsArray && field.GoType == "string" {
			repo.NeedsSQLNull = true
		}

		// The optional/auto-generated insert path falls back to
		// reflect.ValueOf(...).IsZero() when no nil check applies. This must match
		// the template's reflect-emission guard exactly (CreateOne and CreateMany
		// in repository.tmpl), which also excludes
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
