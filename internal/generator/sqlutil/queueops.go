package sqlutil

import (
	"fmt"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// QueueOperatorStatements are the statements an operator runs on a queue's
// table on Postgres, beside the claims (QueueStatements): they read the
// messages that are dead, make them ready again and delete them
// (`superschematic stack queue`, docs/stack-model.md, section 8.8). The
// migration runner runs them as the command's document carries them
// (`superschematic-migrate queue`), so this function is their one writer.
// They run on Postgres alone: the claims run on SQLite too
// (SQLiteQueueStatements), and `stack queue` on SQLite is not built (D53,
// amended).
//
// Each statement that takes messages by ID takes them as one parameter, $1,
// a text array of their IDs, which pgx and node-postgres both bind from a
// list of strings; the statement casts it to UUIDs. Each reads and returns
// what the runner's driver scans: text and 64-bit integers, never NULL.
// Times are the database's clock.
type QueueOperatorStatements struct {
	// Dead lists up to $1 dead messages, the oldest death first, and
	// returns, per message, its ID, its attempts, its last error (empty
	// when it has none), when it died as RFC 3339 text in UTC to the
	// second, the message as a JSON object of its columns, and the number
	// of dead messages in all. A dead message changes no more, so its
	// updated_at is when it died.
	Dead string

	// States returns the ID and state of each message among $1 the table
	// holds, so a retry or a drop that missed one can say why.
	States string

	// Retry makes each dead message among $1 ready again: due now, with no
	// attempts, so its retries start over, and no claim. Its last error
	// stays until its next failure replaces it. It returns the IDs it
	// changed. RetryAll does so to every dead message.
	Retry    string
	RetryAll string

	// Drop deletes each dead message among $1 and returns the IDs it
	// deleted. DropAll deletes every dead message.
	Drop    string
	DropAll string
}

// PostgresQueueOperatorStatements are the operator's statements on the
// queue whose table is table, quoted for SQL as QuoteIdentifier quotes it.
// They read the columns every queue keeps (ir.QueueColumns), and the
// message's as the row's other columns, so they need no list of them.
func PostgresQueueOperatorStatements(table string) QueueOperatorStatements {
	kept := make([]string, 0, len(ir.QueueColumns()))
	for _, c := range ir.QueueColumns() {
		kept = append(kept, "'"+c+"'")
	}
	dead := fmt.Sprintf("q.state = '%s'", ir.QueueStateDead)
	ids := "id = ANY($1::text[]::uuid[])"
	retry := fmt.Sprintf("UPDATE %s SET state = '%s', attempts = 0, due_at = now(), claim_token = NULL, claim_expires_at = NULL, updated_at = now()", table, ir.QueueStateReady)
	return QueueOperatorStatements{
		Dead: lines(
			"SELECT q.id::text, q.attempts::bigint, COALESCE(q.last_error, ''),",
			`  to_char(q.updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),`,
			"  (to_jsonb(q) - ARRAY["+strings.Join(kept, ", ")+"]::text[])::text,",
			"  count(*) OVER ()",
			"FROM "+table+" AS q",
			"WHERE "+dead,
			"ORDER BY q.updated_at, q.id",
			"LIMIT $1",
		),
		States: "SELECT q.id::text, q.state FROM " + table + " AS q WHERE q." + ids,
		Retry: lines(
			retry,
			fmt.Sprintf("WHERE state = '%s' AND %s", ir.QueueStateDead, ids),
			"RETURNING id::text",
		),
		RetryAll: lines(
			retry,
			fmt.Sprintf("WHERE state = '%s'", ir.QueueStateDead),
			"RETURNING id::text",
		),
		Drop: lines(
			"DELETE FROM "+table,
			fmt.Sprintf("WHERE state = '%s' AND %s", ir.QueueStateDead, ids),
			"RETURNING id::text",
		),
		DropAll: lines(
			"DELETE FROM "+table,
			fmt.Sprintf("WHERE state = '%s'", ir.QueueStateDead),
			"RETURNING id::text",
		),
	}
}
