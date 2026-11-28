package sqlgen

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// A table's @enqueueOnChange (D80): a trigger that, after a write to the
// table's rows, inserts a message into a queue's table, whatever wrote the
// row. On Postgres it is a plpgsql function and the trigger that executes
// it, as a @versioned table's capture function and triggers are, so
// create.sql and the migration planner's model (internal/sqlmigrate) take
// them from here alike and the planner adds, replaces and drops them as
// it does those. SQLite has no trigger functions: its triggers hold their
// statements, one trigger per write. The insert ends with the queue's
// Conflict (sqlutil.QueueStatements), so under replace or skip repeated
// writes keep one ready message per key.

// EnqueueTrigger is a table's @enqueueOnChange of one queue.
type EnqueueTrigger struct {
	// Origin is the table's type, and Queue the @queue class.
	Origin string
	Queue  string

	Table, QuotedTable           string
	QueueTable, QuotedQueueTable string

	// FunctionName is the Postgres trigger function, and TriggerName the
	// Postgres trigger; SQLite's triggers add the write to the name.
	FunctionName string
	TriggerName  string

	// On are the writes that enqueue, in ir.EnqueueEvents order.
	On []string

	// Fields are the quoted columns an update compares; none enqueues on
	// every update.
	Fields []string

	// When are the conditions on the row, each with the row's column.
	When []EnqueueWhen

	// Columns are the quoted queue columns the insert sets, in the
	// message's field order, and Values what each takes: a quoted column
	// of the row, or a literal where Literal says so (an enum's schema
	// default for a field the message leaves out). Where Through holds a
	// value, the row's column is a relation's key, and the value is the
	// column of the row it names.
	Columns []string
	Values  []string
	Literal []bool
	Through []*EnqueueThrough

	// Conflict and SQLiteConflict end the insert in each dialect; empty
	// for a queue that appends.
	Conflict       string
	SQLiteConflict string

	// Backfill is the declaration's backfill (D88): the migration step
	// that creates the trigger on a table with rows also runs
	// BackfillSQL, or SQLiteBackfillSQL.
	Backfill bool
	// SourceKey is the table's quoted primary key column.
	SourceKey string
	// ReplaceKeys are, on a queue that replaces the ready message of a
	// key, the index in Columns of each column of the message's key, in
	// the key's order, -1 for one the message leaves to its column's
	// default; nil for any other queue.
	ReplaceKeys []int
}

// EnqueueThrough is a message's value read through a to-one relation of
// the row: the quoted column of the related table's row that the row's
// key column names, by that table's quoted key. A version graph's release
// pointer takes its commit's sequence so (D138); the loader's expansion
// writes such a value as <relation>.<field>, and a declaration of the
// schema's own names the row's fields alone.
type EnqueueThrough struct {
	Table  string
	Key    string
	Column string
}

// value is the subquery that reads the related row's column, for the row's
// key column at col, its identifiers quoted by quote.
func (v *EnqueueThrough) value(col string, quote func(string) string) string {
	return "(SELECT " + quote(v.Column) + " FROM " + quote(v.Table) + " WHERE " + quote(v.Key) + " = " + col + ")"
}

// EnqueueWhen is one condition of an EnqueueTrigger on the row's quoted
// column: IS NULL, IS NOT NULL, or = a literal, written per dialect.
type EnqueueWhen struct {
	Column  string
	IsNull  bool
	NotNull bool
	Equals  any
}

// buildEnqueueTriggers builds each @enqueueOnChange of the schema's tables
// from the tables and the queues' tables as sqlgen built them, sorted by
// table, then queue.
func buildEnqueueTriggers(tableTypes []*ir.TypeDef, tables []Table, schema *ir.Schema) ([]EnqueueTrigger, error) {
	byName := map[string]*Table{}
	for i := range tables {
		byName[tables[i].OriginalName+"/"+tables[i].Name] = &tables[i]
	}
	var out []EnqueueTrigger
	for _, td := range tableTypes {
		for _, def := range td.EnqueueOnChange {
			source := byName[td.Name+"/"+sqlutil.TableName(td)]
			queueDef := schema.Types[def.Queue]
			if source == nil || queueDef == nil || queueDef.Queue == nil {
				return nil, fmt.Errorf("@enqueueOnChange(%s) on %s: no table of %s or no queue %s", def.Queue, td.Name, td.Name, def.Queue)
			}
			queue := byName[queueDef.Name+"/"+QueueTableName(queueDef)]
			if queue == nil {
				return nil, fmt.Errorf("@enqueueOnChange(%s) on %s: queue %s has no table", def.Queue, td.Name, def.Queue)
			}
			trigger, err := enqueueTrigger(td, def, source, queueDef, queue, schema, byName)
			if err != nil {
				return nil, fmt.Errorf("@enqueueOnChange(%s) on %s: %w", def.Queue, td.Name, err)
			}
			out = append(out, trigger)
		}
	}
	slices.SortStableFunc(out, func(a, b EnqueueTrigger) int {
		return strings.Compare(a.Table+"/"+a.Queue, b.Table+"/"+b.Queue)
	})
	return out, nil
}

func enqueueTrigger(td *ir.TypeDef, def *ir.EnqueueOnChange, source *Table, queueDef *ir.TypeDef, queue *Table, schema *ir.Schema, tables map[string]*Table) (EnqueueTrigger, error) {
	// The names follow the tables' (sqlutil.TableName, QueueTableName):
	// the queue's is its table's without the suffix.
	base := strings.TrimSuffix(queue.Name, ir.QueueTableSuffix)
	t := EnqueueTrigger{
		Origin:           td.Name,
		Queue:            queueDef.Name,
		Table:            source.Name,
		QuotedTable:      source.QuotedName,
		QueueTable:       queue.Name,
		QuotedQueueTable: queue.QuotedName,
		FunctionName:     source.Name + "_enqueue_" + base,
		TriggerName:      "trg_" + source.Name + "_enqueue_" + base,
		On:               append([]string(nil), def.On...),
	}
	for _, name := range []string{t.FunctionName, t.TriggerName + "_" + ir.EnqueueOnInsert, t.TriggerName + "_" + ir.EnqueueOnUpdate, t.TriggerName + "_" + ir.EnqueueOnDelete} {
		if len(name) > sqlutil.PostgresIdentifierLimit {
			return t, fmt.Errorf("its trigger %s is %d bytes, over PostgreSQL's %d-byte limit; give the table or the queue a shorter name", name, len(name), sqlutil.PostgresIdentifierLimit)
		}
	}
	column := func(field string) (*Column, error) {
		for i := range source.Columns {
			c := &source.Columns[i]
			if c.Origin == td.Name+"."+field || (c.Origin == "" && c.Name == field && field == "id") {
				return c, nil
			}
		}
		return nil, fmt.Errorf("%s has no column of field %s", td.Name, field)
	}
	for _, field := range def.Fields {
		c, err := column(field)
		if err != nil {
			return t, err
		}
		t.Fields = append(t.Fields, c.QuotedName)
	}
	for _, cond := range def.When {
		c, err := column(cond.Field)
		if err != nil {
			return t, err
		}
		t.When = append(t.When, EnqueueWhen{Column: c.QuotedName, IsNull: cond.IsNull, NotNull: cond.NotNull, Equals: cond.Equals})
	}
	columns := make([]string, 0, len(queueDef.Fields))
	for _, f := range queueDef.Fields {
		if f == nil {
			continue
		}
		qc := queueColumnOf(queue, queueDef.Name, f.Name)
		if qc == nil {
			return t, fmt.Errorf("queue %s has no column of field %s", queueDef.Name, f.Name)
		}
		columns = append(columns, qc.QuotedName)
		if from, mapped := def.Message[f.Name]; mapped {
			relation, field, through := strings.Cut(from, ".")
			c, err := column(relation)
			if err != nil {
				return t, err
			}
			var via *EnqueueThrough
			if through {
				if via, err = enqueueThrough(td, relation, field, schema, tables); err != nil {
					return t, err
				}
			}
			if via != nil {
				// Through holds a value, at its index, only once one is read
				// through a relation.
				t.Through = append(t.Through, make([]*EnqueueThrough, len(t.Values)-len(t.Through))...)
				t.Through = append(t.Through, via)
			}
			t.Columns, t.Values, t.Literal = append(t.Columns, qc.QuotedName), append(t.Values, c.QuotedName), append(t.Literal, false)
			continue
		}
		// An enum the message leaves out takes its schema default, as an
		// enqueue writes it; any other column its own default, which
		// verification made sure every required one has.
		if f.Default != nil && schema.Enums[f.TypeRef.Name] != nil && f.Required && !f.TypeRef.IsArray {
			t.Columns, t.Values, t.Literal = append(t.Columns, qc.QuotedName), append(t.Values, sqlutil.QuoteLiteral(*f.Default)), append(t.Literal, true)
			continue
		}
		if !qc.Nullable && qc.DefaultValue == "" {
			return t, fmt.Errorf("queue %s's field %s is required and its column has no default, so the message maps it", queueDef.Name, f.Name)
		}
	}
	spec := sqlutil.QueueSpecOf(queueDef, queue.QuotedName, columns, true)
	t.Conflict = sqlutil.PostgresQueueStatements(spec).Conflict
	t.SQLiteConflict = sqlutil.SQLiteQueueStatements(spec).Conflict
	t.Backfill, t.SourceKey, t.ReplaceKeys = def.Backfill, source.PrimaryKey, nil
	if q := queueDef.Queue; q.HasKey() && q.OnDuplicate == ir.QueueOnDuplicateReplace {
		t.ReplaceKeys = []int{}
		for _, field := range q.KeyFields() {
			at := -1
			if key := queueColumnOf(queue, queueDef.Name, field); key != nil {
				at = slices.Index(t.Columns, key.QuotedName)
			}
			t.ReplaceKeys = append(t.ReplaceKeys, at)
		}
	}
	return t, nil
}

// enqueueThrough is the value of field read through relation, a to-one
// relation of td: the related table's column of the field, by its key.
func enqueueThrough(td *ir.TypeDef, relation, field string, schema *ir.Schema, tables map[string]*Table) (*EnqueueThrough, error) {
	var rel *ir.FieldDef
	for _, f := range td.Fields {
		if f != nil && f.Name == relation {
			rel = f
		}
	}
	target := (*ir.TypeDef)(nil)
	if rel != nil && !rel.TypeRef.IsArray && !rel.TypeRef.IsMap {
		target = schema.Types[rel.TypeRef.Name]
	}
	if target == nil || target.Role != ir.RoleDBTable {
		return nil, fmt.Errorf("the message reads %s.%s, and %s is no to-one relation of %s to a table", relation, field, relation, td.Name)
	}
	table := tables[target.Name+"/"+sqlutil.TableName(target)]
	if table == nil || table.PrimaryKey == "" {
		return nil, fmt.Errorf("the message reads %s.%s, and %s has no table with a key", relation, field, target.Name)
	}
	for _, c := range table.Columns {
		if c.Origin == target.Name+"."+field {
			return &EnqueueThrough{Table: table.QuotedName, Key: table.PrimaryKey, Column: c.QuotedName}, nil
		}
	}
	return nil, fmt.Errorf("the message reads %s.%s, and %s has no column of field %s", relation, field, target.Name, field)
}

// BackfillSQL is the backfill's statement on Postgres (D88): one insert
// of a message for each row of the table that meets the conditions, as
// the trigger's insert of the row would write it, ending with the
// queue's Conflict. Under replace it takes one row of each key, the first
// by the table's key, since one statement's ON CONFLICT DO UPDATE changes
// a row once; a message keyed by the table's own key needs none.
func (t EnqueueTrigger) BackfillSQL() string {
	identity := func(c string) string { return c }
	return t.backfillSQL(identity, t.QuotedTable, t.QuotedQueueTable, "TRUE", "FALSE", t.Conflict, perKeyDistinctOn)
}

// SQLiteBackfillSQL is the backfill's statement on SQLite, its
// identifiers quoted by quote. SQLite's upsert changes a row as often as
// the select returns its key, so under replace a key's last row wins. The
// select always has a WHERE, which SQLite needs to read the ON CONFLICT
// that follows it as the upsert's.
func (t EnqueueTrigger) SQLiteBackfillSQL(quote func(string) string) string {
	unquote := func(c string) string { return quote(sqlutil.Unquote(c)) }
	return t.backfillSQL(unquote, quote(t.Table), quote(t.QueueTable), "1", "0", t.SQLiteConflict, perKeyAll)
}

// SQLiteQueueBackfillSQL is the backfill `stack queue backfill` runs on
// SQLite and D1 (D88): SQLiteBackfillSQL's, but under replace it takes one
// row of each key, the first by the table's key, as Postgres's does, by a
// window over the rows that meet the conditions, so the command enqueues on
// SQLite the messages it enqueues on Postgres and counts each key once.
// The plan's backfill keeps SQLiteBackfillSQL, which the SQLite model
// holds.
func (t EnqueueTrigger) SQLiteQueueBackfillSQL(quote func(string) string) string {
	unquote := func(c string) string { return quote(sqlutil.Unquote(c)) }
	return t.backfillSQL(unquote, quote(t.Table), quote(t.QueueTable), "1", "0", t.SQLiteConflict, perKeyWindow)
}

// How a backfill under replace takes the rows of one key (backfillSQL).
type perKey int

const (
	// perKeyAll takes every row, so SQLite's upsert keeps a key's last.
	perKeyAll perKey = iota
	// perKeyDistinctOn takes the first by the table's key with Postgres's
	// DISTINCT ON.
	perKeyDistinctOn
	// perKeyWindow takes the first by the table's key with row_number().
	perKeyWindow
)

// backfillRow is the column perKeyWindow numbers each key's rows in.
const backfillRow = "superschematic_backfill_row"

// backfillSQL renders a backfill: quote quotes a column, table and queue
// are quoted, yes and no are the dialect's booleans, and per says how it
// takes the rows of one key under replace.
func (t EnqueueTrigger) backfillSQL(quote func(string) string, table, queue, yes, no, conflict string, per perKey) string {
	const row = "src"
	columns, values := t.columnsAndValues(row, quote)
	conds := []string{yes}
	if len(t.When) > 0 {
		conds = conds[:0]
		for _, w := range t.When {
			conds = append(conds, w.sql(row+"."+quote(w.Column), yes, no))
		}
	}
	selectList := strings.Join(values, ", ")
	order := ""
	// The values the trigger gives the message's key, of the key's
	// columns it sets, and whether one of them is the table's own key.
	var keyed []string
	own := false
	for _, at := range t.ReplaceKeys {
		if at >= 0 {
			keyed = append(keyed, values[at])
			own = own || values[at] == row+"."+quote(t.SourceKey)
		}
	}
	from, where := table+" AS "+row, strings.Join(conds, " AND ")
	switch {
	case per == perKeyAll || t.ReplaceKeys == nil:
	case own:
		// The table's key is of the message's: each row is its own key.
	case len(keyed) == 0:
		order = "\nORDER BY " + row + "." + quote(t.SourceKey) + "\nLIMIT 1"
	case per == perKeyWindow:
		// The rows that meet the conditions, numbered within each key by
		// the table's key, and the first of each.
		key := strings.Join(keyed, ", ")
		from = "(SELECT " + row + ".*, row_number() OVER (PARTITION BY " + key + " ORDER BY " + row + "." + quote(t.SourceKey) + ") AS " + quote(backfillRow) +
			" FROM " + table + " AS " + row + " WHERE " + where + ") AS " + row
		where = row + "." + quote(backfillRow) + " = 1"
	default:
		key := strings.Join(keyed, ", ")
		selectList = "DISTINCT ON (" + key + ") " + selectList
		order = "\nORDER BY " + key + ", " + row + "." + quote(t.SourceKey)
	}
	s := "INSERT INTO " + queue + " (" + strings.Join(columns, ", ") + ")\nSELECT " + selectList +
		"\nFROM " + from + "\nWHERE " + where + order
	if conflict != "" {
		s += "\n" + conflict
	}
	return s
}

// queueColumnOf is the column of a queue's table that stores the message's
// field.
func queueColumnOf(queue *Table, queueName, field string) *Column {
	for i := range queue.Columns {
		if queue.Columns[i].Origin == queueName+"."+field {
			return &queue.Columns[i]
		}
	}
	return nil
}

// Trigger is the Postgres trigger that executes the function after each
// write the declaration takes.
func (t EnqueueTrigger) Trigger() Trigger {
	events := make([]string, len(t.On))
	for i, event := range t.On {
		events[i] = strings.ToUpper(event)
	}
	return Trigger{
		Name:        t.TriggerName,
		Timing:      "AFTER " + strings.Join(events, " OR "),
		Table:       t.Table,
		QuotedTable: t.QuotedTable,
		Function:    t.FunctionName,
	}
}

// FunctionSQL renders the Postgres trigger function: an update that
// changes none of the fields returns at once, and a row that meets the
// conditions inserts the message, from the row as written or, for a
// delete, as it was.
func (t EnqueueTrigger) FunctionSQL() string {
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE OR REPLACE FUNCTION %s() RETURNS trigger AS $$\nBEGIN\n", sqlutil.QuoteIdentifier(t.FunctionName))
	fmt.Fprintf(&b, "  -- Enqueues a message of %s after a write to %s (D80).\n", t.Queue, t.Table)
	if len(t.Fields) > 0 {
		fmt.Fprintf(&b, "  IF TG_OP = 'UPDATE' AND NOT (%s) THEN\n    RETURN NULL;\n  END IF;\n", t.changed("IS DISTINCT FROM"))
	}
	written := slices.Contains(t.On, ir.EnqueueOnInsert) || slices.Contains(t.On, ir.EnqueueOnUpdate)
	if slices.Contains(t.On, ir.EnqueueOnDelete) {
		if !written {
			b.WriteString(t.pgInsert("OLD", "  "))
			b.WriteString("  RETURN NULL;\nEND;\n$$ LANGUAGE plpgsql")
			return b.String()
		}
		b.WriteString("  IF TG_OP = 'DELETE' THEN\n")
		b.WriteString(t.pgInsert("OLD", "    "))
		b.WriteString("    RETURN NULL;\n  END IF;\n")
	}
	b.WriteString(t.pgInsert("NEW", "  "))
	b.WriteString("  RETURN NULL;\nEND;\n$$ LANGUAGE plpgsql")
	return b.String()
}

// pgInsert is the function's insert from row, NEW or OLD, under its
// conditions, at indent.
func (t EnqueueTrigger) pgInsert(row, indent string) string {
	insert := t.insert(row, t.QuotedQueueTable, func(c string) string { return c }, t.Conflict) + ";"
	if len(t.When) == 0 {
		return indentLines(insert, indent) + "\n"
	}
	var conds []string
	for _, w := range t.When {
		conds = append(conds, w.sql(row+"."+w.Column, "TRUE", "FALSE"))
	}
	return indent + "IF " + strings.Join(conds, " AND ") + " THEN\n" + indentLines(insert, indent+"  ") + "\n" + indent + "END IF;\n"
}

// indentLines puts indent before each line of s.
func indentLines(s, indent string) string {
	return indent + strings.ReplaceAll(s, "\n", "\n"+indent)
}

// insert is the INSERT of the message from row, its identifiers quoted by
// quote, ended on a line of its own with conflict.
func (t EnqueueTrigger) insert(row, table string, quote func(string) string, conflict string) string {
	columns, values := t.columnsAndValues(row, quote)
	s := "INSERT INTO " + table + " (" + strings.Join(columns, ", ") + ") VALUES (" + strings.Join(values, ", ") + ")"
	if conflict != "" {
		s += "\n  " + conflict
	}
	return s
}

// columnsAndValues are the queue columns the insert sets and the value of
// each from row, its identifiers quoted by quote: a literal as it is, a
// column of the row, and a value through a relation as the subquery that
// reads the related row's column.
func (t EnqueueTrigger) columnsAndValues(row string, quote func(string) string) ([]string, []string) {
	columns := make([]string, len(t.Columns))
	values := make([]string, len(t.Values))
	for i := range t.Columns {
		columns[i] = quote(t.Columns[i])
		values[i] = t.Values[i]
		if t.Literal[i] {
			continue
		}
		values[i] = row + "." + quote(t.Values[i])
		if i < len(t.Through) && t.Through[i] != nil {
			values[i] = t.Through[i].value(values[i], quote)
		}
	}
	return columns, values
}

// changed is true of an update that changes one of the fields, compared
// with distinct, the dialect's null-safe inequality.
func (t EnqueueTrigger) changed(distinct string) string {
	return t.changedQuoted(distinct, func(c string) string { return c })
}

func (t EnqueueTrigger) changedQuoted(distinct string, quote func(string) string) string {
	parts := make([]string, len(t.Fields))
	for i, f := range t.Fields {
		parts[i] = "OLD." + quote(f) + " " + distinct + " NEW." + quote(f)
	}
	return strings.Join(parts, " OR ")
}

// sql renders the condition on the column expression col, with the
// dialect's booleans.
func (w EnqueueWhen) sql(col, yes, no string) string {
	switch {
	case w.IsNull:
		return col + " IS NULL"
	case w.NotNull:
		return col + " IS NOT NULL"
	}
	switch v := w.Equals.(type) {
	case bool:
		if v {
			return col + " = " + yes
		}
		return col + " = " + no
	case float64:
		return col + " = " + strconv.FormatFloat(v, 'f', -1, 64)
	case string:
		return col + " = " + sqlutil.QuoteLiteral(v)
	}
	return col + " = " + sqlutil.QuoteLiteral(fmt.Sprint(w.Equals))
}

// SQLiteTrigger is a trigger on SQLite, which holds its statements: one of
// an EnqueueTrigger's or a ChangeTrigger's, a StateGuard's or a Fence's.
type SQLiteTrigger struct {
	Name       string
	Table      string
	Definition string
	// Refuses is set on a trigger that fails a write it does not allow
	// (Trigger.Refuses).
	Refuses bool
}

// SQLiteTriggers renders the declaration's triggers on SQLite, one per
// write, in ir.EnqueueEvents order: each holds its insert, and its WHEN the
// conditions and, for an update, the comparison of the fields with IS NOT,
// SQLite's null-safe inequality. quote quotes an identifier as the SQLite
// dialect does.
func (t EnqueueTrigger) SQLiteTriggers(quote func(string) string) []SQLiteTrigger {
	unquote := func(c string) string { return quote(sqlutil.Unquote(c)) }
	var out []SQLiteTrigger
	for _, event := range t.On {
		row := "NEW"
		if event == ir.EnqueueOnDelete {
			row = "OLD"
		}
		var when []string
		if event == ir.EnqueueOnUpdate && len(t.Fields) > 0 {
			when = append(when, "("+t.changedQuoted("IS NOT", unquote)+")")
		}
		for _, w := range t.When {
			when = append(when, w.sql(row+"."+unquote(w.Column), "1", "0"))
		}
		name := t.TriggerName + "_" + event
		var b strings.Builder
		fmt.Fprintf(&b, "CREATE TRIGGER %s AFTER %s ON %s FOR EACH ROW\n", quote(name), strings.ToUpper(event), quote(t.Table))
		if len(when) > 0 {
			fmt.Fprintf(&b, "WHEN %s\n", strings.Join(when, " AND "))
		}
		insert := t.insert(row, quote(t.QueueTable), unquote, t.SQLiteConflict)
		fmt.Fprintf(&b, "BEGIN\n%s;\nEND", indentLines(insert, "  "))
		out = append(out, SQLiteTrigger{Name: name, Table: t.Table, Definition: b.String()})
	}
	return out
}
