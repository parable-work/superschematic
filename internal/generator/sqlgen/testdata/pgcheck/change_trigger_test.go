package pgcheck

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// changeMessage is a message of task_change_queue as the change trigger
// wrote it (D103).
type changeMessage struct {
	write         string
	at            int64
	before, after map[string]any
}

// TestChangeTriggerOnPostgres applies the create.sql of fixture-reactions-db
// with Task given an updatedAt audit field, a deletedAt one, a location
// stored as a POINT and enough own fields that its image passes one
// jsonb_build_object call (D103): the change trigger writes a message for
// an insert, an update of a column of the image, a soft delete and a
// restore, which move deleted_at alone, and a delete, each with the write,
// the database's clock and the row's images by column, and none for an
// update of updated_at alone or of the location, which the image leaves
// out, a POINT having no equality to compare it with; a write of the
// location never fails; and one row's messages are created, and fall due,
// in the order they were written, within one transaction too.
func TestChangeTriggerOnPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	conn := scratchDatabase(t, ctx, "superschematic_change_trigger")

	execScript(t, ctx, conn, "create.sql", readSQL(t, "create.sql"))
	var project, task string
	if err := conn.QueryRow(ctx, `INSERT INTO project ("name", status) VALUES ('Launch', 'planned') RETURNING id::text`).Scan(&project); err != nil {
		t.Fatalf("insert a project: %v", err)
	}
	start := time.Now().Add(-time.Minute).UnixMilli()
	if err := conn.QueryRow(ctx, `INSERT INTO task (title, project_id, status, extra_60, location) VALUES ('Write it', $1, 'todo', 'sixty', point(1, 2)) RETURNING id::text`, project).Scan(&task); err != nil {
		t.Fatalf("insert a task: %v", err)
	}
	mustExec(t, ctx, conn, `UPDATE task SET status = 'doing', status_entered_at = now() WHERE id = $1`, task)
	mustExec(t, ctx, conn, `UPDATE task SET updated_at = now() + interval '1 hour' WHERE id = $1`, task)
	mustExec(t, ctx, conn, `UPDATE task SET location = point(3, 4) WHERE id = $1`, task)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{`UPDATE task SET deleted_at = now() WHERE id = $1`, `UPDATE task SET deleted_at = NULL WHERE id = $1`} {
		if _, err := tx.Exec(ctx, statement, task); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	mustExec(t, ctx, conn, `DELETE FROM task WHERE id = $1`, task)

	var ordered bool
	if err := conn.QueryRow(ctx, `SELECT bool_and(created_at = due_at) AND count(DISTINCT created_at) = count(*) FROM task_change_queue WHERE row_key = $1`, task).Scan(&ordered); err != nil || !ordered {
		t.Errorf("the task's messages are not each created and due at its own write's clock: %v", err)
	}
	rows, err := conn.Query(ctx, `SELECT "write", "at", "before", "after" FROM task_change_queue WHERE row_key = $1 ORDER BY created_at`, task)
	if err != nil {
		t.Fatal(err)
	}
	var messages []changeMessage
	for rows.Next() {
		var m changeMessage
		var before, after []byte
		if err := rows.Scan(&m.write, &m.at, &before, &after); err != nil {
			t.Fatal(err)
		}
		for _, image := range []struct {
			raw  []byte
			into *map[string]any
		}{{before, &m.before}, {after, &m.after}} {
			if image.raw != nil {
				if err := json.Unmarshal(image.raw, image.into); err != nil {
					t.Fatal(err)
				}
			}
		}
		messages = append(messages, m)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	var writes []string
	for _, m := range messages {
		writes = append(writes, m.write)
	}
	if fmt.Sprint(writes) != "[insert update update update delete]" {
		t.Fatalf("the trigger wrote %v, want an insert, an update, a soft delete's update, a restore's update and a delete: the update of updated_at alone writes none, and of the location none", writes)
	}
	insert, update, del := messages[0], messages[1], messages[4]
	if insert.at < start || update.at < insert.at || messages[2].at < update.at || messages[3].at < messages[2].at || del.at < messages[3].at {
		t.Errorf("the writes' times are %d, %d, %d, %d and %d, want each at or after the last, from %d", insert.at, update.at, messages[2].at, messages[3].at, del.at, start)
	}
	// A soft delete and a restore move deleted_at alone, which the image
	// leaves out: each image is the row's.
	for _, m := range messages[2:4] {
		if _, ok := m.after["deleted_at"]; ok || m.after["status"] != "doing" || fmt.Sprint(m.before) != fmt.Sprint(m.after) {
			t.Errorf("a soft delete's or a restore's images = %v and %v", m.before, m.after)
		}
	}
	if insert.before != nil || del.after != nil {
		t.Errorf("an insert's before is %v and a delete's after %v, want none", insert.before, del.after)
	}
	if insert.after["title"] != "Write it" || insert.after["project_id"] != project || insert.after["status"] != "todo" || insert.after["status_entered_at"] != nil {
		t.Errorf("the insert's after = %v", insert.after)
	}
	// The image is by column: the key, _version and the audit columns are
	// not in it, and its 65 columns pass one call of 50.
	for _, left := range []string{"id", "_version", "updated_at", "deleted_at", "location"} {
		if _, ok := insert.after[left]; ok {
			t.Errorf("the image holds %s: %v", left, insert.after)
		}
	}
	if len(insert.after) != 65 || insert.after["extra_60"] != "sixty" {
		t.Errorf("the image holds %d columns, extra_60 %v, want 65 and sixty", len(insert.after), insert.after["extra_60"])
	}
	if update.before["status"] != "todo" || update.after["status"] != "doing" || update.after["status_entered_at"] == nil {
		t.Errorf("the update's images = %v and %v", update.before, update.after)
	}
	if del.before["status"] != "doing" || del.before["title"] != "Write it" {
		t.Errorf("the delete's before = %v", del.before)
	}

	execScript(t, ctx, conn, "drop.sql", readSQL(t, "drop.sql"))
	var left int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_proc WHERE proname IN ('project_changes', 'task_changes')`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("drop.sql left %d change functions: %v", left, err)
	}
}
