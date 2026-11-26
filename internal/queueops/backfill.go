package queueops

import (
	"fmt"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// BackfillStatements are the statements `stack queue backfill` runs for
// queue, a @queue class of schema, the DB service sqlgen builds with opts
// (D88): for each table whose @enqueueOnChange names the queue, the
// trigger's backfill in dialect, which enqueues a message for each of the
// table's rows that meets the trigger's conditions, under the queue's
// onDuplicate, whatever the declaration's own backfill says. On Postgres
// each returns one row, the number of messages it enqueued or, under
// replace, made fresh; on SQLite, which has no insert in a WITH, each
// returns a row of 1 per message, which the runner adds up. Under replace
// both take one row of each key, the first by the table's key. A queue no
// trigger enqueues has none.
func BackfillStatements(schema *ir.Schema, opts sqlgen.Options, queue string, dialect Dialect) ([]string, error) {
	if td := schema.Types[queue]; td == nil || td.Queue == nil {
		return nil, fmt.Errorf("%s declares no queue %s", schema.Name, queue)
	}
	out, err := sqlgen.Generate(schema, opts)
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, nil
	}
	var statements []string
	for _, e := range out.EnqueueTriggers {
		if e.Queue != queue {
			continue
		}
		switch dialect {
		case "", Postgres:
			statements = append(statements, "WITH enqueued AS (\n"+e.BackfillSQL()+"\nRETURNING 1\n)\nSELECT count(*) FROM enqueued")
		case SQLite:
			statements = append(statements, e.SQLiteQueueBackfillSQL(sqliteQuote)+"\nRETURNING 1")
		default:
			return nil, fmt.Errorf("dialect %q is not postgres or sqlite", dialect)
		}
	}
	return statements, nil
}

// sqliteQuote quotes an identifier as the SQLite model does: always.
func sqliteQuote(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// MessageColumns are the columns of the message of queue, a @queue class of
// schema, the DB service sqlgen builds with opts, in the table's order, each
// with what it holds (sqlutil.QueueMessageColumnOf): what a listing of its
// dead messages on SQLite names (D68). The columns every queue keeps are
// not among them.
func MessageColumns(schema *ir.Schema, opts sqlgen.Options, queue string) ([]sqlutil.QueueMessageColumn, error) {
	td := schema.Types[queue]
	if td == nil || td.Queue == nil {
		return nil, fmt.Errorf("%s declares no queue %s", schema.Name, queue)
	}
	out, err := sqlgen.Generate(schema, opts)
	if err != nil {
		return nil, err
	}
	name := sqlgen.QueueTableName(td)
	kept := map[string]bool{}
	for _, c := range ir.QueueColumns() {
		kept[c] = true
	}
	if out != nil {
		for _, table := range out.Tables {
			if table.Name != name || table.OriginalName != td.Name {
				continue
			}
			var columns []sqlutil.QueueMessageColumn
			for _, c := range table.Columns {
				if !kept[c.Name] {
					columns = append(columns, sqlutil.QueueMessageColumnOf(c.Name, c.Type))
				}
			}
			return columns, nil
		}
	}
	return nil, fmt.Errorf("%s builds no table %s of queue %s", schema.Name, name, queue)
}
