package sqlmigrate

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
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
// trigger per write, which holds its insert. An @optimistic table's bump,
// which a table whose fields are behaviors takes (D103), is an AFTER UPDATE
// trigger that gives the row it updated the next version, as Postgres's
// BEFORE UPDATE trigger does (sqliteBumpTrigger). A @stateMachine's guard
// (D118) is one trigger whose WHEN is the machine's rule and whose body
// fails the write, and a @fenced counter's (D119) is one of the same form.
// A @versioned table's history is a table its row triggers fill (D189,
// sqlite_history.go). The model refuses what SQLite has no form of (D27),
// naming the feature: a history's retention, pins and partitions,
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
	searchIndex := ""
	for i := range out.Tables {
		types[out.Tables[i].Name] = out.Tables[i].OriginalName
		if out.Tables[i].OriginalName == ir.SearchIndexTypeName {
			// The search index's words and audience have GIN indexes on
			// Postgres; on SQLite its words are its FTS5 table, and its
			// audience a JSON array a search reads with json_each (D186).
			searchIndex = out.Tables[i].Name
			out.Tables[i].Indexes = slices.DeleteFunc(out.Tables[i].Indexes, func(idx sqlgen.Index) bool { return idx.Type == "GIN" })
		}
		if td := schema.Types[out.Tables[i].OriginalName]; td != nil && td.Audience != nil {
			// An @audience table's effective audience has a GIN index on
			// Postgres; on SQLite it is a JSON array a read takes apart
			// with json_each, which no index serves (D182).
			effective := sqlutil.QuoteIdentifier(codegen.ToSnakeCase(ir.EffectiveAudienceField))
			out.Tables[i].Indexes = slices.DeleteFunc(out.Tables[i].Indexes, func(idx sqlgen.Index) bool {
				return idx.Type == "GIN" && slices.Equal(idx.Columns, []string{effective})
			})
		}
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
	// A @versioned table's history and the triggers that keep it (D189).
	for _, h := range out.HistoryTables {
		var source *Table
		for _, t := range m.Tables {
			if t.Name == h.SourceTableName {
				source = t
			}
		}
		if source == nil {
			return nil, fmt.Errorf("sqlmigrate: the history %s has no table %s", h.Name, h.SourceTableName)
		}
		source.History = h.Name
		source.HistoryExclude = append([]string(nil), h.ExcludedColumns...)
		history, err := sqliteHistoryTable(h, types[h.SourceTableName], ns)
		if err != nil {
			return nil, err
		}
		m.Tables = append(m.Tables, history)
		m.Triggers = append(m.Triggers, sqliteHistoryTriggers(h, source)...)
	}
	for _, o := range out.OptimisticTables {
		m.Triggers = append(m.Triggers, &Trigger{Name: o.TriggerName, Table: o.TableName, Definition: sqliteBumpTrigger(o), Origin: types[o.TableName]})
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
	// A hearing table's change trigger (D103): one trigger per write, two
	// when its image is empty, with no backfill.
	for _, c := range out.ChangeTriggers {
		for _, t := range c.SQLiteTriggers(qs) {
			m.Triggers = append(m.Triggers, &Trigger{Name: t.Name, Table: t.Table, Definition: t.Definition, Origin: c.Origin})
		}
	}
	// A @stateMachine's guard (D118): one trigger, which holds the rule and
	// refuses a move the machine does not list.
	for _, g := range out.StateGuards {
		t := g.SQLiteTrigger(qs)
		m.Triggers = append(m.Triggers, &Trigger{Name: t.Name, Table: t.Table, Definition: t.Definition, Origin: g.Origin, Refuses: t.Refuses})
	}
	// A @fenced counter (D119): one trigger, which holds the rule and
	// refuses an update that does not advance the fence.
	for _, f := range out.Fences {
		t := f.SQLiteTrigger(qs)
		m.Triggers = append(m.Triggers, &Trigger{Name: t.Name, Table: t.Table, Definition: t.Definition, Origin: f.Origin, Refuses: t.Refuses})
	}
	// An @audience table's insert and update triggers, which refuse an
	// audience they do not know and resolve the row's, and a parent's
	// cascade (D156, D182). The update's trigger resolves a table's rows
	// once when it is created on one that holds rows.
	for _, a := range out.AudienceTriggers {
		for _, t := range a.SQLiteTriggers(qs) {
			trigger := &Trigger{Name: t.Name, Table: t.Table, Definition: t.Definition, Origin: a.Origin, Refuses: t.Refuses}
			if strings.HasSuffix(t.Name, "_update") {
				trigger.Backfill = a.SQLiteBackfillSQL(qs)
				trigger.BackfillNote = fmt.Sprintf("Backfilling %s resolves the audience of every row of %s.", t.Name, a.Table)
			}
			m.Triggers = append(m.Triggers, trigger)
		}
	}
	for _, c := range out.AudienceCascades {
		t, err := c.SQLiteTrigger(out.AudienceTriggers, qs)
		if err != nil {
			return nil, err
		}
		m.Triggers = append(m.Triggers, &Trigger{Name: t.Name, Table: t.Table, Definition: t.Definition, Origin: c.Origin})
	}
	// Search's row triggers (D155, D186): each @searchable table's after
	// each write, which delete its index row with it, copy its audience and
	// enqueue it, and its sub-tables', which enqueue the row they belong to.
	for _, s := range out.SearchTriggers {
		for _, t := range s.SQLiteTriggers(qs) {
			m.Triggers = append(m.Triggers, &Trigger{Name: t.Name, Table: t.Table, Definition: t.Definition, Origin: s.Origin})
		}
	}
	for _, c := range out.SearchChildTriggers {
		for _, t := range c.SQLiteTriggers(qs) {
			m.Triggers = append(m.Triggers, &Trigger{Name: t.Name, Table: t.Table, Definition: t.Definition, Origin: c.Origin})
		}
	}
	// References' and attachments' row triggers (D154, D182): a target's
	// clear and a file's release.
	for _, c := range out.ReferencesClears {
		t := c.SQLiteTrigger(qs)
		m.Triggers = append(m.Triggers, &Trigger{Name: t.Name, Table: t.Table, Definition: t.Definition, Origin: c.Origin})
	}
	for _, r := range out.AttachmentsReleases {
		t := r.SQLiteTrigger(qs)
		m.Triggers = append(m.Triggers, &Trigger{Name: t.Name, Table: t.Table, Definition: t.Definition, Origin: r.Origin})
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
	if searchIndex != "" {
		m.RawSQL = append(m.RawSQL, searchFTSPiece(searchIndex, schema.SearchConfig() == ir.SearchFoldedConfig))
	}
	for _, s := range out.RoleSeeds {
		m.Roles = append(m.Roles, modelRole(s, s.SQLiteInsertSQL(qs), s.SQLiteUpdateSQL(qs)))
	}
	for _, s := range out.SeedRows {
		m.Rows = append(m.Rows, &SeedRow{Table: s.Table, Key: s.Key, Origin: s.Origin, Insert: s.SQLiteInsertSQL(qs)})
	}
	m.sort()
	return m, nil
}

// SearchFTSPiece is the id of the piece of SQL that makes the search
// index's FTS5 table on SQLite (D186).
const SearchFTSPiece = "search_index_fts"

// searchFTSPiece is the search index's words on SQLite (D186): its FTS5
// table and the triggers on the index table that keep it, which the model
// carries as a piece of SQL of its own, after every object, since it has
// no form of a virtual table: a file that holds them reads back as having
// run it, and a plan to a schema that adds the index runs it. Its down
// drops them. A table made @searchable with rows needs reindexSearch once,
// as on Postgres. fold removes diacritics (#740).
func searchFTSPiece(index string, fold bool) *RawSQL {
	up, down := sqlgen.SearchFTS(index, qs, fold)
	return &RawSQL{ID: SearchFTSPiece, Origin: ir.SearchIndexTypeName, Position: ir.RawSQLAfterIndexes, Statements: up, Down: down, Checksum: sqlutil.StatementsChecksum(up)}
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
			if idx.NullsNotDistinct {
				refuse("the index %s of %s makes nulls collide (nullsNotDistinct)", idx.Name, t.OriginalName)
			}
		}
	}
	for _, h := range out.HistoryTables {
		sqliteHistoryRefusals(h, types[h.SourceTableName], refuse)
	}
	for _, view := range out.Projections {
		refuse("%s is a projection (%s.%s)", view.TypeName, view.Pool, view.Name)
	}
	return errors.Join(errs...)
}

// sqliteBumpTrigger is an @optimistic table's version bump on SQLite, whose
// triggers cannot set NEW: after an update that leaves _version as it was,
// it sets the row's _version to the next one, so a write fenced on the
// version a read saw changes no row once another write has moved it. An
// update that sets _version itself keeps what it set, and the trigger's
// own update, which moves it, does not fire it again.
func sqliteBumpTrigger(o sqlgen.OptimisticTable) string {
	table := qs(o.TableName)
	return "CREATE TRIGGER " + qs(o.TriggerName) + " AFTER UPDATE ON " + table +
		" FOR EACH ROW WHEN NEW._version = OLD._version BEGIN UPDATE " + table +
		" SET _version = OLD._version + 1 WHERE rowid = NEW.rowid; END"
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
