package sqlutil

import (
	"fmt"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// QueueStatements are the statements of a queue's table on Postgres
// (docs/stack-model.md, section 8.8, D53), written here once for every
// language that claims: the Go ORM's queues.go and the TypeScript types'
// queues.ts carry them as they are, so a message one language enqueues
// another claims, completes, fails, extends and releases alike. Each takes
// positional parameters from $1, which pgx and node-postgres both bind.
//
// A claim takes the messages that are ready and due, or claimed past their
// claim's expiry, with FOR UPDATE SKIP LOCKED, so workers that claim at
// once take different messages, and gives each a new claim token and one
// more attempt. A message whose last attempt's claim expired is dead
// first, so a message that kills its worker every time stops coming back.
// Complete, Fail, Extend and Release change a message only while the token
// still claims it: no row is a lost claim. Times are the database's clock.
//
// A failure waits longer each time (D68): the attempt that failed, n, waits
// the backoff × 2^(n−1), up to the maximum backoff, before it is due again.
// A done message is never changed again, so its updated_at is when it was
// done: Prune removes the done messages older than the queue's retention,
// a batch at a time, and never a dead one, which waits for an operator. A
// queue that keeps no done message, a retention of zero, deletes a message
// as it completes it.
type QueueStatements struct {
	// Insert begins an enqueue. The caller adds the columns its message
	// sets with their placeholders, ` (a, b) VALUES ($1, $2)`, or
	// ` DEFAULT VALUES` when it sets none, and runs it in its own
	// transaction.
	Insert string

	// Expire marks dead each message whose last attempt's claim expired:
	// $1 is the queue's retries.
	Expire string

	// Claim claims up to $1 due messages for $2 seconds, and returns, per
	// message, its ID and the claim's token as text, the attempt, from 1,
	// then the message's columns in field order.
	Claim string

	// Complete marks message $1 done while token $2 claims it, or, for a
	// queue that keeps no done message, deletes it.
	Complete string

	// Fail records the cause $5 on message $1 while token $2 claims it,
	// and makes it due again after $4 seconds, the backoff, doubled for
	// each attempt before this one and capped at $6 seconds, the maximum
	// backoff; or dead once its attempts pass the retries, $3. It returns
	// the message's state.
	Fail string

	// Extend holds message $1 for $3 seconds from now while token $2
	// claims it.
	Extend string

	// Release gives message $1 back while token $2 claims it, ready and
	// due at once, without counting the attempt: its handler did not
	// finish, and did not fail.
	Release string

	// Prune deletes up to $2 done messages that were done $1 seconds ago
	// or more, the queue's retention, the oldest first, skipping any row
	// another prune holds, so workers that prune at once delete different
	// rows. Each run is one short transaction; a worker runs it again
	// while it deletes a whole batch.
	Prune string
}

// queueBackoffDoublings bounds the exponent of a failure's backoff, so the
// doubling cannot overflow however many retries a queue allows: 2^40
// seconds is longer than any duration a queue can write, so the maximum
// backoff caps the wait first.
const queueBackoffDoublings = 40

// PostgresQueueStatements are the statements of the queue whose table is
// table and whose message's columns are columns, in field order, each
// quoted for SQL as QuoteIdentifier quotes it. keepDone is whether the
// queue keeps a done message for a retention, as every queue does unless
// its retention is zero; one that does not deletes a message as it
// completes it.
func PostgresQueueStatements(table string, columns []string, keepDone bool) QueueStatements {
	returned := "q.id::text, q.claim_token::text, q.attempts"
	for _, c := range columns {
		returned += ", q." + c
	}
	claimed := fmt.Sprintf("WHERE id = $1::uuid AND claim_token = $2::uuid AND state = '%s'", ir.QueueStateClaimed)
	unclaim := "claim_token = NULL, claim_expires_at = NULL"
	complete := lines("DELETE FROM "+table, claimed)
	if keepDone {
		complete = lines(
			fmt.Sprintf("UPDATE %s SET state = '%s', %s, updated_at = now()", table, ir.QueueStateDone, unclaim),
			claimed,
		)
	}
	return QueueStatements{
		Insert: "INSERT INTO " + table,
		Expire: lines(
			fmt.Sprintf("UPDATE %s SET state = '%s', %s,", table, ir.QueueStateDead, unclaim),
			"  last_error = 'the claim of its last attempt expired before its handler finished', updated_at = now()",
			fmt.Sprintf("WHERE state = '%s' AND claim_expires_at <= now() AND attempts > $1", ir.QueueStateClaimed),
		),
		Claim: lines(
			"WITH due AS (",
			"  SELECT id FROM "+table,
			fmt.Sprintf("  WHERE (state = '%s' AND due_at <= now()) OR (state = '%s' AND claim_expires_at <= now())", ir.QueueStateReady, ir.QueueStateClaimed),
			"  ORDER BY due_at, id",
			"  LIMIT $1",
			"  FOR UPDATE SKIP LOCKED",
			")",
			fmt.Sprintf("UPDATE %s AS q SET state = '%s', attempts = q.attempts + 1, claim_token = gen_random_uuid(),", table, ir.QueueStateClaimed),
			"  claim_expires_at = now() + make_interval(secs => $2), updated_at = now()",
			"FROM due WHERE q.id = due.id",
			"RETURNING "+returned,
		),
		Complete: complete,
		Fail: lines(
			"UPDATE "+table+" SET",
			fmt.Sprintf("  state = CASE WHEN attempts > $3 THEN '%s' ELSE '%s' END,", ir.QueueStateDead, ir.QueueStateReady),
			fmt.Sprintf("  due_at = CASE WHEN attempts > $3 THEN due_at ELSE now() + make_interval(secs => LEAST($4 * power(2, LEAST(attempts - 1, %d)), $6)) END,", queueBackoffDoublings),
			"  last_error = $5, "+unclaim+", updated_at = now()",
			claimed,
			"RETURNING state",
		),
		Extend: lines(
			"UPDATE "+table+" SET claim_expires_at = now() + make_interval(secs => $3), updated_at = now()",
			claimed,
		),
		Release: lines(
			fmt.Sprintf("UPDATE %s SET state = '%s', attempts = GREATEST(attempts - 1, 0), %s, updated_at = now()", table, ir.QueueStateReady, unclaim),
			claimed,
		),
		Prune: lines(
			"DELETE FROM "+table+" WHERE id IN (",
			"  SELECT id FROM "+table,
			fmt.Sprintf("  WHERE state = '%s' AND updated_at <= now() - make_interval(secs => $1)", ir.QueueStateDone),
			"  ORDER BY updated_at",
			"  LIMIT $2",
			"  FOR UPDATE SKIP LOCKED",
			")",
		),
	}
}

// All are the statements by name, in the order the struct declares them,
// for the generators that write each and the tests that compare them.
func (s QueueStatements) All() []NamedStatement {
	return []NamedStatement{
		{"insert", s.Insert}, {"expire", s.Expire}, {"claim", s.Claim}, {"complete", s.Complete},
		{"fail", s.Fail}, {"extend", s.Extend}, {"release", s.Release}, {"prune", s.Prune},
	}
}

// NamedStatement is one of a queue's statements with its name, lower case.
type NamedStatement struct {
	Name string
	SQL  string
}

// lines joins a statement's lines.
func lines(l ...string) string { return strings.Join(l, "\n") }
