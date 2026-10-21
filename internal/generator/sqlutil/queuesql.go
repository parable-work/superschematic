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

// sqliteNow is the database's clock on SQLite as a queue's times hold it:
// RFC 3339 in UTC to the millisecond, the form sqlgen's SQLite default of
// CURRENT_TIMESTAMP writes (D27), so a queue's times compare as text.
const sqliteNow = "strftime('%Y-%m-%dT%H:%M:%fZ', 'now')"

// sqliteIn is the database's clock on SQLite plus the seconds expression
// seconds, which may hold a fraction, in sqliteNow's form.
func sqliteIn(seconds string) string {
	return "strftime('%Y-%m-%dT%H:%M:%fZ', 'now', '+' || (" + seconds + ") || ' seconds')"
}

// sqliteAgo is the database's clock on SQLite minus the seconds expression
// seconds, in sqliteNow's form.
func sqliteAgo(seconds string) string {
	return "strftime('%Y-%m-%dT%H:%M:%fZ', 'now', '-' || (" + seconds + ") || ' seconds')"
}

// sqliteUUID is a new version 4 UUID in its lowercase text, a claim's
// token: the expression sqlgen's SQLite model writes for
// gen_random_uuid(), evaluated once per row.
const sqliteUUID = "lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || " +
	"substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + (random() & 3), 1) || " +
	"substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6)))"

// SQLiteQueueStatements are the statements of the queue whose table is
// table and whose message's columns are columns, on SQLite (D53, amended):
// the claims of PostgresQueueStatements, with the same parameters by
// number (?1, ?2, ...), the same states, attempts, lease, backoff and
// retention, written for SQLite's one writer. SQLite has no FOR UPDATE
// SKIP LOCKED and needs none: each statement is one UPDATE or DELETE,
// which takes the database's write lock before it reads a row, so two
// workers that claim at once run one after the other, and the second sees
// what the first claimed. A claim is one UPDATE ... WHERE id IN (the due
// messages, oldest first, up to ?1) ... RETURNING, so no message is claimed
// twice and no transaction spans a round trip. The times are text in
// sqliteNow's form, which every statement writes and compares; the backoff
// doubles with a shift, since SQLite's power() is an optional extension,
// and its product turns to a REAL, not an overflow, past 2^63. A claim's
// ID and token are the columns' text, lowercase, which the changes compare
// as text.
func SQLiteQueueStatements(table string, columns []string, keepDone bool) QueueStatements {
	returned := "id, claim_token, attempts"
	for _, c := range columns {
		returned += ", " + c
	}
	claimed := fmt.Sprintf("WHERE id = ?1 AND claim_token = ?2 AND state = '%s'", ir.QueueStateClaimed)
	unclaim := "claim_token = NULL, claim_expires_at = NULL"
	complete := lines("DELETE FROM "+table, claimed)
	if keepDone {
		complete = lines(
			fmt.Sprintf("UPDATE %s SET state = '%s', %s, updated_at = %s", table, ir.QueueStateDone, unclaim, sqliteNow),
			claimed,
		)
	}
	return QueueStatements{
		Insert: "INSERT INTO " + table,
		Expire: lines(
			fmt.Sprintf("UPDATE %s SET state = '%s', %s,", table, ir.QueueStateDead, unclaim),
			"  last_error = 'the claim of its last attempt expired before its handler finished', updated_at = "+sqliteNow,
			fmt.Sprintf("WHERE state = '%s' AND claim_expires_at <= %s AND attempts > ?1", ir.QueueStateClaimed, sqliteNow),
		),
		Claim: lines(
			fmt.Sprintf("UPDATE %s SET state = '%s', attempts = attempts + 1, claim_token = %s,", table, ir.QueueStateClaimed, sqliteUUID),
			"  claim_expires_at = "+sqliteIn("?2")+", updated_at = "+sqliteNow,
			"WHERE id IN (",
			"  SELECT id FROM "+table,
			fmt.Sprintf("  WHERE (state = '%s' AND due_at <= %s) OR (state = '%s' AND claim_expires_at <= %s)", ir.QueueStateReady, sqliteNow, ir.QueueStateClaimed, sqliteNow),
			"  ORDER BY due_at, id",
			"  LIMIT ?1",
			")",
			"RETURNING "+returned,
		),
		Complete: complete,
		Fail: lines(
			"UPDATE "+table+" SET",
			fmt.Sprintf("  state = CASE WHEN attempts > ?3 THEN '%s' ELSE '%s' END,", ir.QueueStateDead, ir.QueueStateReady),
			fmt.Sprintf("  due_at = CASE WHEN attempts > ?3 THEN due_at ELSE %s END,", sqliteIn(fmt.Sprintf("MIN(?4 * (1 << MIN(attempts - 1, %d)), ?6)", queueBackoffDoublings))),
			"  last_error = ?5, "+unclaim+", updated_at = "+sqliteNow,
			claimed,
			"RETURNING state",
		),
		Extend: lines(
			"UPDATE "+table+" SET claim_expires_at = "+sqliteIn("?3")+", updated_at = "+sqliteNow,
			claimed,
		),
		Release: lines(
			fmt.Sprintf("UPDATE %s SET state = '%s', attempts = MAX(attempts - 1, 0), %s, updated_at = %s", table, ir.QueueStateReady, unclaim, sqliteNow),
			claimed,
		),
		Prune: lines(
			"DELETE FROM "+table+" WHERE id IN (",
			"  SELECT id FROM "+table,
			fmt.Sprintf("  WHERE state = '%s' AND updated_at <= %s", ir.QueueStateDone, sqliteAgo("?1")),
			"  ORDER BY updated_at",
			"  LIMIT ?2",
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

// The kinds of value a queue's column, or each element of a list column,
// holds that a client converts on SQLite (D53, amended), where the column
// is TEXT or INTEGER: a UUID, written in its lowercase hyphenated text; a
// timestamp, written in RFC 3339 in UTC; and a boolean, written as 1 or 0
// in a column and as true or false in a list's JSON. The generators mark a
// field of each kind in a queue's definition, so the Go, TypeScript and
// Rust clients convert alike.
const (
	QueueKindUUID      = "uuid"
	QueueKindTimestamp = "timestamp"
	QueueKindBoolean   = "boolean"
)

// QueueKindOf is the kind of value a column of SQL type sqlType holds, or
// of each element of a list of it, as a client converts it on SQLite: one
// of the QueueKind constants, or "" for a value that crosses as it is
// (text, a number, a date's text, a duration's text).
func QueueKindOf(sqlType string) string {
	switch queueBaseType(sqlType) {
	case "UUID":
		return QueueKindUUID
	case "TIMESTAMPTZ", "TIMESTAMP", "TIMESTAMP WITH TIME ZONE", "TIMESTAMP WITHOUT TIME ZONE":
		return QueueKindTimestamp
	case "BOOLEAN", "BOOL":
		return QueueKindBoolean
	}
	return ""
}

// SQLiteQueueRefusal says why a queue's SQLite clients cannot hold a field
// whose column, or each element of whose list, is of SQL type sqlType, or
// is "" when they can: SQLite has no storage for a POINT or the PostGIS
// types, and bytes, a BLOB, have no one form the three clients' messages
// share.
func SQLiteQueueRefusal(sqlType string) string {
	switch base := queueBaseType(sqlType); base {
	case "POINT", "GEOGRAPHY", "GEOMETRY", "LTREE":
		return fmt.Sprintf("is a %s column, which SQLite has no storage for", base)
	case "BYTEA":
		return "is a BYTEA column, a BLOB on SQLite, which the SQLite queue clients do not hold yet"
	}
	return ""
}

// queueBaseType is a SQL type without its length or precision and its
// array brackets, in upper case: VARCHAR(255)[] is VARCHAR.
func queueBaseType(sqlType string) string {
	t := strings.ToUpper(strings.TrimSpace(sqlType))
	t = strings.TrimSuffix(t, "[]")
	if i := strings.IndexByte(t, '('); i >= 0 {
		t = strings.TrimSpace(t[:i])
	}
	return t
}
