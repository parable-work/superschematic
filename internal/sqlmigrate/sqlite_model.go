package sqlmigrate

import (
	"errors"
	"fmt"

	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	ir "github.com/parable-work/superschematic/ir"
)

// model resolves the schema as sqlgen does for Postgres and stores each
// table in SQLite's types, with its defaults in SQLite's forms. A unique
// constraint keeps the name Postgres gives it and becomes a unique index of
// that name. SQLite keeps no comments, so the model has none. The model
// refuses what SQLite has no form of (D27), naming the feature:
// @versioned, @optimistic, @searchField, projections, GIN and GIST
// indexes, and types it has no storage for.
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

	ns := newPGNamespace()
	var errs []error
	for i := range out.Tables {
		table, err := entityTable(&out.Tables[i], ns)
		if err != nil {
			return nil, err
		}
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

	graphs, err := modelGraphs(schema, m)
	if err != nil {
		return nil, err
	}
	m.Graphs = graphs
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
		for _, column := range t.SearchFields {
			field := t.OriginalName + "." + column
			for _, col := range t.Columns {
				if col.Name == column && col.Origin != "" {
					field = col.Origin
				}
			}
			refuse("%s is a @searchField", field)
		}
		for _, idx := range t.Indexes {
			if idx.Type == "GIN" || idx.Type == "GIST" {
				refuse("the index %s of %s is a %s index", idx.Name, t.OriginalName, idx.Type)
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
// default forms. It refuses a column whose type SQLite has no storage for.
func sqliteTable(t *Table) error {
	t.Comment = ""
	var errs []error
	for _, col := range t.Columns {
		pg := col.Type
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
		col.Type, col.Default = sqlite, def
	}
	return errors.Join(errs...)
}
