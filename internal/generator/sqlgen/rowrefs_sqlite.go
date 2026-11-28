package sqlgen

import (
	"fmt"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
)

// References' and attachments' triggers on SQLite (D182), which has no
// statement triggers and no transition tables: each is a row trigger after
// each delete, under the Postgres trigger's name, holding its statements.
//
//   - ReferencesClear, on each table a reference may point at: for each
//     references sub-table that may hold one, a cleared message for each
//     reference to the deleted row, when the declaration names a queue, then
//     the references deleted. A row trigger runs as each row goes, so of a
//     statement that deletes both a holder and a row it points at, the
//     holder's own references are gone first only when the holder goes
//     first; a cleared message may then name a holder the same statement
//     deleted, which its worker finds gone.
//   - AttachmentsRelease, on each attachments sub-table: a released message
//     for the deleted file's object, unless another file of the sub-table
//     still names it, so a statement that deletes the files of one object
//     enqueues its release once, with its last file, as Postgres's DISTINCT
//     ON does. A file of another table that names it holds it still, which
//     the release counts.
//
// A key is compared as text, CAST(... AS TEXT), as Postgres's ::text: a
// UUID is its lowercase hyphenated text either way.

// SQLiteTrigger renders the clearing trigger on SQLite. quote quotes an
// identifier as the SQLite dialect does.
func (c ReferencesClear) SQLiteTrigger(quote func(string) string) SQLiteTrigger {
	q := func(s string) string { return quote(sqlutil.Unquote(s)) }
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE TRIGGER %s AFTER DELETE ON %s FOR EACH ROW\nBEGIN\n", quote(c.TriggerName), quote(c.Table))
	fmt.Fprintf(&b, "  -- The references to the row of %s deleted (D154, D182), cleared,\n  -- and the row that held each told on its cleared queue.\n", c.Origin)
	for _, h := range c.Holders {
		where := fmt.Sprintf("%s = %s AND %s = CAST(OLD.%s AS TEXT)", q(h.TargetTable), sqlutil.QuoteLiteral(c.Origin), q(h.TargetKey), q(c.Key))
		if h.Queue != "" {
			columns := make([]string, len(h.Columns))
			for i, column := range h.Columns {
				columns[i] = q(column)
			}
			fmt.Fprintf(&b, "  INSERT INTO %s (%s)\n    SELECT %s, CAST(%s AS TEXT), %s, %s, %s FROM %s WHERE %s;\n",
				q(h.Queue), strings.Join(columns, ", "), sqlutil.QuoteLiteral(h.Type), q(h.RowColumn), q(h.TargetTable), q(h.TargetKey), q(h.Role), q(h.QuotedTable), where)
		}
		fmt.Fprintf(&b, "  DELETE FROM %s WHERE %s;\n", q(h.QuotedTable), where)
	}
	b.WriteString("END")
	return SQLiteTrigger{Name: c.TriggerName, Table: c.Table, Definition: b.String()}
}

// SQLiteTrigger renders the release trigger on SQLite. quote quotes an
// identifier as the SQLite dialect does.
func (r AttachmentsRelease) SQLiteTrigger(quote func(string) string) SQLiteTrigger {
	q := func(s string) string { return quote(sqlutil.Unquote(s)) }
	columns := make([]string, len(r.Columns))
	for i, column := range r.Columns {
		columns[i] = q(column)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE TRIGGER %s AFTER DELETE ON %s FOR EACH ROW\nBEGIN\n", quote(r.TriggerName), quote(r.Table))
	fmt.Fprintf(&b, "  -- The object the file of %s deleted named (D154, D182), released\n  -- once no file of the table names it: its worker deletes it once no\n  -- file of any table does.\n", r.Origin)
	fmt.Fprintf(&b, "  INSERT INTO %s (%s)\n    SELECT OLD.%s, OLD.%s, 'detached' WHERE NOT EXISTS (SELECT 1 FROM %s WHERE %s = OLD.%s);\n",
		q(r.Queue), strings.Join(columns, ", "), q(r.ObjectKey), q(r.ContentHash), quote(r.Table), q(r.ObjectKey), q(r.ObjectKey))
	b.WriteString("END")
	return SQLiteTrigger{Name: r.TriggerName, Table: r.Table, Definition: b.String()}
}
