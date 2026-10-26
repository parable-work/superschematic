package sqlgen

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
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
	// default for a field the message leaves out).
	Columns []string
	Values  []string
	Literal []bool

	// Conflict and SQLiteConflict end the insert in each dialect; empty
	// for a queue that appends.
	Conflict       string
	SQLiteConflict string
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
			source := byName[td.Name+"/"+codegen.ToSnakeCase(td.Name)]
			queueDef := schema.Types[def.Queue]
			if source == nil || queueDef == nil || queueDef.Queue == nil {
				return nil, fmt.Errorf("@enqueueOnChange(%s) on %s: no table of %s or no queue %s", def.Queue, td.Name, td.Name, def.Queue)
			}
			queue := byName[queueDef.Name+"/"+QueueTableName(queueDef.Name)]
			if queue == nil {
				return nil, fmt.Errorf("@enqueueOnChange(%s) on %s: queue %s has no table", def.Queue, td.Name, def.Queue)
			}
			trigger, err := enqueueTrigger(td, def, source, queueDef, queue, schema)
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

func enqueueTrigger(td *ir.TypeDef, def *ir.EnqueueOnChange, source *Table, queueDef *ir.TypeDef, queue *Table, schema *ir.Schema) (EnqueueTrigger, error) {
	base := codegen.ToSnakeCase(queueDef.Name)
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
			c, err := column(from)
			if err != nil {
				return t, err
			}
			t.Columns, t.Values, t.Literal = append(t.Columns, qc.QuotedName), append(t.Values, c.QuotedName), append(t.Literal, false)
			continue
		}
		// An enum the message leaves out takes its schema default, as an
		// enqueue writes it; any other column its own default, which
		// verification made sure every required one has.
		if f.Default != nil && schema.Enums[f.TypeRef.Name] != nil && f.Required && !f.TypeRef.IsArray {
			t.Columns, t.Values, t.Literal = append(t.Columns, qc.QuotedName), append(t.Values, stringLiteral(*f.Default)), append(t.Literal, true)
			continue
		}
		if !qc.Nullable && qc.DefaultValue == "" {
			return t, fmt.Errorf("queue %s's field %s is required and its column has no default, so the message maps it", queueDef.Name, f.Name)
		}
	}
	spec := sqlutil.QueueSpecOf(queueDef, queue.QuotedName, columns, true)
	t.Conflict = sqlutil.PostgresQueueStatements(spec).Conflict
	t.SQLiteConflict = sqlutil.SQLiteQueueStatements(spec).Conflict
	return t, nil
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
	fmt.Fprintf(&b, "CREATE OR REPLACE FUNCTION %s() RETURNS trigger AS $$\nBEGIN\n", t.FunctionName)
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
	columns := make([]string, len(t.Columns))
	values := make([]string, len(t.Values))
	for i := range t.Columns {
		columns[i] = quote(t.Columns[i])
		values[i] = t.Values[i]
		if !t.Literal[i] {
			values[i] = row + "." + quote(t.Values[i])
		}
	}
	s := "INSERT INTO " + table + " (" + strings.Join(columns, ", ") + ") VALUES (" + strings.Join(values, ", ") + ")"
	if conflict != "" {
		s += "\n  " + conflict
	}
	return s
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
		return col + " = " + stringLiteral(v)
	}
	return col + " = " + stringLiteral(fmt.Sprint(w.Equals))
}

// stringLiteral is s as a SQL string literal.
func stringLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// SQLiteTrigger is one of an EnqueueTrigger's triggers on SQLite.
type SQLiteTrigger struct {
	Name       string
	Table      string
	Definition string
}

// SQLiteTriggers renders the declaration's triggers on SQLite, one per
// write, in ir.EnqueueEvents order: each holds its insert, and its WHEN the
// conditions and, for an update, the comparison of the fields with IS NOT,
// SQLite's null-safe inequality. quote quotes an identifier as the SQLite
// dialect does.
func (t EnqueueTrigger) SQLiteTriggers(quote func(string) string) []SQLiteTrigger {
	unquote := func(c string) string { return quote(strings.Trim(c, `"`)) }
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
