package sqlutil

import (
	"fmt"
	"slices"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// QueueOperatorStatements are the statements an operator runs on a queue's
// table, beside the claims (QueueStatements): they read the messages that
// are dead, make them ready again and delete them (`superschematic stack
// queue`, docs/stack-model.md, section 8.8). The migration runner runs them
// as the command's document carries them (`superschematic-migrate queue`),
// so this package is their one writer.
// PostgresQueueOperatorStatements writes them on Postgres, and
// SQLiteQueueOperatorStatements on SQLite and D1, as the claims are written
// in both (SQLiteQueueStatements).
//
// Each statement that takes messages by ID takes them as one parameter: on
// Postgres $1, a text array of their IDs, which pgx and node-postgres both
// bind from a list of strings and the statement casts to UUIDs; on SQLite
// ?1, the text of a JSON array of them, which the statement reads with
// json_each. Each reads and returns what the runner's driver scans: text and
// 64-bit integers, never NULL. Times are the database's clock.
type QueueOperatorStatements struct {
	// Dead lists up to $1 dead messages, the oldest death first, and
	// returns, per message, its ID, its attempts, its last error (empty
	// when it has none), when it died as RFC 3339 text in UTC to the
	// second, the message as a JSON object of its columns, and the number
	// of dead messages in all. A dead message changes no more, so its
	// updated_at is when it died.
	Dead string

	// States returns the ID and state of each message among $1 the table
	// holds, so a retry that missed one can say why, and DropStates so a
	// drop can. On Postgres the two are one statement. On SQLite, whose
	// retry and drop change every message named or none in one statement
	// (D1 has no transaction a refusal could roll back), each returns, in
	// place of the state, an empty text for a message the operation would
	// have changed had none of the others stopped it, and a retry's says
	// of a dead message whose key a ready message holds what Postgres's
	// says.
	States     string
	DropStates string

	// Retry makes each dead message among $1 ready again: due now, with no
	// attempts, so its retries start over, and no claim. Its last error
	// stays until its next failure replaces it. It returns the IDs it
	// changed. RetryAll does so to every dead message. On a queue that
	// keeps one ready message per key (D80), a retry leaves dead a message
	// whose key a ready message holds, and of the dead messages of one key
	// it retries the one that died last; States says so of each it left.
	// On SQLite, Retry changes none of the messages named unless it can
	// change each one.
	Retry    string
	RetryAll string

	// Drop deletes each dead message among $1 and returns the IDs it
	// deleted. DropAll deletes every dead message. On SQLite, Drop deletes
	// none of the messages named unless each one is dead.
	Drop    string
	DropAll string
}

// PostgresQueueOperatorStatements are the operator's statements on the
// queue whose table is table, quoted for SQL as QuoteIdentifier quotes it.
// They read the columns every queue keeps (ir.QueueColumns), and the
// message's as the row's other columns, so they need no list of them. keys
// are the quoted columns of the key of a queue that keeps one ready message
// per key (D80), one or several, or none for every other queue.
func PostgresQueueOperatorStatements(table string, keys ...string) QueueOperatorStatements {
	keys = slices.DeleteFunc(slices.Clone(keys), func(k string) bool { return k == "" })
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
	s.DropStates = s.States
	if len(keys) > 0 {
		// One dead message per key, the last to die, and none whose key a
		// ready message holds.
		key := strings.Join(keys, ", ")
		free := fmt.Sprintf("NOT EXISTS (SELECT 1 FROM %s AS p WHERE %s AND p.state = '%s')", table, QueueKeyEquals("p", table, keys), ir.QueueStateReady)
		last := func(where string) string {
			return fmt.Sprintf("id IN (SELECT DISTINCT ON (%s) id FROM %s WHERE %s ORDER BY %s, updated_at DESC, id)", key, table, where, key)
		}
		s.States = lines(
			fmt.Sprintf("SELECT q.id::text, CASE WHEN q.state = '%s' AND EXISTS (SELECT 1 FROM %s AS p WHERE %s AND p.state = '%s')", ir.QueueStateDead, table, QueueKeyEquals("p", "q", keys), ir.QueueStateReady),
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

// QueueMessageColumn is a column of a queue's message as the operator's
// listing on SQLite reads it (SQLiteQueueOperatorStatements): its name,
// unquoted, and what its value holds, which the listing writes into the
// message's JSON object as Postgres's to_jsonb writes the column's type.
type QueueMessageColumn struct {
	Name  string
	Holds string
}

// What a message's column holds on SQLite that its JSON is not the
// column's value as it is (QueueMessageColumn.Holds): the JSON text of a
// list or a JSON value, which the listing nests; a boolean, an integer
// SQLite holds as 0 or 1, which it writes as false or true; and bytes, a
// BLOB, which it writes as Postgres's text of a bytea, \x and hex. Any
// other column's value, text or a number, is a JSON string or number.
const (
	QueueHoldsJSON    = "json"
	QueueHoldsBoolean = "boolean"
	QueueHoldsBytes   = "bytes"
)

// QueueMessageColumnOf is the message's column name of Postgres type pg,
// as sqlgen writes it: a list or a JSON value holds JSON, as SQLite stores
// them (D27), a BOOLEAN a boolean and a BYTEA bytes.
func QueueMessageColumnOf(name, pg string) QueueMessageColumn {
	c := QueueMessageColumn{Name: name}
	t := strings.ToUpper(strings.TrimSpace(pg))
	switch base := queueBaseType(t); {
	case strings.HasSuffix(t, "[]"), base == "JSONB", base == "JSON":
		c.Holds = QueueHoldsJSON
	case base == "BOOLEAN":
		c.Holds = QueueHoldsBoolean
	case base == "BYTEA":
		c.Holds = QueueHoldsBytes
	}
	return c
}

// sqliteMessageValue is the value the listing on SQLite writes into the
// message's JSON object for the column c of the row q. A JSON column whose
// text is no JSON, which Postgres's JSONB cannot hold and SQLite's text
// can, is written as that text, so one bad row does not fail the listing
// of every dead message.
func sqliteMessageValue(c QueueMessageColumn) string {
	// Quoted always, since SQLite's keywords are not Postgres's.
	column := `q."` + strings.ReplaceAll(c.Name, `"`, `""`) + `"`
	switch c.Holds {
	case QueueHoldsJSON:
		return fmt.Sprintf("CASE WHEN json_valid(%[1]s) THEN json(%[1]s) ELSE %[1]s END", column)
	case QueueHoldsBoolean:
		return fmt.Sprintf("json(CASE WHEN %[1]s IS NULL THEN NULL WHEN %[1]s THEN 'true' ELSE 'false' END)", column)
	case QueueHoldsBytes:
		return fmt.Sprintf(`CASE WHEN %[1]s IS NULL THEN NULL ELSE '\x' || lower(hex(%[1]s)) END`, column)
	}
	return column
}

// SQLiteQueueOperatorStatements are the operator's statements on the
// queue whose table is table on SQLite and D1, quoted as QuoteIdentifier
// quotes it, as the claims on SQLite are given it (D68, D96). message are
// the columns of the queue's message in field order, which the listing
// names one by one, as SQLite has no to_jsonb of a row. keys are the quoted
// columns of the key of a queue that keeps one ready message per key (D80),
// or none for every other queue.
//
// They do what Postgres's do, with these differences:
//   - The messages named are ?1, the text of a JSON array of their IDs,
//     which each statement reads with json_each.
//   - A retry or a drop of the messages named is one statement that changes
//     each of them or none: it counts the messages it would change and
//     changes them only when that count is the number named. D1 runs each
//     statement on its own, with no transaction a refusal could roll back,
//     so the statement itself is all or nothing.
//   - States and DropStates return an empty state for each message the
//     operation would have changed, so a refusal names only the messages
//     that stopped it, as Postgres's does.
//   - Of a key's dead messages, the one that died last is the first of its
//     key by a window function, where Postgres takes DISTINCT ON.
//   - Times are the text SQLite's queue columns hold (sqliteNow), and a
//     death is written to the second in UTC, as Postgres writes it.
func SQLiteQueueOperatorStatements(table string, message []QueueMessageColumn, keys ...string) QueueOperatorStatements {
	keys = slices.DeleteFunc(slices.Clone(keys), func(k string) bool { return k == "" })
	object := make([]string, 0, 2*len(message))
	for _, c := range message {
		object = append(object, QuoteLiteral(c.Name), sqliteMessageValue(c))
	}
	named := "id IN (SELECT value FROM json_each(?1))"
	dead := fmt.Sprintf("state = '%s'", ir.QueueStateDead)
	retry := fmt.Sprintf("UPDATE %s SET state = '%s', attempts = 0, due_at = %s, claim_token = NULL, claim_expires_at = NULL, updated_at = %s", table, ir.QueueStateReady, sqliteNow, sqliteNow)
	// all holds a statement to the messages named only when chosen, the
	// messages it would change, is every one of them.
	all := func(chosen string) string {
		return fmt.Sprintf("(SELECT count(*) FROM (%s)) = json_array_length(?1)", chosen)
	}
	// states is a States statement: an empty state for each message named
	// that chosen holds, else the reason the CASE's other arms give, else
	// its state.
	states := func(chosen string, reasons ...string) string {
		arms := append([]string{fmt.Sprintf("WHEN q.id IN (%s) THEN ''", chosen)}, reasons...)
		return lines(
			"SELECT q.id, CASE "+strings.Join(arms, " ")+" ELSE q.state END",
			"FROM "+table+" AS q WHERE q."+named,
		)
	}
	deadNamed := fmt.Sprintf("SELECT id FROM %s WHERE %s AND %s", table, dead, named)
	s := QueueOperatorStatements{
		Dead: lines(
			"SELECT q.id, q.attempts, COALESCE(q.last_error, ''),",
			"  strftime('%Y-%m-%dT%H:%M:%SZ', q.updated_at),",
			"  json_object("+strings.Join(object, ", ")+"),",
			"  count(*) OVER ()",
			"FROM "+table+" AS q",
			"WHERE q."+dead,
			"ORDER BY q.updated_at, q.id",
			"LIMIT ?1",
		),
		States:     states(deadNamed),
		DropStates: states(deadNamed),
		Retry: lines(
			retry,
			"WHERE "+dead+" AND "+named,
			"  AND "+all(deadNamed),
			"RETURNING id",
		),
		RetryAll: lines(
			retry,
			"WHERE "+dead,
			"RETURNING id",
		),
		Drop: lines(
			"DELETE FROM "+table,
			"WHERE "+dead+" AND "+named,
			"  AND "+all(deadNamed),
			"RETURNING id",
		),
		DropAll: lines(
			"DELETE FROM "+table,
			"WHERE "+dead,
			"RETURNING id",
		),
	}
	if len(keys) > 0 {
		// One dead message per key, the last to die, and none whose key a
		// ready message holds.
		key := make([]string, len(keys))
		for i, k := range keys {
			key[i] = "c." + k
		}
		// held is whether a ready message holds the key of the row alias.
		held := func(alias string) string {
			return fmt.Sprintf("EXISTS (SELECT 1 FROM %s AS p WHERE %s AND p.state = '%s')", table, QueueKeyEquals("p", alias, keys), ir.QueueStateReady)
		}
		last := func(where string) string {
			return lines(
				"SELECT id FROM (",
				fmt.Sprintf("  SELECT c.id, row_number() OVER (PARTITION BY %s ORDER BY c.updated_at DESC, c.id) AS n", strings.Join(key, ", ")),
				fmt.Sprintf("  FROM %s AS c WHERE c.%s AND %sNOT %s", table, dead, where, held("c")),
				") WHERE n = 1",
			)
		}
		chosen := last("c." + named + " AND ")
		// A dead message the retry leaves is one whose key a ready message
		// holds, or the message the retry makes ready would: what
		// Postgres's States finds after its retry, in its transaction.
		s.States = states(chosen, fmt.Sprintf("WHEN q.%s AND (%s OR EXISTS (SELECT 1 FROM %s AS p WHERE %s AND p.id IN (%s))) THEN 'dead, and a ready message holds its key'",
			dead, held("q"), table, QueueKeyEquals("p", "q", keys), chosen))
		s.Retry = lines(
			retry,
			"WHERE id IN ("+chosen+")",
			"  AND "+all(chosen),
			"RETURNING id",
		)
		s.RetryAll = lines(
			retry,
			"WHERE id IN ("+last("")+")",
			"RETURNING id",
		)
	}
	return s
}
