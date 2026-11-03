package sqlgen

import (
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/behaviorlayout"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// A hearing table's change trigger (D103): after each insert, update and
// delete of one of the table's rows, whoever makes it (the core, a
// repository's bulk or hard write, a script, psql), it writes a message of
// the table's change queue, which a behavior worker claims and runs
// through the core's change command. It is built as @enqueueOnChange's is
// (enqueue.go): on Postgres a plpgsql function and the trigger that
// executes it, on SQLite one trigger per write, D1 included, so create.sql
// and the migration planner's models (internal/sqlmigrate) take them from
// here alike and the planner adds, replaces and drops them as it does
// those. It has no backfill (D88): it hears the writes made after it
// exists.
//
// A message holds the write's kind, the row's key, the database's clock in
// epoch milliseconds, and the row's image before and after the write: a
// JSON object keyed by column of the columns the layout maps for the core
// (behaviorlayout.ImageColumns). An update that moves none of them, nor a
// soft-deletable table's live column, writes none, so the version bump of
// SQLite's @optimistic trigger, an update of the other audit columns alone
// and a write of sub-table rows alone write nothing, while a soft delete
// and a restore, which move deleted_at alone, each write one. The insert
// never conflicts (the queue appends, and its unique index holds claimed
// messages alone), and on SQLite a BLOB a raw write stores in a column of
// the image is written as its hex, which json_object would refuse, so the
// trigger never refuses a write.
//
// A message is created at the database's clock when the write is made,
// which the change queue's claim orders one row's line by
// (sqlutil.QueueSpec.InOrder): on Postgres the trigger sets it, with due_at
// and the message's at, from one read of clock_timestamp(), so a later
// write of one row in one transaction comes later; on SQLite the queue's
// default stamps it to the millisecond.

// pgImageChunk is the most columns one jsonb_build_object call takes in an
// image: Postgres caps a function's arguments at 100, two per column.
const pgImageChunk = 50

// sqliteImageChunk is the most columns one json_object call takes in an
// image on SQLite: D1 caps a function's arguments at 32, two per column,
// where SQLite's own default is 127.
const sqliteImageChunk = 16

// ChangeTrigger is a hearing table's change trigger (D103).
type ChangeTrigger struct {
	// Origin is the table's type: Ticket. Queue is its change queue's:
	// TicketChange.
	Origin string
	Queue  string

	Table, QuotedTable           string
	QueueTable, QuotedQueueTable string

	// FunctionName is the Postgres trigger function, ticket_changes, and
	// TriggerName the Postgres trigger, trg_ticket_changes; SQLite's
	// triggers add the write to the name (_insert, _update, _delete).
	FunctionName string
	TriggerName  string

	// Key is the table's quoted key column.
	Key string

	// Image are the image's columns, unquoted, in layout order.
	Image []string

	// Live is the table's live column, unquoted, deleted_at of a
	// soft-deletable table, or "": the update's guard compares it beside the
	// image's columns, so a soft delete and a restore are heard, and the
	// image leaves it out.
	Live string
}

// buildChangeTriggers builds the change trigger of each table of the
// schema whose change queue the loader added (ir.BehaviorQueueChanges),
// from the tables and the queues' tables as sqlgen built them, sorted by
// table.
func buildChangeTriggers(tableTypes []*ir.TypeDef, tables []Table, schema *ir.Schema) ([]ChangeTrigger, error) {
	byName := map[string]*Table{}
	for i := range tables {
		byName[tables[i].OriginalName+"/"+tables[i].Name] = &tables[i]
	}
	var out []ChangeTrigger
	for _, td := range tableTypes {
		queueDef := schema.BehaviorQueueOf(td.Name, ir.BehaviorQueueChanges)
		if queueDef == nil {
			continue
		}
		source := byName[td.Name+"/"+sqlutil.TableName(td)]
		queue := byName[queueDef.Name+"/"+QueueTableName(queueDef)]
		if source == nil || queue == nil {
			return nil, fmt.Errorf("sqlgen: table %s: its change queue %s has no table, or it has none", td.Name, queueDef.Name)
		}
		image := behaviorlayout.ImageColumns(schema, td)
		if image == nil {
			return nil, fmt.Errorf("sqlgen: table %s: the columns of its change trigger's image cannot be modelled", td.Name)
		}
		t := ChangeTrigger{
			Origin:           td.Name,
			Queue:            queueDef.Name,
			Table:            source.Name,
			QuotedTable:      source.QuotedName,
			QueueTable:       queue.Name,
			QuotedQueueTable: queue.QuotedName,
			FunctionName:     source.Name + "_changes",
			TriggerName:      "trg_" + source.Name + "_changes",
			Key:              source.PrimaryKey,
			Image:            image,
			Live:             behaviorlayout.LiveColumn(schema, td),
		}
		for _, name := range []string{t.FunctionName, t.TriggerName, t.TriggerName + "_" + ir.EnqueueOnInsert, t.TriggerName + "_" + ir.EnqueueOnUpdate, t.TriggerName + "_" + ir.EnqueueOnDelete} {
			if len(name) > sqlutil.PostgresIdentifierLimit {
				return nil, fmt.Errorf("sqlgen: table %s: its change trigger %s is %d bytes, over PostgreSQL's %d-byte limit; give the table a shorter name (D103)", td.Name, name, len(name), sqlutil.PostgresIdentifierLimit)
			}
		}
		if t.Key == "" {
			return nil, fmt.Errorf("sqlgen: table %s: its change trigger names the row by its key, and the table has none", td.Name)
		}
		out = append(out, t)
	}
	slices.SortStableFunc(out, func(a, b ChangeTrigger) int { return strings.Compare(a.Table, b.Table) })
	return out, nil
}

// pgQuote quotes an identifier as the change trigger's function writes
// every one: always, its quotes doubled.
func pgQuote(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// changeColumns are the queue's columns the trigger's insert sets, in
// order: the message's fields (behaviorlayout's change fields, by D79's
// column rule), then, on Postgres, due_at and created_at.
func changeColumns(quote func(string) string, clocked bool) string {
	fields := []string{behaviorlayout.ChangeRowKeyField, behaviorlayout.ChangeWriteField, behaviorlayout.ChangeAtField, behaviorlayout.ChangeBeforeField, behaviorlayout.ChangeAfterField}
	columns := make([]string, 0, len(fields)+2)
	for _, field := range fields {
		columns = append(columns, quote(sqlutil.ColumnNameOf(field)))
	}
	if clocked {
		columns = append(columns, quote(ir.QueueColumnDueAt), quote(changeCreatedAt))
	}
	return strings.Join(columns, ", ")
}

// changeCreatedAt is the queue's column of when a message was created,
// which a change queue's claim orders one row's line by.
const changeCreatedAt = "created_at"

// unquoteKey is the table's key column without its quotes.
func (t ChangeTrigger) unquoteKey() string {
	return strings.ReplaceAll(strings.Trim(t.Key, `"`), `""`, `"`)
}

// guarded are the columns an update's guard compares: the image's, then
// the live column.
func (t ChangeTrigger) guarded() []string {
	if t.Live == "" {
		return t.Image
	}
	return append(slices.Clone(t.Image), t.Live)
}

// moved is true of an update that moves a column of the image or the live
// column, compared with distinct, the dialect's null-safe inequality, each
// column quoted by quote.
func (t ChangeTrigger) moved(distinct string, quote func(string) string) string {
	columns := t.guarded()
	parts := make([]string, len(columns))
	for i, c := range columns {
		parts[i] = "OLD." + quote(c) + " " + distinct + " NEW." + quote(c)
	}
	return strings.Join(parts, " OR ")
}

// image is the row's image as JSON, row NEW or OLD, in the dialect: value
// is a column's value as the image takes it, call builds one object of a
// chunk of at most chunk members, join joins the chunks, and empty is the
// empty image.
func (t ChangeTrigger) image(row string, value func(row, column string) string, call string, chunk int, join func([]string) string, empty string) string {
	if len(t.Image) == 0 {
		return empty
	}
	var chunks []string
	for start := 0; start < len(t.Image); start += chunk {
		end := min(start+chunk, len(t.Image))
		members := make([]string, 0, end-start)
		for _, c := range t.Image[start:end] {
			members = append(members, stringLiteral(c)+", "+value(row, c))
		}
		chunks = append(chunks, call+"("+strings.Join(members, ", ")+")")
	}
	return join(chunks)
}

// pgImage is the row's image on Postgres: jsonb_build_object, chunks
// joined with ||.
func (t ChangeTrigger) pgImage(row string) string {
	value := func(row, column string) string { return row + "." + pgQuote(column) }
	return t.image(row, value, "jsonb_build_object", pgImageChunk, func(chunks []string) string { return strings.Join(chunks, " || ") }, "'{}'::jsonb")
}

// sqliteImage is the row's image on SQLite: json_object, chunks nested in
// json_patch, which leaves a null member of a later chunk out, as the
// adapters read a null. A column's BLOB, which SQLite stores in a column of
// any type a raw write gives it, is its hex: json_object refuses a BLOB.
func (t ChangeTrigger) sqliteImage(row string, quote func(string) string) string {
	value := func(row, column string) string {
		c := row + "." + quote(column)
		return "CASE WHEN typeof(" + c + ") = 'blob' THEN hex(" + c + ") ELSE " + c + " END"
	}
	return t.image(row, value, "json_object", sqliteImageChunk, func(chunks []string) string {
		out := chunks[0]
		for _, chunk := range chunks[1:] {
			out = "json_patch(" + out + ", " + chunk + ")"
		}
		return out
	}, "'{}'")
}

// pgWritten is the trigger function's variable holding the database's
// clock when the write was made, read once per message.
const pgWritten = "written"

// pgAt is the database's clock in epoch milliseconds on Postgres: the
// statement's clock when the write was made, so a later write of one row
// in one transaction is later.
const pgAt = "floor(extract(epoch FROM " + pgWritten + ") * 1000)::bigint"

// sqliteAt is the database's clock in epoch milliseconds on SQLite.
const sqliteAt = "(CAST(strftime('%s', 'now') AS INTEGER) * 1000 + CAST(substr(strftime('%f', 'now'), 4) AS INTEGER))"

// Trigger is the Postgres trigger that executes the function after each
// insert, update and delete of a row.
func (t ChangeTrigger) Trigger() Trigger {
	return Trigger{
		Name:        t.TriggerName,
		Timing:      "AFTER INSERT OR UPDATE OR DELETE",
		Table:       t.Table,
		QuotedTable: t.QuotedTable,
		Function:    t.FunctionName,
	}
}

// FunctionSQL renders the Postgres trigger function: an update that moves
// no column of the image nor the live column returns at once, and every
// other write inserts its message, created and due at the statement's
// clock, so one row's messages are created in the order they were
// written: writes of one row serialize on its row lock, and the later
// write's clock is past the earlier's.
func (t ChangeTrigger) FunctionSQL() string {
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE OR REPLACE FUNCTION %s() RETURNS trigger AS $$\nDECLARE\n  %s timestamptz := clock_timestamp();\nBEGIN\n", pgQuote(t.FunctionName), pgWritten)
	fmt.Fprintf(&b, "  -- Writes a message of %s for each write of a row of %s\n  -- its behaviors hear, whoever makes it (D103).\n", t.Queue, t.Table)
	if len(t.guarded()) == 0 {
		b.WriteString("  IF TG_OP = 'UPDATE' THEN\n    RETURN NULL;\n  END IF;\n")
	} else {
		fmt.Fprintf(&b, "  IF TG_OP = 'UPDATE' AND NOT (%s) THEN\n    RETURN NULL;\n  END IF;\n", t.moved("IS DISTINCT FROM", pgQuote))
	}
	key := pgQuote(t.unquoteKey())
	columns := changeColumns(pgQuote, true)
	insert := func(row, write, before, after string) string {
		return fmt.Sprintf("    INSERT INTO %s (%s)\n    VALUES (%s.%s, '%s', %s, %s, %s, %s, %s);\n", pgQuote(t.QueueTable), columns, row, key, write, pgAt, before, after, pgWritten, pgWritten)
	}
	b.WriteString("  IF TG_OP = 'INSERT' THEN\n")
	b.WriteString(insert("NEW", ir.EnqueueOnInsert, "NULL", t.pgImage("NEW")))
	b.WriteString("  ELSIF TG_OP = 'UPDATE' THEN\n")
	b.WriteString(insert("NEW", ir.EnqueueOnUpdate, t.pgImage("OLD"), t.pgImage("NEW")))
	b.WriteString("  ELSE\n")
	b.WriteString(insert("OLD", ir.EnqueueOnDelete, t.pgImage("OLD"), "NULL"))
	b.WriteString("  END IF;\n  RETURN NULL;\nEND;\n$$ LANGUAGE plpgsql")
	return b.String()
}

// SQLiteTriggers renders the change trigger on SQLite, one trigger per
// write, insert, update and delete: each holds its insert, and the
// update's WHEN compares the image's columns and the live column with IS
// NOT, SQLite's null-safe inequality. A table whose image is empty and
// that is not soft-deletable has no update trigger. The message's id,
// due_at and created_at take the queue table's defaults. quote quotes an
// identifier as the SQLite dialect does.
func (t ChangeTrigger) SQLiteTriggers(quote func(string) string) []SQLiteTrigger {
	key := quote(t.unquoteKey())
	columns := changeColumns(quote, false)
	var out []SQLiteTrigger
	for _, write := range []string{ir.EnqueueOnInsert, ir.EnqueueOnUpdate, ir.EnqueueOnDelete} {
		row, before, after := "NEW", "NULL", t.sqliteImage("NEW", quote)
		switch write {
		case ir.EnqueueOnUpdate:
			if len(t.guarded()) == 0 {
				continue
			}
			before = t.sqliteImage("OLD", quote)
		case ir.EnqueueOnDelete:
			row, before, after = "OLD", t.sqliteImage("OLD", quote), "NULL"
		}
		name := t.TriggerName + "_" + write
		var b strings.Builder
		fmt.Fprintf(&b, "CREATE TRIGGER %s AFTER %s ON %s FOR EACH ROW\n", quote(name), strings.ToUpper(write), quote(t.Table))
		if write == ir.EnqueueOnUpdate {
			fmt.Fprintf(&b, "WHEN %s\n", t.moved("IS NOT", quote))
		}
		fmt.Fprintf(&b, "BEGIN\n  INSERT INTO %s (%s)\n  VALUES (%s.%s, '%s', %s, %s, %s);\nEND", quote(t.QueueTable), columns, row, key, write, sqliteAt, before, after)
		out = append(out, SQLiteTrigger{Name: name, Table: t.Table, Definition: b.String()})
	}
	return out
}
