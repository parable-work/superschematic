package rustgen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/behaviorlayout"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/rustutil"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// The repositories of a DB schema in Rust (D79, D116). The types crate
// carries each table as data, in src/orm.rs, as the TypeScript types carry
// it in orm.ts: the database's definition as JSON, once for each dialect
// the DB service lists, with each table's columns, what each holds, and its
// statements, which sqlutil writes for the Go ORM and the TypeScript
// repositories too. The HTTP runtime's orm module parses it
// (Definition::parse) and fills and runs the statements in the order the Go
// ORM fills them. Beside the data, orm.rs types each table: its row, what a
// create and an update set, and its filter, over which the runtime's typed
// repository reads and writes.
//
// The module, and the crate's dependency on the HTTP runtime, are behind
// the crate's postgres and sqlite features, so a crate built with neither
// is the types alone, as D65 has it carry a queue's descriptor.

// ORMFile is the file of a types crate that holds its repositories.
const ORMFile = "orm.rs"

// ORMInfo is what orm.rs writes.
type ORMInfo struct {
	// Tables are the tables with a repository, in the schema's order.
	Tables []ORMTable

	// Postgres and SQLite are the database's definition in each dialect as
	// the Rust raw string literal a constant holds. SQLite is empty for a
	// DB service that lists no sqlite dialect.
	Postgres string
	SQLite   string

	// SQLiteRefused are the tables with no repository on SQLite, each with
	// why (sqlutil.SQLiteTableRefusal).
	SQLiteRefused []ORMRefusal

	// Rules is whether a table composes behaviors (D103), whose writes go
	// through the core, RulesTables those tables, and RulesLayout the
	// layout document of them (behaviorlayout), as the Rust raw string
	// literal a constant holds: where the core's names live, which the
	// behaviors' adapter reads.
	Rules       bool
	RulesTables []string
	RulesLayout string
}

// ORMRefusal is a table with no repository in a dialect, and why.
type ORMRefusal struct {
	Type   string
	Reason string
}

// ORMTable is a table as orm.rs types it.
type ORMTable struct {
	// Type is the table type, Fn the function that opens its repository
	// (order_line), and KeyType the Rust type of its key.
	Type    string
	Fn      string
	KeyType string
	Doc     string

	// Row, Create, Update and Filter are the members of each of the
	// table's types.
	Row    []ORMMember
	Create []ORMMember
	Update []ORMMember
	Filter []ORMMember

	// Behaviors is whether the table composes behaviors (D103).
	Behaviors bool
}

// ORMMember is a member of one of a table's types.
type ORMMember struct {
	// Name is the Rust field, Rename the member as JSON holds it when it
	// is not Name, and Type the field's type.
	Name   string
	Rename string
	Type   string
	Doc    string

	// Optional is a member a value may leave out, which is then not
	// serialized, and Default one a row may leave out, which then reads as
	// its type's default.
	Optional bool
	Default  bool

	// EmptyList is a list member that is not serialized when empty.
	EmptyList bool
}

// ormDefinition is a database's definition in one dialect, the HTTP
// runtime's orm::Definition: the members of the TypeScript OrmDefinition
// the Rust repositories read.
type ormDefinition struct {
	Dialect          string         `json:"dialect"`
	Clauses          orderedStrings `json:"clauses"`
	DefaultLimit     int            `json:"defaultLimit"`
	SystemActor      *ormActor      `json:"systemActor,omitempty"`
	RequireUpdatedBy bool           `json:"requireUpdatedBy,omitempty"`
	// HistoryActorCTE and ClearHistoryActor are the statements a versioned
	// table's hard delete runs around it, on Postgres (D17, D32).
	HistoryActorCTE   string     `json:"historyActorCTE,omitempty"`
	ClearHistoryActor string     `json:"clearHistoryActor,omitempty"`
	Tables            []ormTable `json:"tables"`
}

type ormActor struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ormTable struct {
	Name         string                  `json:"name"`
	Table        string                  `json:"table"`
	Key          string                  `json:"key"`
	Columns      []ormColumn             `json:"columns"`
	Statements   orderedStrings          `json:"statements"`
	SoftDelete   bool                    `json:"softDelete,omitempty"`
	HistoryActor bool                    `json:"historyActor,omitempty"`
	Fences       []ormFence              `json:"fences,omitempty"`
	Search       string                  `json:"search,omitempty"`
	UniqueKeys   map[string]ormUniqueKey `json:"uniqueKeys,omitempty"`
	ReferencedBy []ormReference          `json:"referencedBy,omitempty"`
	Relations    []ormRelation           `json:"relations,omitempty"`
	Unsupported  string                  `json:"unsupported,omitempty"`
	Behaviors    *ormBehaviors           `json:"behaviors,omitempty"`
}

// ormFence is a fence of a table, a @fenced field (D118), the runtime's
// orm::Fence: sqlutil's FenceModel, whose bump an update, an update_many
// and an upsert's update add to their assignments.
type ormFence struct {
	Column string   `json:"column"`
	Covers []string `json:"covers,omitempty"`
	Bump   string   `json:"bump"`
}

type ormBehaviors struct {
	Owned    []string `json:"owned"`
	NoUpsert bool     `json:"noUpsert,omitempty"`
}

type ormColumn struct {
	Name      string          `json:"name"`
	Column    string          `json:"column"`
	Field     string          `json:"field"`
	Qualified string          `json:"qualified"`
	JSON      bool            `json:"json,omitempty"`
	List      bool            `json:"list,omitempty"`
	UUID      bool            `json:"uuid,omitempty"`
	Timestamp bool            `json:"timestamp,omitempty"`
	Date      bool            `json:"date,omitempty"`
	Numeric   bool            `json:"numeric,omitempty"`
	Point     bool            `json:"point,omitempty"`
	Interval  bool            `json:"interval,omitempty"`
	Boolean   bool            `json:"boolean,omitempty"`
	Key       bool            `json:"key,omitempty"`
	Auto      bool            `json:"auto,omitempty"`
	Required  bool            `json:"required,omitempty"`
	Internal  bool            `json:"internal,omitempty"`
	Encrypted bool            `json:"encrypted,omitempty"`
	Audit     string          `json:"audit,omitempty"`
	Relation  string          `json:"relation,omitempty"`
	Default   json.RawMessage `json:"default,omitempty"`
	Filter    []string        `json:"filter,omitempty"`
	Sortable  bool            `json:"sortable,omitempty"`
}

type ormUniqueKey struct {
	Conflict   string `json:"conflict"`
	Keep       string `json:"keep"`
	Rows       string `json:"rows,omitempty"`
	Held       string `json:"held,omitempty"`
	Expression bool   `json:"expression,omitempty"`
}

type ormReference struct {
	Filter string `json:"filter"`
	Probe  string `json:"probe"`
	Live   string `json:"live,omitempty"`
}

type ormRelation struct {
	Field  string `json:"field"`
	Target string `json:"target"`
	Probe  string `json:"probe"`
}

// orderedStrings is a JSON object of strings whose members keep their
// order: a table's statements and the clauses, in the order sqlutil names
// them, so a definition reads as its Go source does.
type orderedStrings []sqlutil.NamedStatement

func (o orderedStrings) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, member := range o {
		if i > 0 {
			buf.WriteByte(',')
		}
		for _, text := range []string{member.Name, member.SQL} {
			var quoted bytes.Buffer
			enc := json.NewEncoder(&quoted)
			enc.SetEscapeHTML(false)
			if err := enc.Encode(text); err != nil {
				return nil, err
			}
			buf.Write(bytes.TrimSuffix(quoted.Bytes(), []byte("\n")))
			if text == member.Name {
				buf.WriteByte(':')
			}
		}
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// ormStatements are the statements of sqlutil.TableStatements the Rust
// runtime runs, by the names its orm::Statements declares.
var ormStatements = []string{
	"columns", "qualifiedColumns", "getOne", "live", "liveWhere", "liveCondition",
	"findOne", "findMany", "count", "page", "pageAfterKey", "pageAfter", "pageAfterNulls",
	"pageOrder", "pageOrderNulls", "pageTiebreak", "pageKeyExists",
	"insert", "insertMany", "update", "versionFence", "returning", "updateMany", "updatedAt", "updatedBy", "upsert",
	"softDelete", "softDeleteIfVersion", "delete", "deleteIfVersion", "version",
}

// ormClauses are the clauses the Rust runtime writes, by the names its
// orm::Clauses declares.
var ormClauses = []string{
	"eq", "ne", "gt", "gte", "lt", "lte", "ilike", "in", "noneOf", "isNull", "isNotNull", "search",
	"exists", "notExists", "set", "setNull", "pageKey", "placeholder", "default", "or", "group",
	"where", "and", "orderBy", "orderTerm", "limit", "offset", "changed", "never", "null",
}

// named are the members of all that names lists, in its order.
func named(all []sqlutil.NamedStatement, names []string) orderedStrings {
	byName := make(map[string]string, len(all))
	for _, st := range all {
		byName[st.Name] = st.SQL
	}
	out := make(orderedStrings, 0, len(names))
	for _, name := range names {
		out = append(out, sqlutil.NamedStatement{Name: name, SQL: byName[name]})
	}
	return out
}

// rustORM reads the repositories of schema, or nil for a schema with no
// table. sqlite is whether the DB service lists the sqlite dialect, whose
// definition orm.rs then carries beside Postgres's.
func rustORM(schema *ir.Schema, opts Options, output *ModuleOutput) (*ORMInfo, error) {
	tables, err := sqlutil.TablesOf(schema, opts.Dependencies)
	if err != nil {
		return nil, err
	}
	layout, err := behaviorlayout.Of(schema, opts.Registry)
	if err != nil {
		return nil, err
	}
	types := map[string]*TypeInfo{}
	for i := range output.Types {
		types[output.Types[i].Name] = &output.Types[i]
	}
	uuidType := ""
	for _, s := range output.Scalars {
		if s.CanonicalName == "Identity.UUID" {
			uuidType = s.Name
		}
	}
	// A behavior's sub-table has no repository (D103): only the behavior's
	// operations write it.
	owned := map[string]bool{}
	for _, t := range tables {
		if td := schema.Types[t.Type]; td != nil && td.BehaviorTable != nil {
			owned[t.Type] = true
		}
	}
	keyTypes := map[string]string{}
	for i := range tables {
		t := &tables[i]
		key := t.KeyColumn()
		switch {
		case key.Def == nil && uuidType == "":
			// The key the generator adds is a UUID, which a schema that
			// names no Identity.UUID has no type of: its repository takes
			// the key as the text a row carries it in.
			keyTypes[t.Type] = "String"
		case key.Def == nil:
			keyTypes[t.Type] = uuidType
		default:
			keyTypes[t.Type] = baseType(fieldInfoOf(types[t.Type], key.Field))
		}
	}
	info := &ORMInfo{}
	postgres := ormDefinition{Dialect: "postgres", Clauses: named(sqlutil.PostgresClauses().AllClauses(), ormClauses), DefaultLimit: sqlutil.DefaultLimit, RequireUpdatedBy: opts.Actor.RequireUpdatedBy}
	sqlite := ormDefinition{Dialect: "sqlite", Clauses: named(sqlutil.SQLiteClauses().AllClauses(), ormClauses), DefaultLimit: sqlutil.DefaultLimit, RequireUpdatedBy: opts.Actor.RequireUpdatedBy}
	if opts.Actor.SystemSet {
		actor := &ormActor{ID: opts.Actor.SystemID, Name: opts.Actor.SystemName}
		postgres.SystemActor, sqlite.SystemActor = actor, actor
	}
	for i := range tables {
		t := &tables[i]
		if owned[t.Type] {
			continue
		}
		typed := ORMTable{
			Type:    t.Type,
			Fn:      codegen.ToSnakeCase(t.Type),
			KeyType: keyTypes[t.Type],
		}
		if td := schema.Types[t.Type]; td != nil {
			typed.Doc = codegen.DocText(td.Description, td.Comment)
		}
		table := ormTable{
			Name:       t.Type,
			Table:      t.Quoted,
			Key:        t.KeyField,
			SoftDelete: t.SoftDelete,
			UniqueKeys: map[string]ormUniqueKey{},
		}
		if t.Search {
			table.Search = t.SearchColumn
		}
		for _, fence := range t.Fences {
			table.Fences = append(table.Fences, ormFence{Column: fence.Column, Covers: fence.Covers, Bump: fence.Bump})
		}
		var behaviors *ormBehaviors
		if layout != nil && layout.Types[t.Type] != nil {
			l := layout.Types[t.Type]
			behaviors = &ormBehaviors{Owned: []string{}, NoUpsert: l.Deadlines != nil}
			for _, use := range l.Uses {
				if len(use.Tables) > 0 {
					behaviors.NoUpsert = true
				}
			}
			typed.Behaviors, info.Rules = true, true
			info.RulesTables = append(info.RulesTables, t.Type)
		}
		table.Behaviors = behaviors
		for _, c := range t.Columns {
			column, member, err := rustORMColumn(c, types[t.Type], uuidType, keyTypes)
			if err != nil {
				return nil, fmt.Errorf("rustgen: table %s: %w", t.Type, err)
			}
			table.Columns = append(table.Columns, column)
			if behaviors != nil && c.Def.Owned() {
				behaviors.Owned = append(behaviors.Owned, column.Name)
			}
			typed.add(c, column, member)
		}
		for _, rel := range t.ToOne {
			if owned[rel.Target] {
				continue
			}
			table.Relations = append(table.Relations, ormRelation{Field: rel.Field, Target: rel.Target, Probe: rel.Probe})
			typed.Filter = append(typed.Filter, ORMMember{
				Name:     toRustFieldName(rel.Field),
				Rename:   renameOf(rel.Field),
				Type:     "Box<" + rel.Target + "Filter>",
				Doc:      fmt.Sprintf("The rows whose %s is among the %s rows the filter finds (D90).", rel.Field, rel.Target),
				Optional: true,
			})
		}
		for _, ref := range t.ReferencedBy {
			if owned[ref.Source] {
				continue
			}
			probe := sqlutil.ReferenceProbeOf(t, ref)
			member := strings.ToLower(ref.Filter[:1]) + ref.Filter[1:]
			table.ReferencedBy = append(table.ReferencedBy, ormReference{Filter: member, Probe: probe.Probe, Live: probe.Live})
			typed.Filter = append(typed.Filter, ORMMember{
				Name:     toRustFieldName(member),
				Rename:   renameOf(member),
				Type:     "orm::ReferencedBy",
				Doc:      fmt.Sprintf("The rows some %s row's %s points at, or those none does.", ref.Source, ref.Field),
				Optional: true,
			})
		}
		if t.Search {
			typed.Filter = append(typed.Filter, ORMMember{Name: "search_text", Rename: "searchText", Type: "String", Doc: "The rows whose search fields hold each word of the text, in any case (D62).", Optional: true})
		}
		typed.Filter = append(typed.Filter,
			ORMMember{Name: "all_of", Rename: "allOf", Type: "Vec<" + t.Type + "Filter>", Doc: "The rows every one of the filters finds, beside the other members (D99).", EmptyList: true},
			ORMMember{Name: "any_of", Rename: "anyOf", Type: "Vec<" + t.Type + "Filter>", Doc: "The rows any of the filters finds, beside the other members (D90); a filter of no member keeps every row.", EmptyList: true},
		)
		// The key's members, for an update that names who made it.
		if c := t.AuditColumn(sqlutil.AuditUpdatedBy); c != nil {
			typed.Update = append(typed.Update, ORMMember{Name: toRustFieldName(c.Field), Rename: renameOf(c.Field), Type: uuidType, Doc: "Who made the update, the acting user unless set.", Optional: true})
		}

		onPostgres, onSQLite := table, table
		if t.HistoryActor != "" {
			onPostgres.HistoryActor = true
			postgres.HistoryActorCTE = sqlutil.HistoryActorCTE(opts.Naming.HistoryActorSetting)
			postgres.ClearHistoryActor = sqlutil.ClearHistoryActor(opts.Naming.HistoryActorSetting)
		}
		onPostgres.Statements = named(sqlutil.TableStatementsOf(t).AllStatements(), ormStatements)
		for _, k := range t.UniqueKeys {
			onPostgres.UniqueKeys[k.Name] = ormUniqueKey{Conflict: k.Conflict, Keep: k.Keep, Rows: k.Rows, Held: k.Held, Expression: k.Expression}
		}
		postgres.Tables = append(postgres.Tables, onPostgres)
		if opts.SQLite {
			onSQLite.UniqueKeys = map[string]ormUniqueKey{}
			if reason := sqlutil.SQLiteTableRefusal(t); reason != "" {
				onSQLite.Unsupported = reason
				onSQLite.Statements = orderedStrings{}
				info.SQLiteRefused = append(info.SQLiteRefused, ORMRefusal{Type: t.Type, Reason: reason})
			} else {
				onSQLite.Statements = named(sqlutil.SQLiteTableStatementsOf(t).AllStatements(), ormStatements)
				for _, k := range t.UniqueKeys {
					k = k.SQLite()
					onSQLite.UniqueKeys[k.Name] = ormUniqueKey{Conflict: k.Conflict, Keep: k.Keep, Rows: k.Rows, Held: k.Held, Expression: k.Expression}
				}
			}
			sqlite.Tables = append(sqlite.Tables, onSQLite)
		}
		info.Tables = append(info.Tables, typed)
	}
	if len(info.Tables) == 0 {
		return nil, nil
	}
	if info.Rules {
		encoded, err := json.MarshalIndent(layout, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("rustgen: the behaviors' layout of %s: %w", schema.Name, err)
		}
		info.RulesLayout = rustutil.RawString(string(encoded))
	}
	if info.Postgres, err = definitionLiteral(postgres); err != nil {
		return nil, err
	}
	if opts.SQLite {
		if info.SQLite, err = definitionLiteral(sqlite); err != nil {
			return nil, err
		}
	}
	return info, nil
}

// definitionLiteral is a definition as the Rust raw string literal a
// constant of orm.rs holds: indented, with no HTML escapes, since its
// statements compare with `<` and `>`.
func definitionLiteral(d ormDefinition) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(d); err != nil {
		return "", fmt.Errorf("rustgen: encode the ORM's definition on %s: %w", d.Dialect, err)
	}
	return rustutil.RawString(strings.TrimSuffix(buf.String(), "\n")), nil
}

// fieldInfoOf is the field of info named name, or nil.
func fieldInfoOf(info *TypeInfo, name string) *FieldInfo {
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

// baseType is the Rust type of a field's value: its type without the
// Option a field that may be absent wraps it in.
func baseType(f *FieldInfo) string {
	if f == nil {
		return ""
	}
	if !f.Required {
		if inner, ok := strings.CutPrefix(f.RustType, "Option<"); ok {
			return strings.TrimSuffix(inner, ">")
		}
	}
	return f.RustType
}

// renameOf is the serde rename of the member name, or "" when its Rust
// field is the member already.
func renameOf(member string) string {
	if strings.TrimPrefix(toRustFieldName(member), "r#") == member {
		return ""
	}
	return member
}

// rustORMColumn is the column c as the definition describes it, and the
// member of its row's type: its Rust field, its JSON member and its type.
func rustORMColumn(c sqlutil.ColumnModel, info *TypeInfo, uuidType string, keyTypes map[string]string) (ormColumn, ORMMember, error) {
	cl := c.Class
	column := ormColumn{
		Name:      c.Field,
		Column:    c.Quoted,
		Field:     c.Field,
		Qualified: c.Qualified,
		JSON:      cl.JSON,
		List:      cl.List || (cl.AnyJSON && c.IsArray() && !cl.JSON),
		UUID:      cl.UUIDLike && !cl.JSON,
		Timestamp: cl.DateTimeLike && !cl.JSON,
		Date:      cl.DateLike && !cl.DateTimeLike && !cl.JSON,
		Numeric:   strings.HasPrefix(strings.ToUpper(cl.SQLType), "NUMERIC") || strings.HasPrefix(strings.ToUpper(cl.SQLType), "DECIMAL"),
		Point:     cl.Point,
		Interval:  cl.Interval,
		Boolean:   cl.BoolLike && !cl.JSON,
		Key:       c.Key,
		// A field with a schema default is one the database fills too: its
		// column carries the default, which an insert that leaves the
		// member unset takes, as the Go ORM's does.
		Auto:      c.AutoGenerated || c.DefaultsToNow() || c.Default != nil,
		Required:  c.Required,
		Internal:  c.Internal,
		Encrypted: c.Encrypted,
		Audit:     c.Audit,
		Relation:  c.Relation,
		Filter:    sqlutil.FilterOps(c.Filter),
		Sortable:  c.Sortable,
	}
	// A list of a JSON scalar is a list each element of which is JSON.
	if cl.AnyJSON && c.IsArray() && !cl.JSON {
		column.JSON = true
	}
	// An unset enum's default is written by the insert, as the Go ORM
	// writes it; any other default is the column's own.
	if c.EnumDefault != "" {
		text, err := json.Marshal(c.EnumDefault)
		if err != nil {
			return column, ORMMember{}, err
		}
		column.Default = text
	}
	member := ORMMember{Doc: ""}
	switch {
	case c.Relation != "":
		column.Name = c.Field + "Id"
		member.Type = keyTypes[c.Relation]
		if member.Type == "" {
			member.Type = uuidType
		}
		if member.Type == "" {
			member.Type = "String"
		}
	case c.Def == nil && c.Key:
		// The key the generator adds: the schema's UUID, or its text in
		// a schema that names none.
		member.Type = uuidType
		if member.Type == "" {
			member.Type = "String"
		}
	case c.Def == nil:
		member.Type = "i64"
	default:
		f := fieldInfoOf(info, c.Field)
		member.Type = baseType(f)
		if f != nil {
			member.Doc = f.Doc
		}
	}
	if member.Type == "" {
		return column, member, fmt.Errorf("column %s has no Rust type", c.Name)
	}
	member.Name = toRustFieldName(column.Name)
	member.Rename = renameOf(column.Name)
	return column, member, nil
}

// add gives the table's types the member of column c.
func (t *ORMTable) add(c sqlutil.ColumnModel, column ormColumn, member ORMMember) {
	nullable := !c.Required && !c.Key && !c.AutoGenerated
	row := member
	if nullable {
		row.Type = "Option<" + member.Type + ">"
		row.Default = true
	}
	// _version reads as 0 from a row that holds none.
	if c.Internal {
		row.Default = true
	}
	t.Row = append(t.Row, row)

	owned := c.Def.Owned()
	insertable := !c.Internal && c.Audit == "" && !owned
	if insertable {
		create := member
		create.Optional = !c.Required || column.Auto || c.Key || len(column.Default) > 0
		t.Create = append(t.Create, create)
	}
	if !c.Key && c.Audit == "" && !c.Internal && !owned {
		update := member
		update.Optional = true
		if nullable {
			update.Type = "Option<" + member.Type + ">"
			update.Doc = strings.TrimSpace(member.Doc + " `Some(None)` sets the column NULL.")
		}
		t.Update = append(t.Update, update)
	}
	if filter := rustFilterType(c.Filter, member.Type); filter != "" {
		t.Filter = append(t.Filter, ORMMember{Name: member.Name, Rename: member.Rename, Type: filter, Optional: true})
	}
}

// rustFilterType is the runtime's filter of a column of kind whose values
// are of type t, or "" for a column the filter does not hold.
func rustFilterType(kind sqlutil.FilterKind, t string) string {
	switch kind {
	case sqlutil.FilterUUID, sqlutil.FilterJSON, sqlutil.FilterBool, sqlutil.FilterPoint:
		return "orm::Equals<" + t + ">"
	case sqlutil.FilterString:
		return "orm::Text<" + t + ">"
	case sqlutil.FilterInt, sqlutil.FilterFloat, sqlutil.FilterDecimal, sqlutil.FilterDateTime, sqlutil.FilterDate, sqlutil.FilterDuration:
		return "orm::Ordered<" + t + ">"
	}
	return ""
}
