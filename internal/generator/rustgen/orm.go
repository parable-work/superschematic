package rustgen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/behaviorlayout"
	"github.com/parable-work/superschematic/internal/generator/behaviortypes"
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

	// OperationStructs and OperationEnums are the types the operations of
	// the tables' behaviors name (behaviortypes.Operations): each
	// operation's parameters and result and what they hold, which orm.rs
	// writes beside the tables' own.
	OperationStructs []behaviortypes.Struct
	OperationEnums   []behaviortypes.Enum

	// References and Attachments are whether the database keeps references
	// (a @references table, or a table whose Comments ref blocks reach
	// rows) and files (an @attachments table), which the Postgres
	// repositories read through references() and attachments() (D154,
	// D185).
	References  bool
	Attachments bool
}

// ORMUse is a use of a behavior on a table that has operations (D103), as
// orm.rs types them: a struct of a method per operation, which each
// dialect's repositories open, as the Go ORM's db.Order.Status() and the
// TypeScript repositories' orm.order.status are.
type ORMUse struct {
	// Struct is the operations' type (OrderStatusOperations), Fn the
	// repositories' method that opens it (order_status), Member the member
	// the use's operations sit under (status), and Doc its doc.
	Struct string
	Fn     string
	Member string
	Doc    string

	Operations []ORMOperation
}

// ORMOperation is one operation of a use as orm.rs types it.
type ORMOperation struct {
	// Name is the operation as the behavior declares it (fireTimer), Fn
	// its method (fire_timer) and Doc the method's doc.
	Name string
	Fn   string
	Doc  string
	// Params and Result are the Rust types of its parameters and its
	// result.
	Params string
	Result string
	// Table is an operation of the table as a whole (D104), whose method
	// takes no key and no preconditions.
	Table bool
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

	// Behaviors is whether the table composes behaviors (D103), and Uses
	// its uses that have operations, each typed.
	Behaviors bool
	Uses      []ORMUse

	// ReadOnly is a table the repositories only read (D139): one that holds
	// a graph member's released rows (ir.TypeDef.ReleasedOf), which the
	// graph's release owns. Its repository has the reads and no write, and
	// orm.rs writes no Create and no Update of it. ReleasedOf is that
	// member, which the repository's doc names.
	ReadOnly   bool
	ReleasedOf string
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
	Dialect string         `json:"dialect"`
	Clauses orderedStrings `json:"clauses"`
	// JSONClauses are the conditions of a Generic.JSON column's filter
	// where they are not Clauses' (sqlutil.SQLiteJSONClauses); absent on
	// Postgres, whose JSONB column compares its values itself.
	JSONClauses  orderedStrings `json:"jsonClauses,omitempty"`
	DefaultLimit int            `json:"defaultLimit"`
	// MaxParameters is the most parameters one statement binds in the
	// dialect, by which an upsert of many rows batches them (D89):
	// sqlutil.MaxParameters on Postgres, SQLiteMaxParameters on SQLite.
	MaxParameters    int       `json:"maxParameters"`
	SystemActor      *ormActor `json:"systemActor,omitempty"`
	RequireUpdatedBy bool      `json:"requireUpdatedBy,omitempty"`
	// HistoryActorCTE and ClearHistoryActor are the statements a versioned
	// table's hard delete runs around it, on Postgres (D17, D32).
	HistoryActorCTE   string     `json:"historyActorCTE,omitempty"`
	ClearHistoryActor string     `json:"clearHistoryActor,omitempty"`
	Tables            []ormTable `json:"tables"`
	// References and Attachments are the database's references and files
	// (D154), on Postgres alone.
	References  *rustRowReferences `json:"references,omitempty"`
	Attachments *rustAttachments   `json:"attachments,omitempty"`
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
	// Many are the table's one-to-many relations, which a filter reaches
	// through, as a row rule's path and a grant's do (D156).
	Many []ormMany `json:"many,omitempty"`
	// Audience is an @audience table's resolved columns, and Access what
	// the table is to per-record access: grants, log or roles (D156).
	Audience *ormAudience `json:"audience,omitempty"`
	Access   string       `json:"access,omitempty"`
}

// ormFence is a fence of a table, a @fenced field (D119), the runtime's
// orm::Fence: sqlutil's FenceModel, whose bump an update, an update_many
// and an upsert's update add to their assignments.
// ormMany is a one-to-many relation as the runtime's orm::Many reads it:
// the relation's member, the other table's type and its member that holds
// the foreign key (D156).
type ormMany struct {
	Name   string `json:"name"`
	Target string `json:"target"`
	Column string `json:"column"`
}

// ormAudience is an @audience table's resolved columns as the runtime's
// orm::Audience reads them (D156).
type ormAudience struct {
	Effective string `json:"effective"`
	Agents    string `json:"agents"`
	Probe     string `json:"probe,omitempty"`
}

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

// ormEncryptedStatements are the statements of a table with an
// Encrypted<T> column the Rust runtime runs too (D85): its reencrypt's read
// of the rows another key sealed and their reseal, which a table without
// one has none of, and its definition leaves out.
var ormEncryptedStatements = []string{"stale", "reseal"}

// tableStatements are a table's statements the Rust runtime runs, by the
// names its orm::Statements declares: ormStatements, then each of
// ormEncryptedStatements the table has.
func tableStatements(all []sqlutil.NamedStatement) orderedStrings {
	out := named(all, ormStatements)
	for _, st := range named(all, ormEncryptedStatements) {
		if st.SQL != "" {
			out = append(out, st)
		}
	}
	return out
}

// ormClauses are the clauses the Rust runtime writes, by the names its
// orm::Clauses declares.
var ormClauses = []string{
	"eq", "ne", "gt", "gte", "lt", "lte", "ilike", "in", "noneOf", "isNull", "isNotNull", "search",
	"exists", "notExists", "set", "setNull", "pageKey", "placeholder", "default", "or", "group",
	"where", "and", "orderBy", "orderTerm", "limit", "offset", "changed", "never", "null",
	"setExcluded", "excluded",
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
	operations := map[string]behaviortypes.TableOperations{}
	if layout != nil {
		all, err := behaviortypes.Operations(schema, opts.Registry)
		if err != nil {
			return nil, fmt.Errorf("rustgen: the operations of %s's behaviors: %w", schema.Name, err)
		}
		for _, table := range all {
			operations[table.Type] = table
		}
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
		if td := schema.Types[t.Type]; td != nil && (td.BehaviorTable != nil || td.RowTable != nil || td.SearchIndex) {
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
	postgres := ormDefinition{Dialect: "postgres", Clauses: named(sqlutil.PostgresClauses().AllClauses(), ormClauses), DefaultLimit: sqlutil.DefaultLimit, MaxParameters: sqlutil.MaxParameters, RequireUpdatedBy: opts.Actor.RequireUpdatedBy}
	sqlite := ormDefinition{Dialect: "sqlite", Clauses: named(sqlutil.SQLiteClauses().AllClauses(), ormClauses), JSONClauses: sqlutil.SQLiteJSONClauses().AllJSONClauses(), DefaultLimit: sqlutil.DefaultLimit, MaxParameters: sqlutil.SQLiteMaxParameters, RequireUpdatedBy: opts.Actor.RequireUpdatedBy}
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
			typed.ReadOnly, typed.ReleasedOf = td.ReleasedOf != "", td.ReleasedOf
		}
		table := ormTable{
			Name:       t.Type,
			Table:      t.Quoted,
			Key:        t.KeyField,
			SoftDelete: t.SoftDelete,
			UniqueKeys: map[string]ormUniqueKey{},
			Access:     t.Access,
		}
		if a := t.Audience; a != nil {
			table.Audience = &ormAudience{Effective: a.Effective, Agents: a.Agents, Probe: a.Probe}
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
				// A create that writes a sub-table's or a shared table's
				// rows (Numbering's counts, D103's step 4) is no upsert's
				// insert.
				if len(use.Tables) > 0 || len(use.Shared) > 0 {
					behaviors.NoUpsert = true
				}
			}
			typed.Behaviors, info.Rules = true, true
			info.RulesTables = append(info.RulesTables, t.Type)
			if ops, ok := operations[t.Type]; ok {
				typed.Uses = ormUses(typed, ops)
				info.OperationStructs = append(info.OperationStructs, ops.Structs...)
				info.OperationEnums = append(info.OperationEnums, ops.Enums...)
			}
		}
		table.Behaviors = behaviors
		for _, c := range t.Columns {
			column, member, err := rustORMColumn(c, types[t.Type], uuidType, keyTypes, sqlutil.DDLPolicyOf(opts.Naming.OrDefault()))
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
		for _, many := range t.ToMany {
			if owned[many.Source] {
				continue
			}
			table.Many = append(table.Many, ormMany{Name: many.Name, Target: many.Source, Column: many.Field + "Id"})
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
		if c := t.AuditColumn(sqlutil.AuditUpdatedBy); c != nil && !typed.ReadOnly {
			typed.Update = append(typed.Update, ORMMember{Name: toRustFieldName(c.Field), Rename: renameOf(c.Field), Type: uuidType, Doc: "Who made the update, the acting user unless set.", Optional: true})
		}

		onPostgres, onSQLite := table, table
		if t.HistoryActor != "" {
			onPostgres.HistoryActor = true
			postgres.HistoryActorCTE = sqlutil.HistoryActorCTE(opts.Naming.HistoryActorSetting)
			postgres.ClearHistoryActor = sqlutil.ClearHistoryActor(opts.Naming.HistoryActorSetting)
		}
		onPostgres.Statements = tableStatements(sqlutil.TableStatementsOf(t).AllStatements())
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
				onSQLite.Statements = tableStatements(sqlutil.SQLiteTableStatementsOf(t).AllStatements())
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
	repository := map[string]bool{}
	for _, t := range info.Tables {
		repository[t.Type] = true
	}
	if postgres.References, postgres.Attachments, err = rowFeaturesOf(schema, tables, repository); err != nil {
		return nil, err
	}
	info.References, info.Attachments = postgres.References != nil, postgres.Attachments != nil
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

// writtenDefault is the JSON of the schema default a create writes for
// column c because the DDL leaves it out under policy: a Default<T, V>
// whose column the core's DDL would give V (sqlutil.ColumnDefault) and
// the policy does not. ok is false for every other column.
func writtenDefault(c sqlutil.ColumnModel, policy sqlutil.DDLPolicy) (json.RawMessage, bool) {
	if c.Def == nil || c.Default == nil || c.DefaultsToNow() {
		return nil, false
	}
	if _, core := sqlutil.ColumnDefault(c.Def, c.Type); !core {
		return nil, false
	}
	if _, carried := policy.ColumnDefault(c.Def, c.Type); carried {
		return nil, false
	}
	if sqlutil.IsTextType(c.Type) {
		text, err := json.Marshal(*c.Default)
		return text, err == nil
	}
	// A boolean or a number, which ColumnDefault takes only as JSON writes
	// it.
	literal := json.RawMessage(*c.Default)
	return literal, json.Valid(literal)
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
// policy is the naming file's [sql] table, which says whether the column
// carries its schema default.
func rustORMColumn(c sqlutil.ColumnModel, info *TypeInfo, uuidType string, keyTypes map[string]string, policy sqlutil.DDLPolicy) (ormColumn, ORMMember, error) {
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
	} else if literal, ok := writtenDefault(c, policy); ok {
		// The naming file's column_defaults is "off": the column carries
		// no DEFAULT, so the create writes the schema's, as an enum's.
		column.Auto = c.AutoGenerated
		column.Default = literal
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

	// A table the repositories only read has no create and no update
	// (D139).
	owned := c.Def.Owned()
	insertable := !c.Internal && c.Audit == "" && !owned && !t.ReadOnly
	if insertable {
		create := member
		create.Optional = !c.Required || column.Auto || c.Key || len(column.Default) > 0
		t.Create = append(t.Create, create)
	}
	if !c.Key && c.Audit == "" && !c.Internal && !owned && !t.ReadOnly {
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

// ormUses are the uses of table's behaviors that have operations, as
// orm.rs types them from ops: a struct per use, opened by the
// repositories' method <table>_<member>, with a method per operation.
func ormUses(table ORMTable, ops behaviortypes.TableOperations) []ORMUse {
	var out []ORMUse
	for _, use := range ops.Uses {
		member := strings.TrimPrefix(toRustFieldName(use.Member), "r#")
		u := ORMUse{
			Struct: use.Base + "Operations",
			Fn:     table.Fn + "_" + member,
			Member: use.Member,
			Doc: fmt.Sprintf("The operations of `%s`'s `%s`, %s (D103), each run through the behaviors' core in the transaction the repositories were opened over: on the row whose key it takes, or on the table as a whole (D104).",
				table.Type, use.Member, behaviortypes.An(use.Behavior)),
		}
		if !use.Anchored {
			u.Doc = fmt.Sprintf("The operations of the %s `%s` implements, under `%s` (D103), each run through the behaviors' core in the transaction the repositories were opened over: on the row whose key it takes, or on the table as a whole (D104).",
				use.Behavior, table.Type, use.Member)
		}
		for _, op := range use.Operations {
			doc := strings.Join(strings.Fields(op.Doc), " ")
			switch {
			case doc != "":
			case op.Table:
				doc = fmt.Sprintf("Runs the operation %s on the table as a whole, as a command without a row (D104).", op.Name)
			default:
				doc = fmt.Sprintf("Runs the operation %s on the row whose key is `key`.", op.Name)
			}
			u.Operations = append(u.Operations, ORMOperation{
				Name:   op.Name,
				Fn:     toRustFieldName(op.Name),
				Doc:    doc,
				Params: rustOperationType(op.Params, true),
				Result: rustOperationType(op.Result, true),
				Table:  op.Table,
			})
		}
		out = append(out, u)
	}
	return out
}

// ORMOperationExample is a call of the first operation on a row of the
// first use of the first table with one, as orm.rs's doc shows one:
// orm.order_status()?.transition(&key, &params, &options); "" for a crate
// with none.
func (o *ModuleOutput) ORMOperationExample() string {
	if o.ORM == nil {
		return ""
	}
	for _, table := range o.ORM.Tables {
		for _, use := range table.Uses {
			for _, op := range use.Operations {
				if !op.Table {
					return fmt.Sprintf("orm.%s()?.%s(&key, &params, &options)", use.Fn, op.Fn)
				}
			}
		}
	}
	return ""
}

// repositoriesMethods are the methods a dialect's generated Repositories
// holds whatever its tables, which a use's method may not take.
var repositoriesMethods = []string{"new", "acting", "as_system", "with_keys", "with_rules", "with_caller", "with_trace", "untyped"}

// checkOperationNames refuses a name the operations of a table's behaviors
// would give in orm.rs that another of the crate's already names: a type
// (a use's struct, an operation's parameters, result or what they hold, or
// an enum their narrowing names) that the schema, its behaviors' fields or
// the repositories name, and a method of a dialect's Repositories that
// opens a table's repository or another use.
func checkOperationNames(output *ModuleOutput) error {
	if output.ORM == nil {
		return nil
	}
	types := map[string]string{}
	claim := func(name, owner string) error {
		if prev, taken := types[name]; taken {
			return fmt.Errorf("rustgen: %s would name a type %s, which %s already names", owner, name, prev)
		}
		types[name] = owner
		return nil
	}
	for _, t := range output.Types {
		types[t.Name] = "the schema's type " + t.Name
	}
	for _, t := range output.ImportedTypes {
		types[t.Name] = "the import " + t.Name
	}
	for _, e := range output.Enums {
		types[e.Name] = "the schema's enum " + e.Name
	}
	for _, u := range output.Unions {
		types[u.Name] = "the schema's union " + u.Name
	}
	for _, s := range output.Scalars {
		types[s.Name] = "the scalar " + s.Name
	}
	for _, b := range output.Behaviors {
		for _, s := range b.Structs {
			types[s.Name] = "the behaviors of " + b.Name
		}
		for _, e := range b.Enums {
			types[e.Name] = "the behaviors of " + b.Name
		}
	}
	methods := map[string]string{}
	for _, name := range repositoriesMethods {
		methods[name] = "the repositories"
	}
	for _, table := range output.ORM.Tables {
		suffixes := []string{"Row", "Create", "Update", "Filter", "Repository"}
		if table.ReadOnly {
			// A table that is only read has no Create and no Update (D139).
			suffixes = []string{"Row", "Filter", "Repository"}
		}
		for _, suffix := range suffixes {
			types[table.Type+suffix] = "the repository of " + table.Type
		}
		methods[table.Fn] = "the repository of " + table.Type
	}
	for _, s := range output.ORM.OperationStructs {
		if err := claim(s.Name, "an operation of a table's behaviors"); err != nil {
			return err
		}
	}
	for _, e := range output.ORM.OperationEnums {
		if err := claim(e.Name, "an operation of a table's behaviors"); err != nil {
			return err
		}
	}
	for _, table := range output.ORM.Tables {
		for _, use := range table.Uses {
			owner := fmt.Sprintf("the operations of %s's %s", table.Type, use.Member)
			if err := claim(use.Struct, owner); err != nil {
				return err
			}
			if prev, taken := methods[use.Fn]; taken {
				return fmt.Errorf("rustgen: %s would open by the repositories' method %s, which %s already takes", owner, use.Fn, prev)
			}
			methods[use.Fn] = owner
		}
	}
	return nil
}
