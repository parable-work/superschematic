package sqlgen

import (
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// The triggers of search across tables (D155). Each @searchable table's
// rows have a row each in the database's search index, which a worker
// builds from the row, its conversation and its files, after a message on
// the declaration's queue names the row. Three triggers keep the index
// and the queue:
//
//   - SearchTrigger, on each @searchable table, after each insert, update
//     and delete of a row: a delete, a soft delete and a change of key
//     delete the index row in the writer's transaction, so a search never
//     shows a row that is gone; on an @audience table, a change of the
//     row's effective audience or agent access is copied into its index
//     row in the same transaction, so a search's access check, which reads
//     the index row, is never behind the row; and an insert, a restore and
//     an update that changes an indexed field enqueue the row.
//   - SearchChildTrigger, on the table's Comments messages sub-table and
//     its attachments sub-table when the declaration indexes them, after
//     each insert, delete and update of the text they hold, enqueues the
//     row they belong to.
//
// An enqueue skips a row that holds a ready message already (the queue's
// key, which the loader gives it, D80), so a row written many times before
// a worker claims it is rebuilt once. These are the Postgres triggers;
// searchable_sqlite.go writes SQLite's (D186).

// The suffix of the functions, after the table; each trigger's name is its
// function's after trg_.
const searchSuffix = "_search"

// SearchQueue is the queue a @searchable declaration names: its quoted
// table, its message's columns in ir.SearchIndexingFields' order, quoted,
// and the clause that ends an enqueue on it in each dialect. An @audience
// declaration's queue is one of the same shape, its columns in
// ir.AudienceResolveFields' order (#734).
type SearchQueue struct {
	Table          string
	Columns        []string
	Conflict       string
	SQLiteConflict string
}

// messageQueueOf is the queue class named name of schema, whose message
// holds fields, in their order.
func messageQueueOf(schema *ir.Schema, name string, fields []string) (SearchQueue, error) {
	td := schema.Types[name]
	if td == nil || td.Queue == nil {
		return SearchQueue{}, fmt.Errorf("sqlgen: %s is no queue of %s", name, schema.Name)
	}
	var columns []string
	for _, field := range fields {
		var fd *ir.FieldDef
		for _, f := range td.Fields {
			if f != nil && f.Name == field {
				fd = f
			}
		}
		if fd == nil {
			return SearchQueue{}, fmt.Errorf("sqlgen: queue %s has no field %s", name, field)
		}
		columns = append(columns, sqlutil.QuoteIdentifier(sqlutil.ColumnName(fd)))
	}
	table := sqlutil.QuoteIdentifier(QueueTableName(td))
	spec := sqlutil.QueueSpecOf(td, table, columns, true)
	return SearchQueue{Table: table, Columns: columns, Conflict: sqlutil.PostgresQueueStatements(spec).Conflict, SQLiteConflict: sqlutil.SQLiteQueueStatements(spec).Conflict}, nil
}

// SearchTrigger is the trigger of one @searchable table.
type SearchTrigger struct {
	// Origin is the table's type, which an index row and a message name.
	Origin string

	Table, QuotedTable string
	// Key is the table's key column, quoted.
	Key string
	// Watched are the columns of the indexed fields and the scope, quoted,
	// a change of which enqueues the row.
	Watched []string
	// DeletedAt is a soft-deleting table's deleted_at column, quoted;
	// empty for a table without one.
	DeletedAt string
	// Effective and EffectiveAgents are an @audience table's resolved
	// columns, quoted; empty for a table without an audience.
	Effective, EffectiveAgents string

	// Index is the index table, and its columns of the row's table and
	// key, its audience and its agent access, each quoted.
	Index, IndexTable, IndexKey, IndexAudience, IndexAgents string

	Queue SearchQueue

	// Environment is a table whose index rows are copied into the
	// environment's index (outputs.sql.search.environment, #740): a delete,
	// a soft delete, a change of key and every change of the row enqueue
	// it too, so its worker copies what changed.
	Environment bool

	FunctionName, TriggerName string
}

// SearchChildTrigger is the trigger of a @searchable table's Comments
// messages sub-table or attachments sub-table, whose text the row's index
// row holds.
type SearchChildTrigger struct {
	// Origin is the sub-table's type, and Row the @searchable table's.
	Origin, Row string

	Table, QuotedTable string
	// RowColumn is the sub-table's column of the row's key, quoted.
	RowColumn string
	// Watched are the columns an update of which enqueues the row, quoted.
	Watched []string

	Queue SearchQueue

	// Grant marks the trigger of the @accessGrants table of a database
	// whose index rows are copied into the environment's index (#740):
	// Row is the grants table's type, and RowColumn its key, so a write
	// of a grant enqueues the grant for its reach to be copied again.
	Grant bool

	FunctionName, TriggerName string
}

// buildSearchables builds the triggers of the schema's @searchable
// declarations from the tables as sqlgen built them, sorted by table.
func buildSearchables(schema *ir.Schema, tableTypes []*ir.TypeDef, tables []Table) ([]SearchTrigger, []SearchChildTrigger, error) {
	searchables := schema.SearchableTables()
	if len(searchables) == 0 {
		return nil, nil, nil
	}
	byType := map[string]*Table{}
	for i := range tables {
		if td := typeNamed(tableTypes, tables[i].OriginalName); td != nil && tables[i].Name == sqlutil.TableName(td) {
			byType[tables[i].OriginalName] = &tables[i]
		}
	}
	column := func(typeName, field string) (string, error) {
		table := byType[typeName]
		if table == nil {
			return "", fmt.Errorf("sqlgen: %s has no table", typeName)
		}
		for i := range table.Columns {
			if table.Columns[i].Origin == typeName+"."+field {
				return table.Columns[i].QuotedName, nil
			}
		}
		return "", fmt.Errorf("sqlgen: table %s has no column of field %s", table.Name, field)
	}
	index := byType[ir.SearchIndexTypeName]
	if index == nil {
		return nil, nil, fmt.Errorf("sqlgen: @searchable: the search index %s has no table", ir.SearchIndexTypeName)
	}
	indexColumns := map[string]string{}
	for _, field := range []string{ir.SearchSourceTableField, ir.SearchSourceKeyField, ir.SearchAudienceField, ir.SearchAgentsField} {
		c, err := column(ir.SearchIndexTypeName, field)
		if err != nil {
			return nil, nil, err
		}
		indexColumns[field] = c
	}
	queueOf := func(name string) (SearchQueue, error) {
		return messageQueueOf(schema, name, ir.SearchIndexingFields)
	}
	tooLong := func(td *ir.TypeDef, name string) error {
		if len(name) > sqlutil.PostgresIdentifierLimit {
			return fmt.Errorf("sqlgen: @searchable on %s: its trigger %s is %d bytes, over PostgreSQL's %d-byte limit; give the table a shorter name (D155)",
				td.Name, name, len(name), sqlutil.PostgresIdentifierLimit)
		}
		return nil
	}

	var triggers []SearchTrigger
	var children []SearchChildTrigger
	for _, td := range searchables {
		table := byType[td.Name]
		if table == nil || table.PrimaryKey == "" {
			return nil, nil, fmt.Errorf("sqlgen: @searchable on %s: %s has no table with one key", td.Name, td.Name)
		}
		queue, err := queueOf(td.Searchable.Queue)
		if err != nil {
			return nil, nil, err
		}
		t := SearchTrigger{
			Origin:        td.Name,
			Table:         table.Name,
			QuotedTable:   table.QuotedName,
			Key:           sqlutil.QuoteIdentifier(table.PrimaryKey),
			Index:         index.QuotedName,
			IndexTable:    indexColumns[ir.SearchSourceTableField],
			IndexKey:      indexColumns[ir.SearchSourceKeyField],
			IndexAudience: indexColumns[ir.SearchAudienceField],
			IndexAgents:   indexColumns[ir.SearchAgentsField],
			Queue:         queue,
			Environment:   schema.SearchesEnvironment(),
			FunctionName:  table.Name + searchSuffix,
			TriggerName:   "trg_" + table.Name + searchSuffix,
		}
		fields := td.Searchable.Fields()
		if td.Searchable.Scope != "" {
			fields = append(fields, td.Searchable.Scope)
		}
		for _, field := range fields {
			c, err := column(td.Name, field)
			if err != nil {
				// A relation's column is its key's: <field>_id.
				if c, err = column(td.Name, field+"Id"); err != nil {
					return nil, nil, err
				}
			}
			t.Watched = append(t.Watched, c)
		}
		if c, err := column(td.Name, sqlutil.AuditDeletedAt); err == nil {
			t.DeletedAt = c
		}
		if td.Audience != nil {
			if t.Effective, err = column(td.Name, ir.EffectiveAudienceField); err != nil {
				return nil, nil, err
			}
			if t.EffectiveAgents, err = column(td.Name, ir.EffectiveAgentsField); err != nil {
				return nil, nil, err
			}
		}
		if err := tooLong(td, t.TriggerName); err != nil {
			return nil, nil, err
		}
		triggers = append(triggers, t)

		// The sub-tables whose text the row's index row holds.
		type sub struct {
			typeName, rowField string
			watched            []string
		}
		var subs []sub
		if td.Searchable.IndexesComments() {
			for _, name := range sortedTypeNames(schema) {
				if m := schema.Types[name].BehaviorTable; m != nil && m.Row == td.Name && m.Behavior == "Comments" && m.Table == "messages" {
					subs = append(subs, sub{name, m.RowField, []string{"body", "state"}})
				}
			}
		}
		if td.Searchable.IndexesAttachments() && td.Attachments != nil {
			name := ir.AttachmentsTypeName(td.Name)
			if m := schema.Types[name]; m != nil && m.RowTable != nil {
				subs = append(subs, sub{name, m.RowTable.RowField, []string{ir.AttStateField, ir.AttExtractedField}})
			}
		}
		for _, s := range subs {
			subTable := byType[s.typeName]
			if subTable == nil {
				return nil, nil, fmt.Errorf("sqlgen: @searchable on %s: its sub-table %s has no table", td.Name, s.typeName)
			}
			c := SearchChildTrigger{
				Origin:       s.typeName,
				Row:          td.Name,
				Table:        subTable.Name,
				QuotedTable:  subTable.QuotedName,
				Queue:        queue,
				FunctionName: subTable.Name + searchSuffix,
				TriggerName:  "trg_" + subTable.Name + searchSuffix,
			}
			if c.RowColumn, err = column(s.typeName, s.rowField); err != nil {
				return nil, nil, err
			}
			for _, field := range s.watched {
				w, err := column(s.typeName, field)
				if err != nil {
					return nil, nil, err
				}
				c.Watched = append(c.Watched, w)
			}
			if err := tooLong(td, c.TriggerName); err != nil {
				return nil, nil, err
			}
			children = append(children, c)
		}
	}
	if grants := schema.AccessGrantsTable(); grants != nil && schema.SearchesEnvironment() {
		// A grant's write enqueues it on the first declaration's queue
		// (#740), whose worker copies its reach again.
		table := byType[grants.Name]
		if table == nil || table.PrimaryKey == "" {
			return nil, nil, fmt.Errorf("sqlgen: @accessGrants %s has no table with one key", grants.Name)
		}
		queue, err := queueOf(searchables[0].Searchable.Queue)
		if err != nil {
			return nil, nil, err
		}
		c := SearchChildTrigger{
			Origin:       grants.Name,
			Row:          grants.Name,
			Table:        table.Name,
			QuotedTable:  table.QuotedName,
			RowColumn:    sqlutil.QuoteIdentifier(table.PrimaryKey),
			Queue:        queue,
			Grant:        true,
			FunctionName: table.Name + searchSuffix,
			TriggerName:  "trg_" + table.Name + searchSuffix,
		}
		for _, field := range []string{ir.AccessGrantHolderField, ir.AccessGrantHolderKindField, ir.AccessGrantRoleField, ir.AccessGrantTargetTypeField, ir.AccessGrantTargetIDField,
			ir.AccessGrantStartsAtField, ir.AccessGrantEndsAtField, ir.AccessGrantEndsWhenFieldField, ir.AccessGrantEndsWhenStateField, ir.AccessGrantRevokedAtField} {
			w, err := column(grants.Name, field)
			if err != nil {
				return nil, nil, err
			}
			c.Watched = append(c.Watched, w)
		}
		if err := tooLong(grants, c.TriggerName); err != nil {
			return nil, nil, err
		}
		children = append(children, c)
	}
	slices.SortStableFunc(triggers, func(a, b SearchTrigger) int { return strings.Compare(a.Table, b.Table) })
	slices.SortStableFunc(children, func(a, b SearchChildTrigger) int { return strings.Compare(a.Table, b.Table) })
	return triggers, children, nil
}

func sortedTypeNames(schema *ir.Schema) []string {
	names := make([]string, 0, len(schema.Types))
	for name := range schema.Types {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// enqueue is the statement that enqueues the row whose key is key, as
// text, unless it holds a ready message.
func (q SearchQueue) enqueue(origin, key, indent string) string {
	conflict := q.Conflict
	if conflict != "" && !strings.HasPrefix(conflict, " ") && !strings.HasPrefix(conflict, "\n") {
		conflict = " " + conflict
	}
	return fmt.Sprintf("%sINSERT INTO %s (%s)\n%sVALUES (%s, %s::text)%s;\n", indent, q.Table, strings.Join(q.Columns, ", "), indent, sqlutil.QuoteLiteral(origin), key, conflict)
}

// Trigger is the row trigger that keeps the table's index rows.
func (t SearchTrigger) Trigger() Trigger {
	return Trigger{Name: t.TriggerName, Timing: "AFTER INSERT OR UPDATE OR DELETE", Table: t.Table, QuotedTable: t.QuotedTable, Function: t.FunctionName}
}

// FunctionSQL renders the table's function: the index row deleted with
// the row, its access columns copied, and the row enqueued when its
// indexed text changed.
func (t SearchTrigger) FunctionSQL() string {
	var b strings.Builder
	where := func(row string) string {
		return fmt.Sprintf("%s = %s AND %s = %s.%s::text", t.IndexTable, sqlutil.QuoteLiteral(t.Origin), t.IndexKey, row, t.Key)
	}
	fmt.Fprintf(&b, "BEGIN\n  -- The search index row of a row of %s (D155): deleted with the row,\n  -- its access copied in the writer's transaction, and the row enqueued\n  -- for its text to be indexed again when that changed.\n", t.Origin)
	if t.Environment {
		b.WriteString("  -- Its index row is copied into the environment's index (#740): a\n  -- delete, a soft delete, a change of key and every change of the row\n  -- enqueue it too, for its worker to copy what changed.\n")
	}
	// gone enqueues, for the environment's copy, a row whose index row a
	// branch deleted.
	gone := func(row, indent string) string {
		if !t.Environment {
			return ""
		}
		return t.Queue.enqueue(t.Origin, row+"."+t.Key, indent)
	}
	fmt.Fprintf(&b, "  IF TG_OP = 'DELETE' THEN\n    DELETE FROM %s WHERE %s;\n%s    RETURN NULL;\n  END IF;\n", t.Index, where("OLD"), gone("OLD", "    "))
	fmt.Fprintf(&b, "  IF TG_OP = 'UPDATE' AND OLD.%s IS DISTINCT FROM NEW.%s THEN\n    DELETE FROM %s WHERE %s;\n%s  END IF;\n", t.Key, t.Key, t.Index, where("OLD"), gone("OLD", "    "))
	if t.DeletedAt != "" {
		soft := ""
		if t.Environment {
			soft = fmt.Sprintf("    IF TG_OP = 'INSERT' OR OLD.%s IS NULL THEN\n%s    END IF;\n", t.DeletedAt, gone("NEW", "      "))
		}
		fmt.Fprintf(&b, "  IF NEW.%s IS NOT NULL THEN\n    DELETE FROM %s WHERE %s;\n%s    RETURN NULL;\n  END IF;\n", t.DeletedAt, t.Index, where("NEW"), soft)
	}
	if t.Effective != "" {
		fmt.Fprintf(&b, "  UPDATE %s SET %s = NEW.%s, %s = NEW.%s\n    WHERE %s\n      AND (%s IS DISTINCT FROM NEW.%s OR %s IS DISTINCT FROM NEW.%s);\n",
			t.Index, t.IndexAudience, t.Effective, t.IndexAgents, t.EffectiveAgents, where("NEW"), t.IndexAudience, t.Effective, t.IndexAgents, t.EffectiveAgents)
	}
	changed := []string{"TG_OP = 'INSERT'", fmt.Sprintf("OLD.%s IS DISTINCT FROM NEW.%s", t.Key, t.Key)}
	if t.DeletedAt != "" {
		changed = append(changed, fmt.Sprintf("OLD.%s IS NOT NULL", t.DeletedAt))
	}
	olds, news := make([]string, len(t.Watched)), make([]string, len(t.Watched))
	for i, c := range t.Watched {
		olds[i], news[i] = "OLD."+c, "NEW."+c
	}
	changed = append(changed, fmt.Sprintf("ROW(%s) IS DISTINCT FROM ROW(%s)", strings.Join(olds, ", "), strings.Join(news, ", ")))
	if t.Environment {
		changed = []string{"TG_OP = 'INSERT'", "ROW(OLD.*) IS DISTINCT FROM ROW(NEW.*)"}
	}
	fmt.Fprintf(&b, "  IF %s THEN\n", strings.Join(changed, "\n     OR "))
	b.WriteString(t.Queue.enqueue(t.Origin, "NEW."+t.Key, "    "))
	b.WriteString("  END IF;\n  RETURN NULL;\nEND;\n")
	body := b.String()
	tag := dollarQuote(body)
	return fmt.Sprintf("CREATE OR REPLACE FUNCTION %s() RETURNS trigger AS %s\n%s%s LANGUAGE plpgsql", sqlutil.QuoteIdentifier(t.FunctionName), tag, body, tag)
}

// Trigger is the row trigger that enqueues the row a sub-table's write
// changes the text of.
func (c SearchChildTrigger) Trigger() Trigger {
	return Trigger{Name: c.TriggerName, Timing: "AFTER INSERT OR UPDATE OR DELETE", Table: c.Table, QuotedTable: c.QuotedTable, Function: c.FunctionName}
}

// FunctionSQL renders the sub-table's function: the row its row belongs
// to enqueued on an insert, a delete, and an update of the text.
func (c SearchChildTrigger) FunctionSQL() string {
	var b strings.Builder
	if c.Grant {
		fmt.Fprintf(&b, "BEGIN\n  -- The grant a write of %s changed (#740), enqueued for its reach\n  -- to be copied into the environment's index again.\n", c.Origin)
	} else {
		fmt.Fprintf(&b, "BEGIN\n  -- The row of %s whose text the write of %s changed (D155),\n  -- enqueued for its index row to be built again.\n", c.Row, c.Origin)
	}
	fmt.Fprintf(&b, "  IF TG_OP = 'DELETE' THEN\n%s    RETURN NULL;\n  END IF;\n", c.Queue.enqueue(c.Row, "OLD."+c.RowColumn, "    "))
	olds, news := make([]string, len(c.Watched)), make([]string, len(c.Watched))
	for i, w := range c.Watched {
		olds[i], news[i] = "OLD."+w, "NEW."+w
	}
	fmt.Fprintf(&b, "  IF TG_OP = 'INSERT' OR ROW(%s) IS DISTINCT FROM ROW(%s) THEN\n", strings.Join(olds, ", "), strings.Join(news, ", "))
	b.WriteString(c.Queue.enqueue(c.Row, "NEW."+c.RowColumn, "    "))
	fmt.Fprintf(&b, "  END IF;\n  IF TG_OP = 'UPDATE' AND OLD.%s IS DISTINCT FROM NEW.%s THEN\n", c.RowColumn, c.RowColumn)
	b.WriteString(c.Queue.enqueue(c.Row, "OLD."+c.RowColumn, "    "))
	b.WriteString("  END IF;\n  RETURN NULL;\nEND;\n")
	body := b.String()
	tag := dollarQuote(body)
	return fmt.Sprintf("CREATE OR REPLACE FUNCTION %s() RETURNS trigger AS %s\n%s%s LANGUAGE plpgsql", sqlutil.QuoteIdentifier(c.FunctionName), tag, body, tag)
}
