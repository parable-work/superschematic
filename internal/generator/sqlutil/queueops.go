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
	// changed. RetryAll does so to every dead message. On a queue that
	// keeps one ready message per key (D80), a retry leaves dead a message
	// whose key a ready message holds, and of the dead messages of one key
	// it retries the one that died last; States says so of each it left.
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
// message's as the row's other columns, so they need no list of them. key
// is the quoted column of the key of a queue that keeps one ready message
// per key (D80), or "" for every other queue.
func PostgresQueueOperatorStatements(table, key string) QueueOperatorStatements {
	kept := make([]string, 0, len(ir.QueueColumns()))
	for _, c := range ir.QueueColumns() {
		kept = append(kept, "'"+c+"'")
	}
	dead := fmt.Sprintf("q.state = '%s'", ir.QueueStateDead)
	ids := "id = ANY($1::text[]::uuid[])"
	retry := fmt.Sprintf("UPDATE %s SET state = '%s', attempts = 0, due_at = now(), claim_token = NULL, claim_expires_at = NULL, updated_at = now()", table, ir.QueueStateReady)
	s := QueueOperatorStatements{
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
	if key != "" {
		// One dead message per key, the last to die, and none whose key a
		// ready message holds.
		free := fmt.Sprintf("NOT EXISTS (SELECT 1 FROM %[1]s AS p WHERE p.%[2]s = %[1]s.%[2]s AND p.state = '%[3]s')", table, key, ir.QueueStateReady)
		last := func(where string) string {
			return fmt.Sprintf("id IN (SELECT DISTINCT ON (%s) id FROM %s WHERE %s ORDER BY %s, updated_at DESC, id)", key, table, where, key)
		}
		s.States = lines(
			fmt.Sprintf("SELECT q.id::text, CASE WHEN q.state = '%[1]s' AND EXISTS (SELECT 1 FROM %[2]s AS p WHERE p.%[3]s = q.%[3]s AND p.state = '%[4]s')", ir.QueueStateDead, table, key, ir.QueueStateReady),
			fmt.Sprintf("  THEN 'dead, and a ready message holds its key' ELSE q.state END FROM %s AS q WHERE q.%s", table, ids),
		)
		s.Retry = lines(
			retry,
			fmt.Sprintf("WHERE state = '%s' AND %s", ir.QueueStateDead, ids),
			"  AND "+free,
			"  AND "+last(fmt.Sprintf("state = '%s' AND %s", ir.QueueStateDead, ids)),
			"RETURNING id::text",
		)
		s.RetryAll = lines(
			retry,
			fmt.Sprintf("WHERE state = '%s'", ir.QueueStateDead),
			"  AND "+free,
			"  AND "+last(fmt.Sprintf("state = '%s'", ir.QueueStateDead)),
			"RETURNING id::text",
		)
	}
	return s
}
