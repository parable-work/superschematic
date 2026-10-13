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

	// Complete marks message $1 done while token $2 claims it.
	Complete string

	// Fail records the cause $5 on message $1 while token $2 claims it,
	// and makes it due again after $4 seconds, or dead once its attempts
	// pass the retries, $3. It returns the message's state.
	Fail string

	// Extend holds message $1 for $3 seconds from now while token $2
	// claims it.
	Extend string

	// Release gives message $1 back while token $2 claims it, ready and
	// due at once, without counting the attempt: its handler did not
	// finish, and did not fail.
	Release string
}

// PostgresQueueStatements are the statements of the queue whose table is
// table and whose message's columns are columns, in field order, each
// quoted for SQL as QuoteIdentifier quotes it.
func PostgresQueueStatements(table string, columns []string) QueueStatements {
	returned := "q.id::text, q.claim_token::text, q.attempts"
	for _, c := range columns {
		returned += ", q." + c
	}
	claimed := fmt.Sprintf("WHERE id = $1::uuid AND claim_token = $2::uuid AND state = '%s'", ir.QueueStateClaimed)
	unclaim := "claim_token = NULL, claim_expires_at = NULL"
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
		Complete: lines(
			fmt.Sprintf("UPDATE %s SET state = '%s', %s, updated_at = now()", table, ir.QueueStateDone, unclaim),
			claimed,
		),
		Fail: lines(
			"UPDATE "+table+" SET",
			fmt.Sprintf("  state = CASE WHEN attempts > $3 THEN '%s' ELSE '%s' END,", ir.QueueStateDead, ir.QueueStateReady),
			"  due_at = CASE WHEN attempts > $3 THEN due_at ELSE now() + make_interval(secs => $4) END,",
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
	}
}

// All are the statements by name, in the order the struct declares them,
// for the generators that write each and the tests that compare them.
func (s QueueStatements) All() []NamedStatement {
	return []NamedStatement{
		{"insert", s.Insert}, {"expire", s.Expire}, {"claim", s.Claim}, {"complete", s.Complete},
		{"fail", s.Fail}, {"extend", s.Extend}, {"release", s.Release},
	}
}

// NamedStatement is one of a queue's statements with its name, lower case.
type NamedStatement struct {
	Name string
	SQL  string
}

// lines joins a statement's lines.
func lines(l ...string) string { return strings.Join(l, "\n") }
