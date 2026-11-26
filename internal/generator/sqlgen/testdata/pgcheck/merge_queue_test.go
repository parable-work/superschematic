package pgcheck

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// mergePlan is the merge sqlutil.MergePlanOf writes (D147), as
// TestMergeThroughKeyedQueuesOnPostgres encodes it in merge.json.
type mergePlan struct {
	Lock  string
	Steps []struct {
		Source, Field, SelfReference string
		Collisions                   []mergeStatement
		Repoint                      mergeStatement
	}
}

// mergeStatement is one statement of a merge, which takes the acting user
// as $3 where it names an audit field.
type mergeStatement struct {
	SQL, Actor string
}

// TestMergeQueueKeysOnPostgres applies create.sql for fixture-merge-queue-db
// and merges one account into another as the ORMs' mergeInto does: it locks
// both rows, deletes the referencing rows a unique index would refuse,
// repoints the rest and deletes the account merged away, in one
// transaction. Three keyed queues hold the accounts' keys (D80), and the
// merge's writes fire the triggers of two of them onto keys a message of
// the account that stays already holds: a ready one of a queue that
// replaces, a claimed one of a queue that serializes, and a ready one of a
// queue that skips. None is a unique violation: each trigger's insert
// takes its queue's conflict, so a key keeps one ready message and one
// claimed one, and the merge rewrites no message, so a message keyed by
// the account merged away stays as it was written.
// TestMergeThroughKeyedQueuesOnPostgres in the sqlgen package runs it with
// PGCHECK_DATABASE_URL and PGCHECK_SQL_DIR.
func TestMergeQueueKeysOnPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	conn := scratchDatabase(t, ctx, "superschematic_merge_queue")
	execScript(t, ctx, conn, "create.sql", readSQL(t, "create.sql"))
	var plan mergePlan
	if err := json.Unmarshal([]byte(readSQL(t, "merge.json")), &plan); err != nil {
		t.Fatalf("read merge.json: %v", err)
	}

	// names reads ids back as the names the checks give them.
	names := map[string]string{}
	insert := func(t *testing.T, name, sql string, args ...any) string {
		t.Helper()
		var id string
		if err := conn.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		names[id] = name
		return id
	}
	account := func(t *testing.T, name string) string {
		return insert(t, name, `INSERT INTO account (name) VALUES ($1) RETURNING id::text`, name)
	}
	contact := func(t *testing.T, name, of, channel string) string {
		return insert(t, name, `INSERT INTO contact (account_id, channel) VALUES ($1, $2) RETURNING id::text`, of, channel)
	}
	membership := func(t *testing.T, of, team string) {
		t.Helper()
		mustExec(t, ctx, conn, `INSERT INTO membership (account_id, team) VALUES ($1, $2)`, of, team)
	}
	// rows reads each row of sql, its columns as text joined by a slash,
	// ids as their names, sorted.
	rows := func(t *testing.T, sql string, args ...any) []string {
		t.Helper()
		found, err := conn.Query(ctx, sql, args...)
		if err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		defer found.Close()
		var out []string
		for found.Next() {
			values, err := found.Values()
			if err != nil {
				t.Fatalf("%s: %v", sql, err)
			}
			parts := make([]string, len(values))
			for i, value := range values {
				text, _ := value.(string)
				if name, ok := names[text]; ok {
					text = name
				}
				parts[i] = text
			}
			out = append(out, strings.Join(parts, "/"))
		}
		if err := found.Err(); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		sort.Strings(out)
		return out
	}
	want := func(t *testing.T, what string, got, want []string) {
		t.Helper()
		sort.Strings(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s:\n got  %q\n want %q", what, got, want)
		}
	}

	from, into := account(t, "from"), account(t, "into")

	// A sync of each account for one reason waits, as an enqueue writes it.
	for _, of := range []string{from, into} {
		mustExec(t, ctx, conn, `INSERT INTO account_synced_queue (account_id, reason) VALUES ($1, 'nightly')`, of)
	}
	// Each contact's insert enqueues its account's change on its channel:
	// from's two on email replace one another, and into's on email is
	// claimed by a worker before the merge.
	contact(t, "email1", from, "email")
	contact(t, "email2", from, "email")
	contact(t, "sms", from, "sms")
	held := contact(t, "held", into, "email")
	mustExec(t, ctx, conn, `UPDATE contact_changed_queue SET state = 'claimed', claim_token = gen_random_uuid(), claim_expires_at = now() + interval '5 minutes' WHERE contact_id = $1`, held)
	// Both accounts are on red, so the merge deletes from's place there;
	// from alone is on blue, where a change of into's waits already.
	membership(t, from, "red")
	membership(t, into, "red")
	membership(t, from, "blue")
	mustExec(t, ctx, conn, `INSERT INTO membership_changed_queue (account_id, team) VALUES ($1, 'blue')`, into)

	// The merge, as the ORMs run it.
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if locked := len(queryAll(t, ctx, tx, plan.Lock, from, into)); locked != 2 {
		t.Fatalf("the lock read %d rows, want both", locked)
	}
	run := func(t *testing.T, what string, statement mergeStatement) {
		t.Helper()
		args := []any{from, into}
		if statement.Actor != "" {
			args = append(args, nil)
		}
		if _, err := tx.Exec(ctx, statement.SQL, args...); err != nil {
			t.Fatalf("%s: %v\n%s", what, err, statement.SQL)
		}
	}
	for _, step := range plan.Steps {
		if step.SelfReference != "" && len(queryAll(t, ctx, tx, step.SelfReference, from, into)) > 0 {
			t.Fatalf("%s.%s: into references from", step.Source, step.Field)
		}
		for _, collision := range step.Collisions {
			run(t, "delete the rows of "+step.Source+" a unique index would refuse", collision)
		}
		run(t, "repoint "+step.Source+"."+step.Field, step.Repoint)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM account WHERE id = $1`, from); err != nil {
		t.Fatalf("delete from: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit the merge: %v", err)
	}

	want(t, "accounts", rows(t, `SELECT id::text FROM account`), []string{"into"})
	want(t, "contacts", rows(t, `SELECT id::text, account_id::text FROM contact`),
		[]string{"email1/into", "email2/into", "sms/into", "held/into"})
	want(t, "memberships", rows(t, `SELECT account_id::text, team FROM membership`),
		[]string{"into/blue", "into/red"})
	want(t, "syncs, which the merge leaves as they were", rows(t, `SELECT account_id::text, reason, state FROM account_synced_queue`),
		[]string{"from/nightly/ready", "into/nightly/ready"})
	// On into's email the repoint of email1 and email2, one statement,
	// enqueues twice: the second replaces the first, and both wait beside
	// the claimed one.
	changes := rows(t, `SELECT account_id::text, channel, state, contact_id::text FROM contact_changed_queue`)
	readyEmail := ""
	for _, change := range changes {
		if strings.HasPrefix(change, "into/email/ready/") {
			readyEmail = change
		}
	}
	if readyEmail != "into/email/ready/email1" && readyEmail != "into/email/ready/email2" {
		t.Errorf("into's ready change on email is %q, want email1's or email2's", readyEmail)
	}
	want(t, "contact changes", changes, []string{
		"from/email/ready/email2", "from/sms/ready/sms",
		"into/email/claimed/held", readyEmail, "into/sms/ready/sms",
	})
	// The delete of from's place on red enqueues from's change there; the
	// repoint of its place on blue finds into's waiting, and adds nothing.
	want(t, "membership changes", rows(t, `SELECT account_id::text, team, state FROM membership_changed_queue`),
		[]string{"from/red/ready", "into/blue/ready"})
}

// queryAll reads the first column of each row of sql as text.
func queryAll(t *testing.T, ctx context.Context, tx pgx.Tx, sql string, args ...any) []string {
	t.Helper()
	found, err := tx.Query(ctx, sql, args...)
	if err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	defer found.Close()
	var out []string
	for found.Next() {
		values, err := found.Values()
		if err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		text, _ := values[0].(string)
		out = append(out, text)
	}
	if err := found.Err(); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return out
}
