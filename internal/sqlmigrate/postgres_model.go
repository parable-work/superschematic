package sqlmigrate

import (
	"fmt"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/graphdesc"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	ir "github.com/parable-work/superschematic/ir"
)

// model replays sqlgen's output in create.sql's order: entity tables, join
// tables, history tables with their indexes, then foreign keys, indexes,
// functions, triggers and views. The order matters only for the names
// Postgres chooses for unnamed constraints, which depend on the names taken
// before them.
func (postgresDialect) model(schema *ir.Schema, opts sqlgen.Options) (*Model, error) {
	m := &Model{Version: ModelVersion, Dialect: Postgres, Service: opts.SchemaName}
	out, err := sqlgen.Generate(schema, opts)
	if err != nil {
		return nil, err
	}
	if out == nil {
		return m, nil
	}

	m.Extensions = append([]string(nil), out.Extensions...)
	sort.Strings(m.Extensions)

	ns := newPGNamespace()
	types := map[string]string{} // table name -> type name
	for i := range out.Tables {
		types[out.Tables[i].Name] = out.Tables[i].OriginalName
	}
	optimistic := map[string]bool{}
	for _, o := range out.OptimisticTables {
		optimistic[o.TableName] = true
	}
	histories := map[string]sqlgen.HistoryTable{}
	for _, h := range out.HistoryTables {
		histories[h.SourceTableName] = h
	}

	for i := range out.Tables {
		table, err := entityTable(&out.Tables[i], ns)
		if err != nil {
			return nil, err
		}
		table.Optimistic = optimistic[table.Name]
		if h, ok := histories[table.Name]; ok {
			table.History = h.Name
			table.HistoryExclude = append([]string(nil), h.ExcludedColumns...)
		}
		m.Tables = append(m.Tables, table)
	}
	for _, jt := range out.JoinTables {
		m.Tables = append(m.Tables, joinTable(jt, types[jt.LeftTable], ns))
	}
	for _, h := range out.HistoryTables {
		table, err := historyTable(h, types[h.SourceTableName], ns)
		if err != nil {
			return nil, err
		}
		m.Tables = append(m.Tables, table)

		capture, err := h.CaptureFunctionSQL()
		if err != nil {
			return nil, err
		}
		origin := types[h.SourceTableName]
		m.Functions = append(m.Functions, &Function{Name: h.FunctionName, Definition: capture, Origin: origin})
		if h.RetentionDays > 0 {
			prune, err := h.PruneFunctionSQL()
			if err != nil {
				return nil, err
			}
			m.Functions = append(m.Functions, &Function{
				Name: h.PruneFunctionName, Arguments: sqlgen.PruneFunctionArguments, Definition: prune, Origin: origin,
			})
		}
		for _, t := range h.Triggers() {
			trigger, err := modelTrigger(t, origin)
			if err != nil {
				return nil, err
			}
			m.Triggers = append(m.Triggers, trigger)
		}
	}
	for _, o := range out.OptimisticTables {
		bump, err := o.BumpFunctionSQL()
		if err != nil {
			return nil, err
		}
		origin := types[o.TableName]
		m.Functions = append(m.Functions, &Function{Name: o.FunctionName, Definition: bump, Origin: origin})
		trigger, err := modelTrigger(o.Trigger(), origin)
		if err != nil {
			return nil, err
		}
		m.Triggers = append(m.Triggers, trigger)
	}

	schemas := map[string]bool{}
	for _, view := range out.Projections {
		schemas[view.Pool] = true
		m.Views = append(m.Views, modelView(view))
	}
	for name := range schemas {
		m.Schemas = append(m.Schemas, name)
	}
	sort.Strings(m.Schemas)

	graphs, err := modelGraphs(schema, m)
	if err != nil {
		return nil, err
	}
	m.Graphs = graphs

	m.sort()
	return m, nil
}

// entityTable is a DB type's table. The primary key and each UNIQUE clause
// get the name Postgres gives them when create.sql creates the table.
func entityTable(t *sqlgen.Table, ns *pgNamespace) (*Table, error) {
	table := &Table{Name: t.Name, Kind: TableEntity, Origin: t.OriginalName, Comment: t.Doc}
	if table.Comment == "" {
		table.Comment = "Generated from schema type: " + t.OriginalName
	}
	raw := map[string]string{} // quoted name -> name
	var key string
	for _, col := range t.Columns {
		raw[col.QuotedName] = col.Name
		table.Columns = append(table.Columns, &Column{
			Name: col.Name, Origin: col.Origin, Type: col.Type, Nullable: col.Nullable, Default: col.DefaultValue,
		})
		if col.IsPrimaryKey {
			key = col.Name
		}
	}
	if len(t.SearchFields) > 0 {
		table.Columns = append(table.Columns, &Column{
			Name: sqlgen.SearchTextColumn, Type: "TEXT", Nullable: true, Generated: sqlgen.SearchTextExpr(t.SearchFields),
		})
	}
	unquote := func(quoted []string) ([]string, error) {
		names := make([]string, len(quoted))
		for i, q := range quoted {
			name, ok := raw[q]
			if !ok {
				return nil, fmt.Errorf("sqlmigrate: table %s has no column %s", t.Name, q)
			}
			names[i] = name
		}
		return names, nil
	}

	ns.relation(t.Name)
	if key == "" {
		return nil, fmt.Errorf("sqlmigrate: table %s has no primary key", t.Name)
	}
	table.PrimaryKey = &Constraint{Name: ns.chooseConstraintIndexName(t.Name, []string{key}, true), Columns: []string{key}}
	// Postgres drops a UNIQUE clause that repeats an earlier one.
	seen := map[string]bool{}
	for _, quoted := range t.Unique {
		columns, err := unquote(quoted)
		if err != nil {
			return nil, err
		}
		if seen[strings.Join(columns, ",")] {
			continue
		}
		seen[strings.Join(columns, ",")] = true
		table.Uniques = append(table.Uniques, &Constraint{
			Name: ns.chooseConstraintIndexName(t.Name, columns, false), Columns: columns,
		})
	}

	for _, fk := range t.ForeignKeys {
		onDelete := fk.OnDelete
		if onDelete == "" {
			onDelete = "CASCADE"
		}
		table.ForeignKeys = append(table.ForeignKeys, &ForeignKey{
			Name: fk.ConstraintName, Columns: []string{fk.Column},
			RefTable: fk.RefTable, RefColumns: []string{fk.RefColumn}, OnDelete: onDelete,
		})
	}
	for _, idx := range t.Indexes {
		columns, err := unquote(idx.Columns)
		if err != nil {
			return nil, err
		}
		table.Indexes = append(table.Indexes, &Index{
			Name: idx.Name, Columns: columns, Method: idx.Type, Unique: idx.Unique, Where: idx.Where,
		})
	}
	if len(t.SearchFields) > 0 {
		table.Indexes = append(table.Indexes, &Index{
			Name: sqlgen.SearchIndexName(t.Name), Columns: []string{sqlgen.SearchTextColumn},
			Method: "GIN", Opclass: "gin_trgm_ops",
		})
	}
	return table, nil
}

// joinTable is a @manyToMany join table. leftType is the type that declares
// the relation.
func joinTable(jt sqlgen.JoinTable, leftType string, ns *pgNamespace) *Table {
	columns := []string{jt.LeftColumn, jt.RightColumn}
	ns.relation(jt.Name)
	return &Table{
		Name:    jt.Name,
		Kind:    TableJoin,
		Origin:  leftType + "." + jt.OriginalField,
		Comment: "Join table for " + jt.LeftTable + "." + jt.OriginalField,
		Columns: []*Column{
			{Name: jt.LeftColumn, Type: jt.LeftColumnType},
			{Name: jt.RightColumn, Type: jt.RightColumnType},
		},
		PrimaryKey: &Constraint{Name: ns.chooseConstraintIndexName(jt.Name, columns, true), Columns: columns},
		ForeignKeys: []*ForeignKey{
			{Name: jt.LeftConstraintName, Columns: []string{jt.LeftColumn}, RefTable: jt.LeftTable, RefColumns: []string{"id"}, OnDelete: "CASCADE"},
			{Name: jt.RightConstraintName, Columns: []string{jt.RightColumn}, RefTable: jt.RightTable, RefColumns: []string{"id"}, OnDelete: "CASCADE"},
		},
	}
}

// historyTable is a @versioned table's history table, with its indexes and
// the default partition of a partitioned one. sourceType is the versioned
// type.
func historyTable(h sqlgen.HistoryTable, sourceType string, ns *pgNamespace) (*Table, error) {
	table := &Table{
		Name:    h.Name,
		Kind:    TableHistory,
		Origin:  sourceType,
		Comment: "Version history for " + h.SourceTableName,
	}
	for _, col := range h.Columns() {
		table.Columns = append(table.Columns, &Column{
			Name: col.Name, Type: col.Type, Nullable: col.Nullable, Default: col.DefaultValue,
		})
	}
	ns.relation(h.Name)
	if h.Partitioned() {
		table.PartitionBy = h.PartitionClause()
		table.Partitions = []*Partition{{Name: h.DefaultPartitionName, Bound: "DEFAULT"}}
		ns.relation(h.DefaultPartitionName)
		table.Indexes = append(table.Indexes, &Index{Name: h.VersionIndexName, Columns: []string{h.KeyColumn, "_version"}, Method: "BTREE"})
	} else {
		table.PrimaryKey = &Constraint{
			Name: ns.chooseConstraintIndexName(h.Name, []string{"history_id"}, true), Columns: []string{"history_id"},
		}
		table.Indexes = append(table.Indexes, &Index{Name: h.UniqueIndexName, Columns: []string{h.KeyColumn, "_version"}, Method: "BTREE", Unique: true})
	}
	table.Indexes = append(table.Indexes, &Index{Name: h.RecordedIndexName, Columns: []string{h.KeyColumn, "recorded_at"}, Method: "BTREE"})
	if h.RetentionDays > 0 {
		table.Indexes = append(table.Indexes, &Index{Name: h.RetentionIndexName, Columns: []string{"recorded_at"}, Method: "BTREE"})
	}
	for _, idx := range table.Indexes {
		ns.relation(idx.Name)
	}
	return table, nil
}

func modelTrigger(t sqlgen.Trigger, origin string) (*Trigger, error) {
	definition, err := t.SQL()
	if err != nil {
		return nil, err
	}
	return &Trigger{Name: t.Name, Table: t.Table, Definition: definition, Origin: origin}, nil
}

func modelView(view sqlgen.ProjectionView) *View {
	v := &View{
		Schema:     view.Pool,
		Name:       view.Name,
		Owner:      view.ViewOwner,
		Definition: sqlgen.ProjectionViewStatements(view),
		Origin:     view.TypeName,
	}
	for _, col := range view.Columns {
		v.Columns = append(v.Columns, &ViewColumn{Name: col.Name, Type: col.Type, Nullable: col.Nullable})
	}
	for _, read := range view.Reads {
		v.Reads = append(v.Reads, &ColumnRef{Table: read.Table, Column: read.Column})
	}
	return v
}

// modelGraphs lists the schema's version graphs with each member's content
// columns: every column of the member's table that is not one of the
// graph's role columns (D17: the entity key, id, ref, tombstone, version,
// root and author columns) and not excluded (the audit fields and
// @conflictUnit('excluded') fields), as the version-graph core decides.
func modelGraphs(schema *ir.Schema, m *Model) ([]*Graph, error) {
	graphs, err := graphdesc.Graphs(schema)
	if err != nil {
		return nil, err
	}
	tables := map[string]*Table{}
	for _, t := range m.Tables {
		tables[t.Name] = t
	}
	var out []*Graph
	for _, g := range graphs {
		graph := &Graph{Name: g.Name, SchemaEpoch: int(g.Root.VersionGraph.SchemaEpoch)}
		for _, kind := range g.Descriptor.Kinds {
			table := tables[kind.Table]
			if table == nil {
				return nil, fmt.Errorf("sqlmigrate: version graph %s: member table %s is not in the model", g.Name, kind.Table)
			}
			notContent := map[string]bool{
				kind.Key: true, kind.ID: true, kind.Ref: true, kind.Tombstone: true,
				kind.Version: true, kind.Root: true, kind.Author: true,
			}
			for _, column := range kind.Excluded {
				notContent[column] = true
			}
			member := &GraphMember{Table: kind.Table, Content: []string{}}
			for _, col := range table.Columns {
				if !notContent[col.Name] {
					member.Content = append(member.Content, col.Name)
				}
			}
			sort.Strings(member.Content)
			graph.Members = append(graph.Members, member)
		}
		out = append(out, graph)
	}
	return out, nil
}

// sort puts every object list in name order, so the model's JSON, and its
// hash, depend only on the schema and the compiler. Columns keep
// create.sql's order.
func (m *Model) sort() {
	sort.Slice(m.Tables, func(i, j int) bool { return m.Tables[i].Name < m.Tables[j].Name })
	for _, t := range m.Tables {
		sort.Slice(t.Uniques, func(i, j int) bool { return t.Uniques[i].Name < t.Uniques[j].Name })
		sort.Slice(t.ForeignKeys, func(i, j int) bool { return t.ForeignKeys[i].Name < t.ForeignKeys[j].Name })
		sort.Slice(t.Indexes, func(i, j int) bool { return t.Indexes[i].Name < t.Indexes[j].Name })
		sort.Slice(t.Partitions, func(i, j int) bool { return t.Partitions[i].Name < t.Partitions[j].Name })
	}
	sort.Slice(m.Functions, func(i, j int) bool { return m.Functions[i].Name < m.Functions[j].Name })
	sort.Slice(m.Triggers, func(i, j int) bool {
		if m.Triggers[i].Name != m.Triggers[j].Name {
			return m.Triggers[i].Name < m.Triggers[j].Name
		}
		return m.Triggers[i].Table < m.Triggers[j].Table
	})
	sort.Slice(m.Views, func(i, j int) bool {
		if m.Views[i].Schema != m.Views[j].Schema {
			return m.Views[i].Schema < m.Views[j].Schema
		}
		return m.Views[i].Name < m.Views[j].Name
	})
	sort.Slice(m.Graphs, func(i, j int) bool { return m.Graphs[i].Name < m.Graphs[j].Name })
	for _, g := range m.Graphs {
		sort.Slice(g.Members, func(i, j int) bool { return g.Members[i].Table < g.Members[j].Table })
	}
}
