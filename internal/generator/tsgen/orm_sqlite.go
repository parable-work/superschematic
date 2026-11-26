package tsgen

import (
	"strings"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
)

// The repositories of a DB schema on SQLite and D1 in TypeScript (D182):
// orm-sqlite.ts, which package.json exports as "./orm/sqlite", holds each
// table as data again, its statements in SQLite's words as sqlutil writes
// them for the Rust repositories on SQLite (sqlutil.SQLiteTableStatementsOf,
// D116), and createOrm(db), which the HTTP runtime's sqliteOrm runs over a
// SqlDatabase: a D1 binding in a Worker, or openSqlite's file under Bun. It
// imports the row and repository types from ./orm, as types alone, and the
// runtime from its ./d1 entry, so a Worker bundles it without pg. A
// database that lists sqlite and not postgres gets an orm.ts of types alone.

// ORMSQLiteFile is the file of a types package that holds its repositories
// on SQLite, and ORMSQLiteEntry the entry that exports it.
const (
	ORMSQLiteFile  = "orm-sqlite.ts"
	ORMSQLiteEntry = "/orm/sqlite"
)

// ORMSQLite is what orm-sqlite.ts writes.
type ORMSQLite struct {
	// Clauses are sqlutil.SQLiteClauses by name.
	Clauses []sqlutil.NamedStatement
	// Tables are the tables on SQLite: each ORMTable with its statements,
	// unique keys and columns as SQLite holds them.
	Tables []ORMTable
	// LeftOut are the tables the SQLite repositories do not hold yet, each
	// with why (sqlutil.SQLiteTableRefusal); orm-sqlite.ts names them.
	LeftOut []ORMRefusal
}

// ORMRefusal is a table left out of orm-sqlite.ts, and why.
type ORMRefusal struct {
	Type   string
	Reason string
}

// sqliteORMTable is table as orm-sqlite.ts writes it, from t, its model:
// its statements and unique keys in SQLite's words, its JSON columns read
// as the text SQLite holds them in, its audience compared through
// json_each, and no merge or history actor, which SQLite has no form of.
// The reason is SQLite's refusal of the table, "" for none.
func sqliteORMTable(t *sqlutil.TableModel, table ORMTable) (ORMTable, string) {
	if reason := sqlutil.SQLiteTableRefusal(t); reason != "" {
		return ORMTable{}, reason
	}
	out := table
	out.Merge, out.HistoryActor = nil, false
	statements := map[string]string{}
	for _, st := range sqlutil.SQLiteTableStatementsOf(t).AllStatements() {
		statements[st.Name] = st.SQL
	}
	out.Statements = nil
	for _, st := range table.Statements {
		if st.Name == "nearest" {
			continue
		}
		out.Statements = append(out.Statements, sqlutil.NamedStatement{Name: st.Name, SQL: statements[st.Name]})
	}
	out.UniqueKeys = nil
	for _, k := range t.UniqueKeys {
		k = k.SQLite()
		out.UniqueKeys = append(out.UniqueKeys, ORMUniqueKey{Name: k.Name, Fields: k.Fields, Conflict: k.Conflict, Keep: k.Keep, Rows: k.Rows, Held: k.Held, Expression: k.Expression})
	}
	out.Columns = make([]ORMColumn, len(table.Columns))
	for i, c := range table.Columns {
		// SQLite holds every JSON value as its text (D116).
		if c.JSON {
			c.JSON, c.JSONText = false, true
		}
		out.Columns[i] = c
	}
	if table.Audience != nil {
		audience := *table.Audience
		audience.Probe = ""
		out.Audience = &audience
	}
	return out, ""
}

// AudienceContains and AudiencePlaceholder are the formats of an
// @audience table's condition on SQLite (sqlutil.SQLiteAudienceContains),
// which orm-sqlite.ts writes into its audience.
func (ORMTable) AudienceContains() string    { return sqlutil.SQLiteAudienceContains }
func (ORMTable) AudiencePlaceholder() string { return sqlutil.SQLiteAudiencePlaceholder }

// SQLiteRuntimeTypes are the types orm-sqlite.ts imports from the HTTP
// runtime's ./d1 entry, sorted.
func (o *ORMInfo) SQLiteRuntimeTypes() []string {
	if o.Rules != nil {
		return []string{"OrmDefinition", "OrmOptions", "RulesLayout", "SqlDatabase", "TableDefinition"}
	}
	return []string{"OrmDefinition", "OrmOptions", "SqlDatabase", "TableDefinition"}
}

// LeftOutNames are the tables orm-sqlite.ts leaves out, joined.
func (s *ORMSQLite) LeftOutNames() string {
	names := make([]string, len(s.LeftOut))
	for i, r := range s.LeftOut {
		names[i] = r.Type
	}
	return strings.Join(names, ", ")
}
