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
}
