package queueops

import (
	"fmt"

	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	ir "github.com/parable-work/superschematic/ir"
)

// BackfillStatements are the statements `stack queue backfill` runs for
// queue, a @queue class of schema, the DB service sqlgen builds with opts
// (D88): for each table whose @enqueueOnChange names the queue, the
// trigger's backfill on Postgres, which enqueues a message for each of the
// table's rows that meets the trigger's conditions, under the queue's
// onDuplicate, whatever the declaration's own backfill says. Each returns
// one row, the number of messages it enqueued or, under replace, made
// fresh. A queue no trigger enqueues has none.
func BackfillStatements(schema *ir.Schema, opts sqlgen.Options, queue string) ([]string, error) {
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
		if e.Queue == queue {
			statements = append(statements, "WITH enqueued AS (\n"+e.BackfillSQL()+"\nRETURNING 1\n)\nSELECT count(*) FROM enqueued")
		}
	}
	return statements, nil
}
