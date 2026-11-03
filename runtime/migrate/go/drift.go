package migrate

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

/*
Drift is how a live database differs from a model (adopt --diff, D85): the
tables, columns, constraints, indexes, functions, triggers and views the
model holds and the database does not, or holds otherwise, and those the
database holds and the model does not. adopt records a model as the
database's state without looking at the database (D27), so a database
adopted while it differs takes plans that fail; the diff says what to
change, by hand or by renaming with @table and @column, first.

A database names a constraint, an index or a trigger as Postgres does,
and a column's type and default as format_type and pg_get_expr write them;
the diff compares a model's in those forms. SQLite names no constraint, so
on SQLite it compares the tables, their columns and the indexes alone. The
runner's own tables are not the service's, and are left out.
*/

// driftModel is the part of a model document the diff reads.
type driftModel struct {
	Dialect    Dialect  `json:"dialect"`
	Extensions []string `json:"extensions"`
	Tables     []struct {
		Name    string `json:"name"`
		Columns []struct {
			Name      string `json:"name"`
			Type      string `json:"type"`
			Nullable  bool   `json:"nullable"`
			Default   string `json:"default"`
			Generated string `json:"generated"`
		} `json:"columns"`
		PrimaryKey  *struct{ Name string }  `json:"primaryKey"`
		Uniques     []struct{ Name string } `json:"uniques"`
		ForeignKeys []struct{ Name string } `json:"foreignKeys"`
		Checks      []struct{ Name string } `json:"checks"`
		Indexes     []struct{ Name string } `json:"indexes"`
	} `json:"tables"`
	Functions []struct{ Name string } `json:"functions"`
	Triggers  []struct {
		Name  string `json:"name"`
		Table string `json:"table"`
	} `json:"triggers"`
	Views []struct {
		Schema string `json:"schema"`
		Name   string `json:"name"`
	} `json:"views"`
}

// liveColumn is a column as the database holds it.
type liveColumn struct {
	typ, def  string
	nullable  bool
	generated bool
}

// liveSchema is what the database holds, by name.
type liveSchema struct {
	tables      map[string]map[string]liveColumn
	constraints map[string]map[string]bool // table -> constraint
	indexes     map[string]bool
	functions   map[string]bool
	triggers    map[string]bool // table.trigger
	views       map[string]bool // schema.view
	extensions  map[string]bool
}

// Drift returns, sorted, each way the database differs from model, one
// line each; none when it holds what the model holds.
func (r *Runner) Drift(ctx context.Context, model *Model) ([]string, error) {
	if dialect := r.Driver.Dialect(); model.Dialect != dialect {
		return nil, refusef("the model is for %s and the database is %s", model.Dialect, dialect)
	}
	var m driftModel
	if err := json.Unmarshal(model.Canonical, &m); err != nil {
		return nil, fmt.Errorf("read the model: %w", err)
	}
	var live *liveSchema
	err := r.Driver.Transact(ctx, TxOptions{ReadOnly: true}, func(ctx context.Context, conn Conn) error {
		var err error
		if model.Dialect == Postgres {
			live, err = readPostgres(ctx, conn, m)
		} else {
			live, err = readSQLite(ctx, conn)
		}
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("read the database's schema: %w", err)
	}
	return drift(m, live, model.Dialect), nil
}

func drift(m driftModel, live *liveSchema, dialect Dialect) []string {
	var out []string
	add := func(format string, args ...any) { out = append(out, fmt.Sprintf(format, args...)) }
	modelTables := map[string]bool{}
	indexes := map[string]bool{}
	for _, t := range m.Tables {
		modelTables[t.Name] = true
		columns, ok := live.tables[t.Name]
		if !ok {
			add("table %s: the model holds it, and the database does not", t.Name)
			continue
		}
		names := map[string]bool{}
		for _, c := range t.Columns {
			names[c.Name] = true
			lc, ok := columns[c.Name]
			switch {
			case !ok:
				add("column %s.%s: the model holds it, and the database does not", t.Name, c.Name)
				continue
			case !sameType(c.Type, lc.typ, dialect):
				add("column %s.%s: the model's type is %s, and the database's %s", t.Name, c.Name, c.Type, lc.typ)
			}
			if c.Nullable != lc.nullable {
				add("column %s.%s: the model %s null, and the database %s", t.Name, c.Name, allows(c.Nullable), allows(lc.nullable))
			}
			if c.Generated == "" && !lc.generated && normalDefault(c.Default) != normalDefault(lc.def) {
				add("column %s.%s: the model's default is %s, and the database's %s", t.Name, c.Name, orNone(c.Default), orNone(lc.def))
			}
		}
		for _, name := range sortedNames(columns) {
			if !names[name] {
				add("column %s.%s: the database holds it, and the model does not", t.Name, name)
			}
		}
		for _, ix := range t.Indexes {
			indexes[ix.Name] = true
		}
		if dialect == SQLite {
			// SQLite keeps a unique constraint as an index of its name.
			for _, u := range t.Uniques {
				indexes[u.Name] = true
			}
			continue
		}
		want := map[string]bool{}
		if t.PrimaryKey != nil {
			want[t.PrimaryKey.Name] = true
		}
		for _, group := range [][]struct{ Name string }{t.Uniques, t.ForeignKeys, t.Checks} {
			for _, c := range group {
				want[c.Name] = true
			}
		}
		for _, name := range sortedNames(want) {
			if !live.constraints[t.Name][name] {
				add("constraint %s on %s: the model holds it, and the database does not", name, t.Name)
			}
		}
		for _, name := range sortedNames(live.constraints[t.Name]) {
			if !want[name] {
				add("constraint %s on %s: the database holds it, and the model does not", name, t.Name)
			}
		}
	}
	for _, name := range sortedNames(live.tables) {
		if !modelTables[name] {
			add("table %s: the database holds it, and the model does not", name)
		}
	}
	compareNames(add, "index", indexes, live.indexes)
	if dialect == SQLite {
		slices.Sort(out)
		return out
	}
	functions, triggers, views := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, f := range m.Functions {
		functions[f.Name] = true
	}
	for _, t := range m.Triggers {
		triggers[t.Table+"."+t.Name] = true
	}
	for _, v := range m.Views {
		views[v.Schema+"."+v.Name] = true
	}
	compareNames(add, "function", functions, live.functions)
	compareNames(add, "trigger", triggers, live.triggers)
	compareNames(add, "view", views, live.views)
	for _, ext := range m.Extensions {
		if !live.extensions[ext] {
			add("extension %s: the model holds it, and the database does not", ext)
		}
	}
	slices.Sort(out)
	return out
}

func compareNames[V any](add func(string, ...any), what string, model map[string]bool, live map[string]V) {
	for _, name := range sortedNames(model) {
		if _, ok := live[name]; !ok {
			add("%s %s: the model holds it, and the database does not", what, name)
		}
	}
	for _, name := range sortedNames(live) {
		if !model[name] {
			add("%s %s: the database holds it, and the model does not", what, name)
		}
	}
}

func sortedNames[V any](m map[string]V) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func allows(nullable bool) string {
	if nullable {
		return "allows"
	}
	return "refuses"
}

func orNone(def string) string {
	if def == "" {
		return "none"
	}
	return def
}

// runnerTable reports whether name is one of the runner's own tables.
func runnerTable(name string) bool {
	return strings.HasPrefix(name, "superschematic_")
}

// pgTypeAliases are the names format_type writes for a type a model may
// spell otherwise.
var pgTypeAliases = map[string]string{
	"timestamptz": "timestamp with time zone",
	"timestamp":   "timestamp without time zone",
	"timetz":      "time with time zone",
	"time":        "time without time zone",
	"int8":        "bigint",
	"int":         "integer",
	"int4":        "integer",
	"int2":        "smallint",
	"bool":        "boolean",
	"float8":      "double precision",
	"float4":      "real",
	"varchar":     "character varying",
	"char":        "character",
	"decimal":     "numeric",
}

// sameType reports whether a model's column type and a database's are one.
func sameType(model, live string, dialect Dialect) bool {
	if dialect == SQLite {
		return strings.EqualFold(strings.TrimSpace(model), strings.TrimSpace(live))
	}
	return pgType(model) == pgType(live)
}

func pgType(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	suffix := ""
	for strings.HasSuffix(t, "[]") {
		t, suffix = strings.TrimSuffix(t, "[]"), suffix+"[]"
	}
	name, args, _ := strings.Cut(t, "(")
	name = strings.TrimSpace(name)
	if alias, ok := pgTypeAliases[name]; ok {
		name = alias
	}
	if args != "" {
		name += "(" + strings.ReplaceAll(args, " ", "")
	}
	return name + suffix
}

// castSuffix is a cast pg_get_expr writes after a literal: 'x'::text,
// '{}'::text[], 1::bigint.
var castSuffix = regexp.MustCompile(`::[a-z ]+(\([0-9, ]+\))?(\[\])*`)

// normalDefault is a default as both sides compare it: no casts, no outer
// parentheses, in lower case.
func normalDefault(def string) string {
	def = strings.ToLower(strings.TrimSpace(def))
	def = castSuffix.ReplaceAllString(def, "")
	for strings.HasPrefix(def, "(") && strings.HasSuffix(def, ")") && balanced(def[1:len(def)-1]) {
		def = strings.TrimSpace(def[1 : len(def)-1])
	}
	return def
}

func balanced(s string) bool {
	depth := 0
	for _, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return false
			}
		}
	}
	return depth == 0
}

// readPostgres reads the current schema of a Postgres database, and the
// views of the schemas m's views live in.
func readPostgres(ctx context.Context, conn Conn, m driftModel) (*liveSchema, error) {
	live := &liveSchema{
		tables: map[string]map[string]liveColumn{}, constraints: map[string]map[string]bool{},
		indexes: map[string]bool{}, functions: map[string]bool{}, triggers: map[string]bool{},
		views: map[string]bool{}, extensions: map[string]bool{},
	}
	query := func(n int, sql string, args []any, row func(cols []string)) error {
		return conn.Query(ctx, sql, args, func(scan func(dest ...any) error) error {
			cols := make([]string, n)
			dest := make([]any, len(cols))
			for i := range cols {
				dest[i] = &cols[i]
			}
			if err := scan(dest...); err != nil {
				return err
			}
			row(cols)
			return nil
		})
	}
	// Each query's n columns are text, never NULL.
	type step struct {
		n    int
		sql  string
		args []any
		row  func(cols []string)
	}
	steps := []step{
		{1, `SELECT c.relname FROM pg_class c WHERE c.relnamespace = current_schema()::regnamespace AND c.relkind IN ('r', 'p') AND NOT c.relispartition`, nil,
			func(cols []string) {
				if !runnerTable(cols[0]) {
					live.tables[cols[0]] = map[string]liveColumn{}
				}
			}},
		{6, `SELECT c.relname, a.attname, format_type(a.atttypid, a.atttypmod), CASE WHEN a.attnotnull THEN 'no' ELSE 'yes' END, COALESCE(pg_get_expr(d.adbin, d.adrelid), ''), CASE WHEN a.attgenerated <> '' THEN 'yes' ELSE 'no' END
			FROM pg_attribute a JOIN pg_class c ON c.oid = a.attrelid LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
			WHERE c.relnamespace = current_schema()::regnamespace AND c.relkind IN ('r', 'p') AND NOT c.relispartition AND a.attnum > 0 AND NOT a.attisdropped`, nil,
			func(cols []string) {
				if columns := live.tables[cols[0]]; columns != nil {
					def := cols[4]
					if cols[5] == "yes" {
						def = ""
					}
					columns[cols[1]] = liveColumn{typ: cols[2], nullable: cols[3] == "yes", def: def, generated: cols[5] == "yes"}
				}
			}},
		{2, `SELECT c.relname, con.conname FROM pg_constraint con JOIN pg_class c ON c.oid = con.conrelid
			WHERE c.relnamespace = current_schema()::regnamespace AND con.contype IN ('p', 'u', 'f', 'c') AND NOT c.relispartition`, nil,
			func(cols []string) {
				if live.tables[cols[0]] == nil {
					return
				}
				if live.constraints[cols[0]] == nil {
					live.constraints[cols[0]] = map[string]bool{}
				}
				live.constraints[cols[0]][cols[1]] = true
			}},
		{2, `SELECT t.relname, i.relname FROM pg_index x JOIN pg_class i ON i.oid = x.indexrelid JOIN pg_class t ON t.oid = x.indrelid
			WHERE t.relnamespace = current_schema()::regnamespace AND NOT t.relispartition
			AND NOT EXISTS (SELECT 1 FROM pg_constraint con WHERE con.conindid = x.indexrelid)`, nil,
			func(cols []string) {
				if live.tables[cols[0]] != nil {
					live.indexes[cols[1]] = true
				}
			}},
		{1, `SELECT p.proname FROM pg_proc p WHERE p.pronamespace = current_schema()::regnamespace
			AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_proc'::regclass AND d.objid = p.oid AND d.deptype = 'e')`, nil,
			func(cols []string) { live.functions[cols[0]] = true }},
		{2, `SELECT c.relname, t.tgname FROM pg_trigger t JOIN pg_class c ON c.oid = t.tgrelid
			WHERE c.relnamespace = current_schema()::regnamespace AND NOT t.tgisinternal`, nil,
			func(cols []string) { live.triggers[cols[0]+"."+cols[1]] = true }},
		{1, `SELECT extname FROM pg_extension`, nil,
			func(cols []string) { live.extensions[cols[0]] = true }},
	}
	schemas := map[string]bool{}
	for _, v := range m.Views {
		schemas[v.Schema] = true
	}
	for _, schema := range sortedNames(schemas) {
		steps = append(steps, step{1, `SELECT viewname FROM pg_views WHERE schemaname = $1`, []any{schema},
			func(cols []string) { live.views[schema+"."+cols[0]] = true }})
	}
	for _, s := range steps {
		if err := query(s.n, s.sql, s.args, s.row); err != nil {
			return nil, err
		}
	}
	return live, nil
}

// readSQLite reads a SQLite database's tables, their columns and indexes.
func readSQLite(ctx context.Context, conn Conn) (*liveSchema, error) {
	live := &liveSchema{tables: map[string]map[string]liveColumn{}, indexes: map[string]bool{}}
	var tables []string
	err := conn.Query(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`, nil,
		func(scan func(dest ...any) error) error {
			var name string
			if err := scan(&name); err != nil {
				return err
			}
			if !runnerTable(name) {
				tables = append(tables, name)
			}
			return nil
		})
	if err != nil {
		return nil, err
	}
	for _, table := range tables {
		columns := map[string]liveColumn{}
		err := conn.Query(ctx, `SELECT name, type, "notnull", COALESCE(dflt_value, ''), hidden FROM pragma_table_xinfo(?)`, []any{table},
			func(scan func(dest ...any) error) error {
				var name, typ, def string
				var notNull, hidden int64
				if err := scan(&name, &typ, &notNull, &def, &hidden); err != nil {
					return err
				}
				generated := hidden == 2 || hidden == 3
				if generated {
					def = ""
				}
				columns[name] = liveColumn{typ: typ, nullable: notNull == 0, def: def, generated: generated}
				return nil
			})
		if err != nil {
			return nil, err
		}
		live.tables[table] = columns
	}
	err = conn.Query(ctx, `SELECT name, tbl_name FROM sqlite_master WHERE type = 'index' AND sql IS NOT NULL`, nil,
		func(scan func(dest ...any) error) error {
			var name, table string
			if err := scan(&name, &table); err != nil {
				return err
			}
			if live.tables[table] != nil {
				live.indexes[name] = true
			}
			return nil
		})
	return live, err
}
