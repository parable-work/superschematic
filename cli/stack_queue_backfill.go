package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/queueops"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// `superschematic stack queue backfill <environment> <db>.<Queue>` (D88):
// enqueues a message for each row an @enqueueOnChange trigger of the queue
// would enqueue one for if the row were inserted now, for a trigger a plan
// added without backfill, or a table too large to backfill inside the
// migration step that creates the trigger.

func newStackQueueBackfillCmd(a *app) *cobra.Command {
	flags := &queueFlags{}
	cmd := &cobra.Command{
		Use:   "backfill <environment> <queue>",
		Short: "Enqueue a message of a queue for each row its triggers would enqueue one for",
		Long: stackQueueLong + `

backfill enqueues a message for each row of each table whose
@enqueueOnChange names the queue that meets the trigger's conditions, as
the trigger's insert of the row would write it, under the queue's
onDuplicate: a queue that replaces or skips keeps one ready message per
key. It runs in one transaction that holds no lock on the tables, so a
row written while it runs may be enqueued by its trigger too, which an
appending queue then holds twice. It says how many messages it enqueued.
A plan enqueues the same messages in the step that adds the trigger
when the declaration says backfill: true.

Examples:
  superschematic stack queue backfill Dev shop-db.StockLow
  superschematic stack queue backfill Dev shop-db.StockLow --format json`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStackQueue(cmd, a, flags, args[0], args[1], func(service, queue string) queueops.Request {
				return queueops.Request{Service: service, Queue: queue, Operation: queueops.Backfill}
			})
		},
	}
	flags.register(cmd)
	return cmd
}

// queueBackfills are the backfill statements of the DB service's queue,
// from its schema as this checkout has it, with the sqlgen options a
// build passes (queueops.BackfillStatements).
func (c *deployContext) queueBackfills(service, queue string) ([]string, error) {
	schema, cfg, err := c.version.load(service)
	if err != nil {
		return nil, err
	}
	outputs, err := registry.ParseOutputs(cfg.Outputs, c.version.reg)
	if err != nil {
		return nil, fmt.Errorf("schema config for %s: %w", service, err)
	}
	var deps map[string]*ir.Schema
	for _, dep := range cfg.Dependencies {
		depSchema, _, err := c.version.load(dep.Name)
		if err != nil {
			return nil, fmt.Errorf("load dependency %s of %s: %w", dep.Name, service, err)
		}
		if deps == nil {
			deps = make(map[string]*ir.Schema, len(cfg.Dependencies))
		}
		deps[dep.Name] = depSchema
	}
	statements, err := queueops.BackfillStatements(schema, generator.SQLOptions(cfg, outputs, deps, c.version.names), queue)
	if err != nil {
		return nil, err
	}
	if len(statements) == 0 {
		return nil, fmt.Errorf("no @enqueueOnChange of %s enqueues %s, so stack queue backfill has no rows to enqueue it for", service, queue)
	}
	return statements, nil
}

// writeBackfillText prints what a backfill did for a person.
func writeBackfillText(b *strings.Builder, environment, ref string, r *queueops.Result) {
	if r.Total == 0 {
		fmt.Fprintf(b, "Queue %s of %s: no row meets its triggers' conditions, so the backfill enqueued nothing.\n", ref, environment)
		return
	}
	enqueued := "1 message"
	if r.Total != 1 {
		enqueued = fmt.Sprintf("%d messages", r.Total)
	}
	fmt.Fprintf(b, "Backfilled queue %s of %s: enqueued %s, one for each row that meets its triggers' conditions.\n", ref, environment, enqueued)
}
