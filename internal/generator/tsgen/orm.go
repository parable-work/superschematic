package tsgen

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// The repositories of a DB schema in TypeScript (D79): orm.ts, which
// package.json exports as "./orm", holds a typed repository per table, the
// row, create, update and filter types of each, and each table as data,
// its columns, what each holds, and its statements on Postgres, which
// sqlutil writes for the Go ORM too (sqlutil.TablesOf,
// sqlutil.TableStatementsOf). The HTTP runtime's ./postgres entry runs
// them (ormOf), so the two languages run the same SQL. The file imports
// the runtime, so the package depends on it.

// ORMFile is the file of a types package that holds its repositories.
const ORMFile = "orm.ts"

// ORMEntry is the entry of a types package that exports its repositories,
// as an import names it after the package's name.
const ORMEntry = "/orm"

// ORMPGPeerRange is the pg a package with orm.ts peers on, the HTTP
// runtime's ./postgres entry's range, and ORMPGTypesVersion the @types/pg
// it type-checks against: tsrestgen's PGPeerRange and PGTypesVersion,
// which an API package whose Deps holds a pool takes.
const (
	ORMPGPeerRange    = ">=8.11.0"
	ORMPGTypesVersion = "8.23.1"
)

// ORMTypeName is the prefix of the names orm.ts exports for the database
// service: ShopDb for shop-db, whose ORM is ShopDbOrm and transaction
// ShopDbTx. An API's Deps names its orm by it.
func ORMTypeName(service string) string {
	return codegen.ToPascalCase(service)
}

// ORMInfo is what orm.ts writes.
type ORMInfo struct {
	// Name is ORMTypeName of the service, Const the name of the ORM's
	// definition, shopDbOrm.
	Name  string
	Const string

	Tables []ORMTable

	// Views are the projections with a key, each read through a read-only
	// repository (D90).
	Views []ORMTable

	// Clauses are sqlutil.PostgresClauses by name, DefaultLimit the rows a
	// find reads unless told, and HistoryActorCTE and ClearHistoryActor the
	// statements a versioned table's hard delete runs around it.
	Clauses      []sqlutil.NamedStatement
	DefaultLimit int
	// ManyLimit is the rows a one-to-many include reads of a relation
	// (sqlutil.ManyLimit, D90).
	ManyLimit int

	// NearestMetrics are the metrics nearest orders by, and DefaultNearestK
	// the rows it reads unless told, on a database with a vector column
	// (D84); none on another.
	NearestMetrics    []sqlutil.NearestMetric
	DefaultNearestK   int
	HistoryActorCTE   string
	ClearHistoryActor string

	// NearestSettings are the settings of a vector index's scan nearest's
	// options set, and NearestSettingsStatement the statement that sets
	// them, beside NearestMetrics (D89).
	NearestSettings          []sqlutil.NearestSetting
	NearestSettingsStatement string

	// Queries are the database's declared queries (D86), which orm.queries
	// runs; none on a database that declares none.
	Queries []ORMQuery

	// SystemActor is the DB service's system actor when it names one, which
	// asSystem() acts as (D87), and nil for the runtime's default.
	// RequireUpdatedBy is outputs.sql.requireUpdatedBy.
	SystemActor      *ORMSystemActor
	RequireUpdatedBy bool
}

// ORMSystemActor is outputs.sql.systemActor as orm.ts writes it.
type ORMSystemActor struct {
	ID   string
	Name string
}

// ORMTable is a table as orm.ts writes it.
type ORMTable struct {
	// Type is the table type, Member the ORM's member that holds its
	// repository (orderLine), Const its definition (orderLineTable), and
	// Table the table, quoted where it must be.
	Type   string
	Member string
	Const  string
	Table  string
	Doc    string

	// Key is the key's member, KeyType its TypeScript type.
	Key     string
	KeyType string

	Columns []ORMColumn

	// Statements are the statements the runtime runs (sqlutil's, by name).
	Statements []sqlutil.NamedStatement

	// Encrypted is a table with an Encrypted<T> column (D85), whose
	// repository can reencrypt.
	Encrypted bool

	SoftDelete   bool
	HistoryActor bool
	// Search is the search column, qualified, of a table with search
	// fields; empty otherwise.
	Search string

	UniqueKeys   []ORMUniqueKey
	ReferencedBy []ORMReference

	// VectorFields are the members of the table's vector(n) columns, which
	// nearest orders rows by (D84).
	VectorFields []string

	// Relations are the table's to-one relations to the tables of its
	// schema, which a read includes and a filter reaches through, and Many
	// its one-to-many relations, which a read includes (D90, sqlutil's
	// ToOne and ToMany).
	Relations []ORMRelation
	Many      []ORMMany

	// View is a projection's view, read through a read-only repository
	// (D90), whose row is named Type, and Relation the view as a reader
	// names it.
	View     bool
	Relation string
}

// ORMRelation is a to-one relation as orm.ts writes it: Field, the
// member a read includes the target's row under and a filter reaches
// through, Column, the row's member that holds the foreign key, the
// Target type and the ORM's member of its repository, TargetMember, the
// filter's Probe, and Kind, "one", or "optional" for a relation an
// include may find no row of.
type ORMRelation struct {
	Field        string
	Column       string
	Target       string
	TargetMember string
	Probe        string
	Kind         string
	// Nullable is a relation whose foreign key may be null, and
	// TargetSoftDelete one whose target keeps deleted rows, which a filter
	// through it leaves out.
	Nullable         bool
	TargetSoftDelete bool
}

// ORMMany is a one-to-many relation as orm.ts writes it: Name, the member
// a read includes the rows under, the Source type and the ORM's member of
// its repository, and Column, the source row's member that holds the
// foreign key.
type ORMMany struct {
	Name         string
	Source       string
	SourceMember string
	Column       string
}

// ORMColumn is a column of a table as orm.ts writes it.
type ORMColumn struct {
	// Member is the row's member: the field, or <field>Id for a to-one
	// relation's foreign key. Field is the schema field, Column the column
	// quoted where it must be, and Qualified with its table.
	Member    string
	Field     string
	Column    string
	Qualified string

	// What the column holds, for the value codec (./pgcodec's RowField).
	// JSON is a JSON or JSONB column, JSONText a TEXT one that holds JSON;
	// Vector a vector(n) column of Dimensions numbers (D84).
	JSON, JSONText, List, UUID, Timestamp, Date, Int64, Numeric, Point, Interval, Boolean, Vector bool
	Dimensions                                                                                    int

	Key, Auto, Required, Internal bool
	// Encrypted is an Encrypted<T> field's column (D85), which a row holds
	// as a SecretValue and a write takes as a string or a SecretValue.
	Encrypted bool
	Audit     string
	Relation  string
	// Default is the JSON of the schema's default, which a create that
	// leaves the member unset writes; empty without one.
	Default string
	// Filter are the operations of the column's filter (sqlutil.FilterOps);
	// none for a column the filter holds no condition on.
	Filter   []string
	Sortable bool

	// Type is the member's TypeScript type, and FilterType its filter's;
	// empty for a column the filter does not hold. Nullable is a column
	// that may hold null.
	Type       string
	FilterType string
	Nullable   bool

	// Compare is the format an upsert only if changed compares the column's
	// values as, when not the value itself (sqlutil.ColumnModel.CompareAs,
	// D89).
	Compare string
}

// ORMUniqueKey is a unique key an upsert is keyed by: Fields its schema
// fields, Name them joined by ",". Rows reads the rows that hold keys
// (D89), and Expression is a key over an expression, which has none.
type ORMUniqueKey struct {
	Name       string
	Fields     []string
	Conflict   string
	Keep       string
	Rows       string
	Expression bool
}

// Tuple is the key's TypeScript type, a readonly tuple of its fields.
func (k ORMUniqueKey) Tuple() string {
	quoted := make([]string, len(k.Fields))
	for i, f := range k.Fields {
		quoted[i] = jsonQuote(f)
	}
	return "readonly [" + strings.Join(quoted, ", ") + "]"
}

// ORMReference is a to-one relation of another table that points at the
// table: Member is the filter's member, Probe and Live its subquery.
type ORMReference struct {
	Member string
	Source string
	Field  string
	Probe  string
	Live   string
}

// ormStatements are the statements of sqlutil.TableStatements the
// TypeScript runtime runs, by the names its TableStatements declares.
var ormStatements = []string{
	"columns", "qualifiedColumns", "selectColumn", "getOne", "getOneFields", "getMany", "live", "liveWhere", "liveCondition",
	"findOne", "findMany", "count", "page", "pageAfterKey", "pageAfter", "pageOrder", "pageTiebreak", "pageKeyExists",
	"insert", "insertMany", "update", "returning", "updateMany", "updatedAt", "updatedBy", "upsert",
	"softDelete", "softDeleteMany", "delete", "deleteMany",
}

// encryptedStatements are the statements a table with an Encrypted<T>
// column adds (D85), which the runtime's TableStatements declares optional.
var encryptedStatements = []string{"stale", "reseal"}

// ormMembers are the members of an ORM a table's repository may not take
// (the HTTP runtime's ORM_MEMBERS).
var ormMembers = []string{"actingUser", "withUser", "as", "asSystem", "transaction", "query", "exec", "queries"}

// typeScriptORM is what orm.ts writes for schema, or nil for a schema with
// no table. output holds the types the rows' members take theirs from, and
// projections the projections' (D90).
func typeScriptORM(schema *ir.Schema, opts Options, output *ModuleOutput, projections []TypeInfo) (*ORMInfo, error) {
	tables, err := sqlutil.TablesOf(schema, opts.Dependencies)
	if err != nil {
		return nil, err
	}
	if len(tables) == 0 && len(schema.Queries()) == 0 {
		return nil, nil
	}
	types := map[string]*TypeInfo{}
	for i := range output.Types {
		types[output.Types[i].Name] = &output.Types[i]
	}
	known := knownTypeNames(output)
	uuidType := ""
	for _, s := range output.Scalars {
		if s.Name == "Identity.UUID" {
			uuidType = "T." + s.Symbol
		}
	}
	keyTypes := map[string]string{}
	for i := range tables {
		t := &tables[i]
		info := types[t.Type]
		key := t.KeyColumn()
		switch {
		case key.Def == nil && uuidType == "":
			return nil, fmt.Errorf("tsgen: table %s has no key field, and schema %s has no Identity.UUID scalar for the key the generator adds", t.Type, schema.Name)
		case key.Def == nil:
			keyTypes[t.Type] = uuidType
		default:
			keyTypes[t.Type] = qualifyTypes(fieldType(info, key.Field), known)
		}
	}
	clauses := sqlutil.PostgresClauses()
	out := &ORMInfo{
		Name:              ORMTypeName(opts.SchemaName),
		Clauses:           clauses.AllClauses(),
		DefaultLimit:      sqlutil.DefaultLimit,
		ManyLimit:         sqlutil.ManyLimit,
		HistoryActorCTE:   sqlutil.HistoryActorCTE(opts.Naming.HistoryActorSetting),
		ClearHistoryActor: sqlutil.ClearHistoryActor(opts.Naming.HistoryActorSetting),
		RequireUpdatedBy:  opts.Actor.RequireUpdatedBy,
	}
	out.Const = lowerFirst(out.Name) + "Orm"
	if opts.Actor.SystemSet {
		out.SystemActor = &ORMSystemActor{ID: opts.Actor.SystemID, Name: opts.Actor.SystemName}
	}
	for i := range tables {
		t := &tables[i]
		info := types[t.Type]
		member := lowerFirst(t.Type)
		if slices.Contains(ormMembers, member) {
			return nil, fmt.Errorf("tsgen: the repository of table %s would be the ORM's member %s, which the ORM holds itself; rename the table", t.Type, member)
		}
		table := ORMTable{
			Type:         t.Type,
			Member:       member,
			Const:        member + "Table",
			Table:        t.Quoted,
			Key:          t.KeyField,
			KeyType:      keyTypes[t.Type],
			SoftDelete:   t.SoftDelete,
			HistoryActor: t.HistoryActor != "",
		}
		if td := schema.Types[t.Type]; td != nil {
			table.Doc = codegen.DocText(td.Description, td.Comment)
		}
		if t.Search {
			table.Search = sqlutil.Qualify(t.Name, sqlutil.SearchTextColumn)
		}
		statements := map[string]string{}
		for _, st := range sqlutil.TableStatementsOf(t).AllStatements() {
			statements[st.Name] = st.SQL
		}
		for _, name := range ormStatements {
			table.Statements = append(table.Statements, sqlutil.NamedStatement{Name: name, SQL: statements[name]})
		}
		if len(t.EncryptedColumns()) > 0 {
			table.Encrypted = true
			for _, name := range encryptedStatements {
				table.Statements = append(table.Statements, sqlutil.NamedStatement{Name: name, SQL: statements[name]})
			}
		}
		if statements["nearest"] != "" {
			table.Statements = append(table.Statements, sqlutil.NamedStatement{Name: "nearest", SQL: statements["nearest"]})
			out.NearestMetrics, out.DefaultNearestK = sqlutil.NearestMetrics(), sqlutil.DefaultNearestK
			out.NearestSettings, out.NearestSettingsStatement = sqlutil.NearestSettingsOf(), sqlutil.NearestSettings
		}
		for _, c := range t.VectorColumns() {
			table.VectorFields = append(table.VectorFields, c.Field)
		}
		for _, c := range t.Columns {
			column, err := ormColumn(c, info, known, uuidType, keyTypes)
			if err != nil {
				return nil, fmt.Errorf("tsgen: table %s: %w", t.Type, err)
			}
			table.Columns = append(table.Columns, column)
		}
		for _, k := range t.UniqueKeys {
			table.UniqueKeys = append(table.UniqueKeys, ORMUniqueKey{Name: k.Name, Fields: k.Fields, Conflict: k.Conflict, Keep: k.Keep, Rows: k.Rows, Expression: k.Expression})
		}
		if err := ormRelations(&table, t); err != nil {
			return nil, err
		}
		for _, ref := range t.ReferencedBy {
			probe := sqlutil.ReferenceProbeOf(t, ref)
			table.ReferencedBy = append(table.ReferencedBy, ORMReference{
				Member: lowerFirst(ref.Filter),
				Source: ref.Source,
				Field:  ref.Field,
				Probe:  probe.Probe,
				Live:   probe.Live,
			})
		}
		out.Tables = append(out.Tables, table)
	}
	views, err := ormViews(schema, opts, out, projections, known)
	if err != nil {
		return nil, err
	}
	out.Views = views
	if out.Queries, err = typeScriptQueries(schema, opts, types, known); err != nil {
		return nil, err
	}
	return out, nil
}

// ormViews are the projections of schema with a key as orm.ts writes them
// (D90): each its view as sqlutil.ViewsOf models it, its columns typed as
// projections types them, and a read-only repository. A view's row is the
// projection's name, which no table's types may take.
func ormViews(schema *ir.Schema, opts Options, out *ORMInfo, projections []TypeInfo, known map[string]bool) ([]ORMTable, error) {
	models, err := sqlutil.ViewsOf(schema, opts.Dependencies)
	if err != nil {
		return nil, err
	}
	infos := map[string]*TypeInfo{}
	for i := range projections {
		infos[projections[i].Name] = &projections[i]
	}
	taken := map[string]string{}
	for _, t := range out.Tables {
		for _, suffix := range []string{"Row", "Create", "Update", "Filter", "OrderField", "SortField", "UniqueKey", "PageKey", "Relations", "Repository", "VectorField"} {
			taken[t.Type+suffix] = t.Type
		}
		taken[t.Member] = t.Type
	}
	var views []ORMTable
	for i := range models {
		v := &models[i]
		member := lowerFirst(v.Type)
		for _, name := range []string{v.Type, v.Type + "Filter", v.Type + "OrderField", v.Type + "SortField", v.Type + "PageKey", v.Type + "Repository", member} {
			if table, ok := taken[name]; ok {
				return nil, fmt.Errorf("tsgen: the projection %s would declare %s, which the table %s's repository declares; rename the projection", v.Type, name, table)
			}
		}
		if slices.Contains(ormMembers, member) {
			return nil, fmt.Errorf("tsgen: the repository of projection %s would be the ORM's member %s, which the ORM holds itself; rename the projection", v.Type, member)
		}
		info := infos[v.Type]
		view := ORMTable{
			Type:   v.Type,
			Member: member,
			Const:  member + "View",
			Table:  v.Quoted,
			Key:    v.KeyField,
			View:   true,
		}
		if td := schema.Types[v.Type]; td != nil {
			view.Doc = codegen.DocText(td.Description, td.Comment)
			view.Relation = td.Projection.Relation()
		}
		statements := map[string]string{}
		for _, st := range sqlutil.TableStatementsOf(v).AllStatements() {
			statements[st.Name] = st.SQL
		}
		for _, name := range ormStatements {
			view.Statements = append(view.Statements, sqlutil.NamedStatement{Name: name, SQL: statements[name]})
		}
		for _, c := range v.Columns {
			column, err := ormColumn(c, info, known, "", nil)
			if err != nil {
				return nil, fmt.Errorf("tsgen: projection %s: %w", v.Type, err)
			}
			if c.Key {
				view.KeyType = column.Type
			}
			view.Columns = append(view.Columns, column)
		}
		views = append(views, view)
	}
	return views, nil
}

// ormRelations gives table, as orm.ts writes it, the relations t, as
// sqlutil models it, includes and filters through (D90). An include's
// member is a member of the row it loads into, so one that a column
// already holds is refused.
func ormRelations(table *ORMTable, t *sqlutil.TableModel) error {
	members := map[string]bool{}
	for _, c := range table.Columns {
		members[c.Member] = true
	}
	for _, rel := range t.ToOne {
		if members[rel.Field] {
			return fmt.Errorf("tsgen: table %s: the relation %s would include its row as the member %s, which a column holds", t.Type, rel.Field, rel.Field)
		}
		kind := "one"
		if rel.Optional {
			kind = "optional"
		}
		table.Relations = append(table.Relations, ORMRelation{
			Field:            rel.Field,
			Column:           rel.Field + "Id",
			Target:           rel.Target,
			TargetMember:     lowerFirst(rel.Target),
			Probe:            rel.Probe,
			Kind:             kind,
			Nullable:         rel.Nullable,
			TargetSoftDelete: rel.TargetLive != "",
		})
	}
	for _, many := range t.ToMany {
		if members[many.Name] {
			return fmt.Errorf("tsgen: table %s: the relation %s would include its rows as the member %s, which a column holds", t.Type, many.Name, many.Name)
		}
		table.Many = append(table.Many, ORMMany{
			Name:         many.Name,
			Source:       many.Source,
			SourceMember: lowerFirst(many.Source),
			Column:       many.Field + "Id",
		})
	}
	return nil
}

// ormColumn is the column c as orm.ts writes it, its type from the table
// type's info.
func ormColumn(c sqlutil.ColumnModel, info *TypeInfo, known map[string]bool, uuidType string, keyTypes map[string]string) (ORMColumn, error) {
	cl := c.Class
	column := ORMColumn{
		Member:    c.Field,
		Field:     c.Field,
		Column:    c.Quoted,
		Qualified: c.Qualified,
		JSON:      cl.JSON,
		List:      cl.List || (cl.AnyJSON && c.IsArray() && !cl.JSON),
		UUID:      cl.UUIDLike && !cl.JSON,
		Timestamp: cl.DateTimeLike && !cl.JSON,
		Date:      cl.DateLike && !cl.DateTimeLike && !cl.JSON,
		Int64:     cl.IntLike && !cl.JSON,
		Numeric:   strings.HasPrefix(strings.ToUpper(cl.SQLType), "NUMERIC") || strings.HasPrefix(strings.ToUpper(cl.SQLType), "DECIMAL"),
		Point:     cl.Point,
		Interval:  cl.Interval,
		Vector:    cl.Vector,
		Boolean:   cl.BoolLike && !cl.JSON,
		Key:       c.Key,
		Auto:      c.AutoGenerated,
		Required:  c.Required,
		Internal:  c.Internal,
		Encrypted: c.Encrypted,
		Audit:     c.Audit,
		Relation:  c.Relation,
		Filter:    sqlutil.FilterOps(c.Filter),
		Sortable:  c.Sortable,
		Nullable:  !c.Required && !c.Key && !c.AutoGenerated,
	}
	if compare := c.CompareAs(); compare != "%s" {
		column.Compare = compare
	}
	if cl.Vector {
		column.Dimensions = sqlutil.VectorDimensions(c.Type)
	}
	// A list of a JSON scalar is a native JSONB[]: each element JSON.
	if cl.AnyJSON && c.IsArray() && !cl.JSON {
		column.JSON = true
	}
	// A JSON value in a TEXT column is its text, which the codec parses;
	// pg parses a JSON or JSONB column itself. On Postgres every JSON
	// column is JSONB (D83); the flag follows the DDL's type wherever a
	// dialect keeps JSON as TEXT.
	if column.JSON && strings.EqualFold(c.Type, "TEXT") {
		column.JSON, column.JSONText = false, true
	}
	// The codec reads a column one way: an Encrypted<T> envelope (D85), a
	// vector's text, a NUMERIC's canonical text (D84), JSON text or a JSON
	// value. Two would decode it twice, so a column that would take two is
	// refused here, though the loader refuses each such field first.
	if paths := codecPaths(column); len(paths) > 1 {
		return column, fmt.Errorf("column %s would decode as %s, and the codec reads a column one way", c.Name, strings.Join(paths, " and as "))
	}
	switch {
	case c.Relation != "":
		column.Member = c.Field + "Id"
		column.Type = keyTypes[c.Relation]
		if column.Type == "" {
			column.Type = uuidType
		}
	case c.Def == nil && c.Key:
		column.Type = uuidType
	case c.Def == nil:
		column.Type = "number"
	default:
		column.Type = qualifyTypes(fieldType(info, c.Field), known)
		if f := fieldInfo(info, c.Field); f != nil && f.HasDefault && f.DefaultLiteral != "" {
			literal, _, _ := strings.Cut(f.DefaultLiteral, " as ")
			if json.Valid([]byte(literal)) {
				column.Default = literal
			}
		}
	}
	if column.Type == "" {
		return column, fmt.Errorf("column %s has no TypeScript type", c.Name)
	}
	column.FilterType = filterType(c.Filter, column.Type)
	if column.FilterType != "" && column.Nullable {
		column.FilterType += " & NullFilter"
	}
	return column, nil
}

// codecPaths are the codec's own decode paths a column takes (pgcodec's
// RowField): at most one.
func codecPaths(c ORMColumn) []string {
	var paths []string
	for _, p := range []struct {
		on   bool
		name string
	}{{c.Encrypted, "an envelope"}, {c.Vector, "a vector"}, {c.Numeric, "a decimal"}, {c.JSONText, "JSON text"}, {c.JSON, "JSON"}} {
		if p.on {
			paths = append(paths, p.name)
		}
	}
	return paths
}

// filterType is the TypeScript filter of a column of kind whose values are
// of type t, or "" for a column the filter does not hold.
func filterType(kind sqlutil.FilterKind, t string) string {
	switch kind {
	case sqlutil.FilterUUID:
		return "UuidFilter<" + t + ">"
	case sqlutil.FilterString:
		return "StringFilter<" + t + ">"
	case sqlutil.FilterJSON:
		return "JsonFilter<" + t + ">"
	case sqlutil.FilterInt, sqlutil.FilterFloat, sqlutil.FilterDecimal:
		return "NumberFilter<" + t + ">"
	case sqlutil.FilterDateTime:
		return "DateTimeFilter<" + t + ">"
	case sqlutil.FilterDate:
		return "DateFilter<" + t + ">"
	case sqlutil.FilterDuration:
		return "DurationFilter<" + t + ">"
	case sqlutil.FilterBool:
		return "BoolFilter"
	case sqlutil.FilterPoint:
		return "PointFilter"
	}
	return ""
}

// fieldInfo is the field name of the type info, or nil.
func fieldInfo(info *TypeInfo, name string) *FieldInfo {
	if info == nil {
		return nil
	}
	for i := range info.Fields {
		if info.Fields[i].Name == name {
			return &info.Fields[i]
		}
	}
	return nil
}

// fieldType is the TypeScript type types.ts gives the field name of the
// type info, or "".
func fieldType(info *TypeInfo, name string) string {
	if f := fieldInfo(info, name); f != nil {
		return f.TSType
	}
	return ""
}

// knownTypeNames are the names the package's ./types entry exports, which
// orm.ts refers to through its namespace import, T.
func knownTypeNames(output *ModuleOutput) map[string]bool {
	known := map[string]bool{}
	for _, s := range output.Scalars {
		known[s.Symbol] = true
	}
	for _, e := range output.Enums {
		known[e.Name] = true
	}
	for _, u := range output.Unions {
		known[u.Name] = true
	}
	for _, t := range output.Types {
		known[t.Name] = true
	}
	for _, t := range output.ImportedTypes {
		known[t.Name] = true
	}
	return known
}

var typeIdentifier = regexp.MustCompile(`\b[A-Za-z_][A-Za-z0-9_]*\b`)

// qualifyTypes is the type expression t with each name the types entry
// exports prefixed by T., orm.ts's namespace import of it; a property name
// or a builtin stays as it is.
func qualifyTypes(t string, known map[string]bool) string {
	return typeIdentifier.ReplaceAllStringFunc(t, func(name string) string {
		if known[name] {
			return "T." + name
		}
		return name
	})
}

// RuntimeTypes are the types orm.ts imports from the HTTP runtime's
// ./postgres entry, sorted: those its declarations use, each once.
func (o *ORMInfo) RuntimeTypes() []string {
	used := map[string]bool{"Orm": true, "OrmDefinition": true, "OrmOptions": true, "OrmPool": true, "OrmTx": true, "TableDefinition": true}
	if len(o.Queries) > 0 {
		used["QueryDefinition"] = true
	}
	for _, q := range o.Queries {
		if q.Page != nil {
			used["QueryPageOptions"] = true
			used["RowPage"] = true
		}
	}
	for _, t := range append(slices.Clone(o.Tables), o.Views...) {
		if len(t.Relations) > 0 || len(t.Many) > 0 {
			used["Related"] = true
		}
		if t.Encrypted {
			used["EncryptedRepository"] = true
			used["SecretValue"] = true
		}
		switch {
		case t.View:
			used["ReadRepository"] = true
		case t.SoftDelete:
			used["SoftDeleteRepository"] = true
		default:
			used["Repository"] = true
		}
		if len(t.ReferencedBy) > 0 {
			used["ReferencedByFilter"] = true
		}
		if len(t.VectorFields) > 0 {
			used["VectorRepository"] = true
		}
		for _, c := range t.Columns {
			if c.FilterType == "" {
				continue
			}
			// A filter without a type argument, PointFilter, ends where its
			// NullFilter begins.
			name, rest, _ := strings.Cut(c.FilterType, "<")
			name, _, _ = strings.Cut(name, " & ")
			used[name] = true
			if strings.Contains(rest, "NullFilter") || strings.HasSuffix(c.FilterType, "NullFilter") {
				used["NullFilter"] = true
			}
		}
	}
	out := make([]string, 0, len(used))
	for name := range used {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// UsesTypes reports whether a declaration of orm.ts refers to the
// package's ./types entry.
func (o *ORMInfo) UsesTypes() bool {
	if len(o.Queries) > 0 {
		return true
	}
	for _, t := range append(slices.Clone(o.Tables), o.Views...) {
		if strings.Contains(t.KeyType, "T.") {
			return true
		}
		for _, c := range t.Columns {
			if strings.Contains(c.Type, "T.") {
				return true
			}
		}
	}
	return false
}

// RowType is the member's type in a row: an encrypted one's value in a
// SecretValue (D85), any other's its type.
func (c ORMColumn) RowType() string {
	if c.Encrypted {
		return "SecretValue<" + c.Type + ">"
	}
	return c.Type
}

// InputType is the member's type in a create or an update: an encrypted
// one's value, or a SecretValue of it, as a read holds it.
func (c ORMColumn) InputType() string {
	if c.Encrypted {
		return c.Type + " | SecretValue<" + c.Type + ">"
	}
	return c.Type
}

// HasORM reports whether the package writes orm.ts.
func (o *ModuleOutput) HasORM() bool {
	return o.ORM != nil
}

// CreateColumns are the columns a create may set: every column but the
// audit fields, which the repository stamps, and internal metadata.
func (t ORMTable) CreateColumns() []ORMColumn {
	var out []ORMColumn
	for _, c := range t.Columns {
		if c.Internal || c.Audit != "" {
			continue
		}
		out = append(out, c)
	}
	return out
}

// CreateOptional reports whether a create may leave the column unset: the
// database or the schema gives it a value, or it may be null.
func (c ORMColumn) CreateOptional() bool {
	return c.Auto || c.Default != "" || !c.Required || (c.Key && c.UUID)
}

// UpdateColumns are the columns an update may set: every column but the
// key, the audit fields and internal metadata.
func (t ORMTable) UpdateColumns() []ORMColumn {
	var out []ORMColumn
	for _, c := range t.Columns {
		if c.Key || c.Internal || c.Audit != "" {
			continue
		}
		out = append(out, c)
	}
	return out
}

// UpdatedBy returns the updatedBy column, which an update may name itself,
// or nil.
func (t ORMTable) UpdatedBy() *ORMColumn {
	for i := range t.Columns {
		if t.Columns[i].Audit == sqlutil.AuditUpdatedBy {
			return &t.Columns[i]
		}
	}
	return nil
}

// FilterColumns are the columns the filter holds conditions on.
func (t ORMTable) FilterColumns() []ORMColumn {
	var out []ORMColumn
	for _, c := range t.Columns {
		if c.FilterType != "" {
			out = append(out, c)
		}
	}
	return out
}

// OrderFields are the members findOne and findMany order by: every column
// but a relation's and an encrypted one's, whose envelopes have no order
// (D85).
func (t ORMTable) OrderFields() []string {
	var out []string
	for _, c := range t.Columns {
		if c.Relation == "" && !c.Encrypted {
			out = append(out, c.Member)
		}
	}
	return out
}

// SortFields are the members findPage orders by: the key, then each
// sortable column.
func (t ORMTable) SortFields() []string {
	out := []string{t.Key}
	for _, c := range t.Columns {
		if c.Sortable && !c.Key {
			out = append(out, c.Member)
		}
	}
	return out
}

// tsUnion is values as a TypeScript union of string literals.
func tsUnion(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = jsonQuote(v)
	}
	return strings.Join(quoted, " | ")
}

// Flags are the codec's and the repository's flags of the column, as
// orm.ts writes them: ", json: true" and the like.
func (c ORMColumn) Flags() string {
	var b strings.Builder
	flag := func(on bool, name string) {
		if on {
			b.WriteString(", " + name + ": true")
		}
	}
	flag(c.JSON, "json")
	flag(c.JSONText, "jsonText")
	flag(c.List, "list")
	flag(c.UUID, "uuid")
	flag(c.Timestamp, "timestamp")
	flag(c.Date, "date")
	flag(c.Int64, "int64")
	flag(c.Numeric, "numeric")
	flag(c.Point, "point")
	flag(c.Interval, "interval")
	flag(c.Vector, "vector")
	flag(c.Boolean, "boolean")
	flag(c.Key, "key")
	flag(c.Auto, "auto")
	flag(c.Required, "required")
	flag(c.Internal, "internal")
	flag(c.Encrypted, "encrypted")
	if c.Audit != "" {
		b.WriteString(", audit: " + jsonQuote(c.Audit))
	}
	if c.Relation != "" {
		b.WriteString(", relation: " + jsonQuote(c.Relation))
	}
	if c.Default != "" {
		b.WriteString(", default: " + c.Default)
	}
	if len(c.Filter) > 0 {
		quoted := make([]string, len(c.Filter))
		for i, op := range c.Filter {
			quoted[i] = jsonQuote(op)
		}
		b.WriteString(", filter: [" + strings.Join(quoted, ", ") + "]")
	}
	flag(c.Sortable, "sortable")
	if c.Dimensions > 0 {
		fmt.Fprintf(&b, ", dimensions: %d", c.Dimensions)
	}
	if c.Compare != "" {
		b.WriteString(", compare: " + jsonQuote(c.Compare))
	}
	return b.String()
}

// Definition is the key as orm.ts writes it in its table's uniqueKeys:
// its conflict target, the assignment of an update that sets nothing, and
// the read of the rows that hold keys, or that it is a key over an
// expression (D89).
func (k ORMUniqueKey) Definition() string {
	out := "{ conflict: " + jsonQuote(k.Conflict) + ", keep: " + jsonQuote(k.Keep)
	if k.Rows != "" {
		out += ", rows: " + jsonQuote(k.Rows)
	}
	if k.Expression {
		out += ", expression: true"
	}
	return out + " }"
}

// NearestSettingDefinitions are the settings of a vector index's scan as
// orm.ts writes them in the ORM's nearest settings (D89): each one's
// option, its call, and the values it takes or the range of an integer.
func (o *ORMInfo) NearestSettingDefinitions() []string {
	out := make([]string, len(o.NearestSettings))
	for i, setting := range o.NearestSettings {
		def := "{ option: " + jsonQuote(setting.Option) + ", sql: " + jsonQuote(setting.SQL)
		if len(setting.Values) > 0 {
			quoted := make([]string, len(setting.Values))
			for j, v := range setting.Values {
				quoted[j] = jsonQuote(v)
			}
			def += ", values: [" + strings.Join(quoted, ", ") + "]"
		} else {
			def += fmt.Sprintf(", min: %d, max: %d", setting.Min, setting.Max)
		}
		out[i] = def + " }"
	}
	return out
}
