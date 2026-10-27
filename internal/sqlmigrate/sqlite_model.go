package sqlmigrate

import (
	"errors"
	"fmt"
	"slices"

	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// model resolves the schema as sqlgen does for Postgres and stores each
// table in SQLite's types, with its defaults in SQLite's forms. A unique
// constraint keeps the name Postgres gives it and becomes a unique index of
// that name. A @searchField's search_text column keeps its expression,
// with each field quoted as SQLite quotes it, and loses the trigram index,
// which SQLite has no operator class for (D27, amended). SQLite keeps no
// comments, so the model has none. A table's @enqueueOnChange (D80) is a
// trigger per write, which holds its insert. The model refuses what SQLite
// has no form of (D27), naming the feature: @versioned, @optimistic,
// projections, GIN and GIST indexes, and types it has no storage for.
func (sqliteDialect) model(schema *ir.Schema, opts sqlgen.Options) (*Model, error) {
	m := &Model{Version: ModelVersion, Dialect: SQLite, Service: opts.SchemaName}
	out, err := sqlgen.Generate(schema, opts)
	if err != nil {
		return nil, err
	}
	if out == nil {
		return m, nil
	}
	types := map[string]string{} // table name -> type name
	for i := range out.Tables {
		types[out.Tables[i].Name] = out.Tables[i].OriginalName
	}
	if err := sqliteRefusals(out, types); err != nil {
		return nil, err
	}
	// A declared query is verified and run on Postgres alone (D86).
	for _, td := range schema.Queries() {
		if td.Query.RunsOn(string(SQLite)) {
			return nil, fmt.Errorf("sqlmigrate: query %s runs on the sqlite dialect, which no query is verified or run on yet: limit it to postgres (dialects: [\"postgres\"]), or build the service for postgres alone (outputs.sql.dialects: [\"postgres\"])", td.Query.Name)
		}
	}

	ns := newPGNamespace()
	var errs []error
	for i := range out.Tables {
		table, err := entityTable(&out.Tables[i], ns)
		if err != nil {
			return nil, err
		}
		if fields := out.Tables[i].SearchFields; len(fields) > 0 {
			table.Indexes = slices.DeleteFunc(table.Indexes, func(idx *Index) bool {
				return idx.Name == sqlgen.SearchIndexName(table.Name)
			})
			// SQLite's keywords are not Postgres's, so every field is quoted.
			columnNamed(table, sqlgen.SearchTextColumn).Generated = sqlgen.SearchTextExprQuoted(fields, qs)
		}
		table.Queue = queueKeyOf(schema, &out.Tables[i])
		errs = append(errs, sqliteTable(table))
		m.Tables = append(m.Tables, table)
	}
	for _, jt := range out.JoinTables {
		table := joinTable(jt, types[jt.LeftTable], ns)
		errs = append(errs, sqliteTable(table))
		m.Tables = append(m.Tables, table)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	for _, e := range out.EnqueueTriggers {
		for i, t := range e.SQLiteTriggers(qs) {
			trigger := &Trigger{Name: t.Name, Table: t.Table, Definition: t.Definition, Origin: e.Origin}
			// The first trigger is the insert's or the update's, which
			// backfill needs one of (D88).
			if e.Backfill && i == 0 {
				trigger.Backfill = e.SQLiteBackfillSQL(qs)
			}
			m.Triggers = append(m.Triggers, trigger)
		}
	}

	graphs, err := modelGraphs(schema, m)
	if err != nil {
		return nil, err
	}
	m.Graphs = graphs
	pieces, err := sqlutil.OrderRawSQL(schema, string(SQLite))
	if err != nil {
		return nil, err
	}
	m.RawSQL = modelRawSQL(pieces)
	for _, s := range out.RoleSeeds {
		m.Roles = append(m.Roles, modelRole(s, s.SQLiteInsertSQL(qs), s.SQLiteUpdateSQL(qs)))
	}
	m.sort()
	return m, nil
}

// sqliteRefusals lists the features of a schema the SQLite dialect does
// not support, each naming the schema element that uses it.
func sqliteRefusals(out *sqlgen.DDLOutput, types map[string]string) error {
	var errs []error
	refuse := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("sqlmigrate: "+format+", which the sqlite dialect does not support", args...))
	}
	for _, t := range out.Tables {
		for _, idx := range t.Indexes {
			if idx.Type != "BTREE" {
				refuse("the index %s of %s is a %s index", idx.Name, t.OriginalName, idx.Type)
			}
			if idx.Opclass != "" {
				refuse("the index %s of %s names the operator class %s", idx.Name, t.OriginalName, idx.Opclass)
			}
		}
	}
	for _, h := range out.HistoryTables {
		refuse("%s is @versioned", types[h.SourceTableName])
	}
	for _, o := range out.OptimisticTables {
		refuse("%s is @optimistic", types[o.TableName])
	}
	for _, view := range out.Projections {
		refuse("%s is a projection (%s.%s)", view.TypeName, view.Pool, view.Name)
	}
	return errors.Join(errs...)
}

// sqliteTable stores a table the Postgres model built in SQLite's types and
// default forms, and records what a column that holds JSON holds, with a
// list's element. It refuses a column whose type SQLite has no storage for.
func sqliteTable(t *Table) error {
	t.Comment = ""
	var errs []error
	for _, col := range t.Columns {
		pg := col.Type
		if sqlutil.IsVectorType(pg) {
			// SQLite has no vector type or index, and no nearest (D84).
			errs = append(errs, fmt.Errorf("sqlmigrate: column %s.%s%s is %s, an Embedding.Vector, which the sqlite dialect has no storage for: build the service for postgres alone (outputs.sql.dialects: [\"postgres\"]), or keep the vector as JSON (JsonField<Embedding.Vector>); vectors on SQLite follow",
				t.Name, col.Name, originNote(col), pg))
			continue
		}
		sqlite, err := sqliteType(pg)
		if err != nil {
			errs = append(errs, fmt.Errorf("sqlmigrate: column %s.%s%s is %s, which the sqlite dialect has no storage for",
				t.Name, col.Name, originNote(col), pg))
			continue
		}
		def, err := sqliteDefault(col.Default, pg)
		if err != nil {
			errs = append(errs, fmt.Errorf("sqlmigrate: column %s.%s%s: %w", t.Name, col.Name, originNote(col), err))
			continue
		}
		col.Type, col.Default, col.Holds, col.Element = sqlite, def, sqliteHolds(pg), sqliteElement(pg)
	}
	return errors.Join(errs...)
}
