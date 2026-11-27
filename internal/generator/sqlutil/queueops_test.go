package sqlutil

import (
	"strings"
	"testing"
)

// TestPostgresQueueOperatorStatements holds the operator's statements on a
// queue's table to their text: the dead messages with their total, the
// states of the IDs given, and a retry and a drop of the IDs given or of
// every dead message, each touching dead messages alone.
func TestPostgresQueueOperatorStatements(t *testing.T) {
	s := PostgresQueueOperatorStatements(`"order_placed_queue"`, "")
	want := map[string]string{
		"dead": `SELECT q.id::text, q.attempts::bigint, COALESCE(q.last_error, ''),
  to_char(q.updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
  (to_jsonb(q) - ARRAY['id', 'state', 'attempts', 'due_at', 'claim_expires_at', 'claim_token', 'last_error', 'created_at', 'updated_at']::text[])::text,
  count(*) OVER ()
FROM "order_placed_queue" AS q
WHERE q.state = 'dead'
ORDER BY q.updated_at, q.id
LIMIT $1`,
		"states": `SELECT q.id::text, q.state FROM "order_placed_queue" AS q WHERE q.id = ANY($1::text[]::uuid[])`,
		"retry": `UPDATE "order_placed_queue" SET state = 'ready', attempts = 0, due_at = now(), claim_token = NULL, claim_expires_at = NULL, updated_at = now()
WHERE state = 'dead' AND id = ANY($1::text[]::uuid[])
RETURNING id::text`,
		"retryAll": `UPDATE "order_placed_queue" SET state = 'ready', attempts = 0, due_at = now(), claim_token = NULL, claim_expires_at = NULL, updated_at = now()
WHERE state = 'dead'
RETURNING id::text`,
		"drop": `DELETE FROM "order_placed_queue"
WHERE state = 'dead' AND id = ANY($1::text[]::uuid[])
RETURNING id::text`,
		"dropAll": `DELETE FROM "order_placed_queue"
WHERE state = 'dead'
RETURNING id::text`,
	}
	got := map[string]string{
		"dead": s.Dead, "states": s.States, "retry": s.Retry, "retryAll": s.RetryAll, "drop": s.Drop, "dropAll": s.DropAll,
	}
	for name, sql := range want {
		if got[name] != sql {
			t.Errorf("%s:\n%s\nwant:\n%s", name, got[name], sql)
		}
	}
	// Every statement that changes a message changes a dead one.
	for name, sql := range got {
		if strings.HasPrefix(sql, "UPDATE") || strings.HasPrefix(sql, "DELETE") {
			if !strings.Contains(sql, "WHERE state = 'dead'") {
				t.Errorf("%s changes messages that are not dead:\n%s", name, sql)
			}
		}
	}
	if s.DropStates != s.States {
		t.Errorf("a drop's states on Postgres are\n%s\nnot\n%s", s.DropStates, s.States)
	}
}

// TestSQLiteQueueOperatorStatements holds the operator's statements on
// SQLite and D1 to what they differ by from Postgres's (D68): a listing
// that names the message's columns, nests a JSON column and writes a
// boolean as JSON's; the IDs named as a JSON array; a retry and a drop of
// the IDs named that change each of them or none; and states that leave
// out a message the operation would have changed. Every statement that
// changes a message changes a dead one.
func TestSQLiteQueueOperatorStatements(t *testing.T) {
	message := []QueueMessageColumn{
		QueueMessageColumnOf("order_id", "UUID"),
		QueueMessageColumnOf("tags", "TEXT[]"),
		QueueMessageColumnOf("ship_to", "JSONB"),
		QueueMessageColumnOf("gift", "BOOLEAN"),
		QueueMessageColumnOf("note", "VARCHAR(200)"),
	}
	if got := []string{message[0].Holds, message[1].Holds, message[2].Holds, message[3].Holds, message[4].Holds}; strings.Join(got, ",") != ",json,json,boolean," {
		t.Errorf("the columns hold %q", got)
	}
	s := SQLiteQueueOperatorStatements("order_placed_queue", message)
	for _, want := range []string{
		`json_object('order_id', q."order_id", 'tags', CASE WHEN json_valid(q."tags") THEN json(q."tags") ELSE q."tags" END, 'ship_to', CASE WHEN json_valid(q."ship_to") THEN json(q."ship_to") ELSE q."ship_to" END, 'gift', json(CASE WHEN q."gift" IS NULL THEN NULL WHEN q."gift" THEN 'true' ELSE 'false' END), 'note', q."note")`,
		"strftime('%Y-%m-%dT%H:%M:%SZ', q.updated_at)",
		"LIMIT ?1",
	} {
		if !strings.Contains(s.Dead, want) {
			t.Errorf("the listing holds no %s:\n%s", want, s.Dead)
		}
	}
	guard := "(SELECT count(*) FROM (SELECT id FROM order_placed_queue WHERE state = 'dead' AND id IN (SELECT value FROM json_each(?1)))) = json_array_length(?1)"
	for name, sql := range map[string]string{"retry": s.Retry, "drop": s.Drop} {
		if !strings.Contains(sql, guard) || strings.Contains(sql, "$1") {
			t.Errorf("the %s changes the messages named unless each one is dead:\n%s", name, sql)
		}
	}
	if s.States != s.DropStates || !strings.HasPrefix(s.States, "SELECT q.id, CASE WHEN q.id IN (SELECT id FROM order_placed_queue WHERE state = 'dead' AND id IN (SELECT value FROM json_each(?1))) THEN '' ELSE q.state END") {
		t.Errorf("the states are\n%s\nand\n%s", s.States, s.DropStates)
	}
	keyed := SQLiteQueueOperatorStatements("stock_checked_queue", nil, "sku")
	if keyed.States == keyed.DropStates || !strings.Contains(keyed.States, "THEN 'dead, and a ready message holds its key'") || !strings.Contains(keyed.Retry, "row_number() OVER (PARTITION BY c.sku ORDER BY c.updated_at DESC, c.id) AS n") {
		t.Errorf("a keyed queue's retry and states:\n%s\n%s", keyed.Retry, keyed.States)
	}
	if !strings.Contains(keyed.Dead, "json_object()") {
		t.Errorf("a listing of no columns is %s", keyed.Dead)
	}
	for _, statements := range []QueueOperatorStatements{s, keyed} {
		for name, sql := range map[string]string{"retry": statements.Retry, "retryAll": statements.RetryAll, "drop": statements.Drop, "dropAll": statements.DropAll} {
			if !strings.Contains(sql, "state = 'dead'") {
				t.Errorf("%s changes messages that are not dead:\n%s", name, sql)
			}
		}
	}
}
