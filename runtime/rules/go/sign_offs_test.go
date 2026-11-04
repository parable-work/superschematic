package rules_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	rules "github.com/parable-work/superschematic/runtime/rules/go"
	"github.com/parable-work/superschematic/runtime/rules/go/internal/rulestest"
)

// A table whose behaviors keep a sub-table and arm and hear nothing
// (D104), through the adapter on the archive's core over a real database
// of each dialect: Task as the generator lays it out for
// fixture-approvals-db, implementing Constants, StateRules and Approvals
// beside its Workflow on status, its sign-offs in the sub-table
// task_approvals_sign_offs and no deadline queue or change queue. Nothing
// of the adapter's names a behavior: it inserts, reads, updates and
// deletes the sub-table's rows as the core's change sets say, runs an
// operation that writes nothing as one, reads the audit field createdBy
// into the row's own fields for a by that names it, and reads a boolean
// and a number column back as the core's JSON, SQLite's 0 and 1 a boolean.

// carolColumn is the UUID of the task's reviewer, hyphenated as its column
// holds it; alice (aliceColumn) creates the task and bob (bobColumn) owns
// it.
const carolColumn = "3b241101-e2bb-4255-8caf-4136c566a962"

// The configs of Task's uses, as fixture-approvals-db writes them.
const (
	taskConstants  = `{"fields":["owner"],"oneWay":{"urgent":"up","attempts":"up"},"permission":"tasks.admin"}`
	taskStateRules = `{"states":{"planned":{"frozen":["plan"],"by":["createdBy"]},"working":{"frozen":["plan"],"by":["reviewer"]},"proving":{"required":["proof"]}}}`
	taskApprovals  = `{"gates":{"proving":{"count":1,"permission":"tasks.review","resetOn":[]},"done":{"count":1,"permission":"tasks.review","resetOn":["proof"],"pinned":true}}}`
	taskWorkflow   = `{"states":["planned","working","proving","done","cancelled"],"transitions":[{"from":"planned","to":"working","by":["owner"]},{"from":"working","to":"proving","by":["owner"]},{"from":"proving","to":"working"},{"from":"proving","to":"done"},{"from":"planned","to":"cancelled"},{"from":"working","to":"cancelled"}]}`
)

// signedTaskLayout is Task's layout as behaviorlayout writes it: its own
// columns in column order, the audit field createdBy, the three uses it
// implements before its Workflow, and Approvals' sub-table.
func signedTaskLayout() *rules.Layout {
	column := func(field, name, typ string) rules.Column {
		return rules.Column{Field: field, Column: name, Type: typ}
	}
	owned := func(field, name, typ string) rules.OwnedField {
		out := rules.OwnedField{Name: field, Column: column(field, name, typ)}
		if typ == "Temporal.DateTime" {
			out.Convert = rules.ConvertEpochMillis
		}
		return out
	}
	implemented := func(behavior string) rules.Use {
		return rules.Use{ID: behavior, Behavior: behavior, Member: strings.ToLower(behavior), Fields: []rules.OwnedField{}, Operations: []rules.Operation{}}
	}
	approvals := implemented("Approvals")
	approvals.Operations = []rules.Operation{{Name: "signOff", Writes: true}, {Name: "withdrawSignOff", Writes: true}, {Name: "listSignOffs"}}
	approvals.Tables = []rules.SubTable{{
		Name:  "signOffs",
		Table: "task_approvals_sign_offs",
		Row:   "task_id",
		Key:   owned("number", "number", "string"),
		Fields: []rules.OwnedField{
			owned("state", "state", "string"),
			owned("subject", "subject", "string"),
			owned("note", "note", "string"),
			owned("at", "at", "Temporal.DateTime"),
			owned("consumedAt", "consumed_at", "Temporal.DateTime"),
		},
	}}
	status := owned("status", "status", "string")
	status.Value = true
	table := &rules.Table{
		Type:    "Task",
		Table:   "task",
		Key:     column("id", "id", "Identity.UUID"),
		Version: "_version",
		Own: []rules.Column{
			column("title", "title", "string"),
			column("owner", "owner", "Identity.UUID"),
			column("reviewer", "reviewer", "Identity.UUID"),
			column("plan", "plan", "string"),
			column("proof", "proof", "string"),
			column("urgent", "urgent", "boolean"),
			column("attempts", "attempts", "number"),
		},
		Audit: []rules.Column{column("createdBy", "created_by", "Identity.UUID")},
		Uses: []rules.Use{
			implemented("Constants"),
			implemented("StateRules"),
			approvals,
			{
				ID: "status", Behavior: "Workflow", Member: "status", Anchor: "status",
				Fields:     []rules.OwnedField{status, owned("enteredAt", "status_entered_at", "Temporal.DateTime")},
				Operations: []rules.Operation{{Name: "transition", Writes: true}},
			},
		},
		Spec: rules.TypeSpec{
			Name: "Task",
			Uses: []rules.UseSpec{
				{Behavior: "Constants", Config: json.RawMessage(taskConstants)},
				{Behavior: "StateRules", Config: json.RawMessage(taskStateRules)},
				{Behavior: "Approvals", Config: json.RawMessage(taskApprovals)},
				{Behavior: "Workflow", Config: json.RawMessage(taskWorkflow), Anchor: rules.Anchor{Field: "status"}},
			},
			Fields: []rules.FieldSpec{
				{Name: "title", Type: "string"},
				{Name: "owner", Type: "Identity.UUID"},
				{Name: "reviewer", Type: "Identity.UUID"},
				{Name: "plan", Type: "string"},
				{Name: "proof", Type: "string"},
				{Name: "urgent", Type: "boolean"},
				{Name: "attempts", Type: "number"},
				{Name: "createdBy", Type: "Identity.UUID"},
			},
		},
	}
	return &rules.Layout{Types: map[string]*rules.Table{"Task": table}}
}

// createSignedTasks writes Task's tables as sqlgen writes them: the row's
// table, its UUID columns uuid on Postgres and TEXT on SQLite, with
// @optimistic's trigger, and the sub-table, a child of the row deleted with
// it, with a surrogate id and the unique index of (row, number). No queue.
func createSignedTasks(t *testing.T, db *rulestest.DB) {
	t.Helper()
	types := db.ColumnTypes()
	uuid, surrogate := "TEXT", "TEXT DEFAULT (lower(hex(randomblob(16))))"
	if db.Dialect == rules.DialectPostgres {
		uuid, surrogate = "UUID", "UUID DEFAULT gen_random_uuid()"
	}
	db.Exec(t, fmt.Sprintf(`CREATE TABLE "task" (id %s PRIMARY KEY, title %s, owner %s, reviewer %s, plan %s, proof %s, urgent %s, attempts %s, created_by %s, status %s, status_entered_at %s, _version %s)`,
		uuid, types.Text, uuid, uuid, types.Text, types.Text, types.Boolean, types.Number, uuid, types.Text, types.Timestamp, types.Version))
	for _, ddl := range db.BumpVersion("task") {
		db.Exec(t, ddl)
	}
	db.Exec(t, fmt.Sprintf(`CREATE TABLE "task_approvals_sign_offs" (
  id %s NOT NULL PRIMARY KEY,
  task_id %s NOT NULL REFERENCES "task" (id) ON DELETE CASCADE,
  number %s NOT NULL,
  state %s,
  subject %s,
  note %s,
  at %s,
  consumed_at %s,
  UNIQUE (task_id, number)
)`, surrogate, uuid, types.Text, types.Text, types.Text, types.Text, types.Timestamp, types.Timestamp))
}

func TestASubTableWithNoQueueIsWrittenAndReadAsTheCoreSays(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createSignedTasks(t, db)
		layout := signedTaskLayout()
		ctx := context.Background()
		alice, bob, carol := wire(t, aliceColumn), wire(t, bobColumn), wire(t, carolColumn)
		asAlice, asBob := rules.Caller{Subject: alice}, rules.Caller{Subject: bob}
		asCarol := rules.Caller{Subject: carol, Permissions: []string{"tasks.review"}}

		// run runs fn on the core over a transaction of its own, recording
		// what it applied.
		run := func(fn func(host rules.Host) error) (*recorded, error) {
			t.Helper()
			var rec *recorded
			err := inTx(t, db, coreHost(layout), func(_ *rulestest.Tx, store rules.Store) rules.Store {
				wrapped, r := record(store)
				rec = r
				return wrapped
			}, func(host rules.Host, _ *rulestest.Tx) error { return fn(host) })
			return rec, err
		}
		update := func(caller rules.Caller, patch map[string]any) error {
			t.Helper()
			_, err := run(func(host rules.Host) error { return rules.RunUpdate(ctx, host, "Task", uuidKey, patch, caller, nil) })
			return err
		}
		operation := func(caller rules.Caller, name, params string) (json.RawMessage, *recorded, error) {
			t.Helper()
			var out json.RawMessage
			rec, err := run(func(host rules.Host) error {
				var err error
				out, err = rules.RunOperation(ctx, host, "Task", uuidKey, "Approvals", name, json.RawMessage(params), caller, nil)
				return err
			})
			return out, rec, err
		}
		read := func() *rules.Row {
			t.Helper()
			var row *rules.Row
			if _, err := run(func(host rules.Host) error {
				var err error
				row, err = rules.ReadRow(ctx, host, "Task", uuidKey)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if row == nil {
				t.Fatal("the task is gone")
			}
			return row
		}
		signOffs := func() string {
			t.Helper()
			var rows []rules.SubRow
			if _, err := run(func(host rules.Host) error {
				var err error
				rows, err = rules.ReadSubRows(ctx, host, "Task", uuidKey, "Approvals", "signOffs")
				return err
			}); err != nil {
				t.Fatal(err)
			}
			return string(jsonOf(t, rows))
		}
		vetoed := func(what string, err error, code, details string) {
			t.Helper()
			refusal := refusalOf(t, err)
			var got struct {
				VetoCode    string          `json:"vetoCode"`
				VetoDetails json.RawMessage `json:"vetoDetails"`
			}
			if jsonErr := json.Unmarshal(refusal.Details, &got); jsonErr != nil {
				t.Fatalf("%s: details %s: %v", what, refusal.Details, jsonErr)
			}
			if refusal.Code != rules.CodeVetoed || refusal.Status != 409 || got.VetoCode != code || !sameJSON(got.VetoDetails, []byte(details)) {
				t.Errorf("%s refused with %+v, want vetoed %s with %s", what, *refusal, code, details)
			}
		}

		// The create, then createdBy as the repository's create stamps it,
		// past the adapter.
		if _, err := run(func(host rules.Host) error {
			_, err := rules.RunCreate(ctx, host, "Task", map[string]any{"title": "Launch", "owner": bob, "reviewer": carol, "plan": "First plan", "urgent": false, "attempts": 0}, asAlice, &rules.CreateOptions{ID: uuidKey})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		db.Exec(t, fmt.Sprintf(`UPDATE "task" SET created_by = %s WHERE id = %s`, db.P(1), db.P(2)), aliceColumn, uuidColumn)
		wantJSON(t, "the created task", read(), fmt.Sprintf(`{"own":{"title":"Launch","owner":%q,"reviewer":%q,"plan":"First plan","urgent":false,"attempts":0,"createdBy":%q},"owned":{"status":{"status":"planned","enteredAt":5000}},"version":2}`, bob, carol, alice))

		// A boolean and a number read back as the core's JSON, so the
		// one-way order reads them.
		if err := update(asBob, map[string]any{"urgent": true, "attempts": 2}); err != nil {
			t.Fatal(err)
		}
		if got := read(); string(got.Own["urgent"]) != "true" || !sameJSON(got.Own["attempts"], []byte("2")) {
			t.Errorf("urgent and attempts = %s and %s, want true and 2", got.Own["urgent"], got.Own["attempts"])
		}
		vetoed("bob's urgent back", update(asBob, map[string]any{"urgent": false}), "one_way", `{"field":"urgent","direction":"up","current":true,"attempted":false}`)

		// createdBy, read into the row's own fields, names who may change
		// what planned freezes.
		vetoed("bob's plan", update(asBob, map[string]any{"plan": "Bob's plan"}), "frozen_fields", `{"state":"planned","fields":["plan"],"by":["createdBy"]}`)
		if err := update(asAlice, map[string]any{"plan": "Second plan"}); err != nil {
			t.Fatalf("alice's plan: %v", err)
		}

		// A sign-off inserts a row of the sub-table and writes the row; a
		// list of them writes nothing.
		before := read().Version
		if _, _, err := operation(asCarol, "signOff", `{"state":"proving","note":"Planned well"}`); err != nil {
			t.Fatalf("carol's sign-off for proving: %v", err)
		}
		signed := read().Version
		if signed != before+1 {
			t.Errorf("the version after a sign-off = %d, want %d", signed, before+1)
		}
		wantJSON(t, "the sign-offs", json.RawMessage(signOffs()), fmt.Sprintf(`[{"key":"0000000001","values":{"state":"proving","subject":%q,"note":"Planned well","at":5000}}]`, carol))
		listed, rec, err := operation(asCarol, "listSignOffs", `{}`)
		if err != nil {
			t.Fatalf("carol's list: %v", err)
		}
		wantJSON(t, "the list", listed, fmt.Sprintf(`{"items":[{"state":"proving","subject":%q,"note":"Planned well","at":5000}],"next":null}`, carol))
		for i, unit := range rec.applied {
			if len(unit) > 0 {
				t.Errorf("the list's unit %d applied %d statements, want none: %+v", i, len(unit), unit)
			}
		}
		if got := read().Version; got != signed {
			t.Errorf("the version after a list = %d, want %d", got, signed)
		}

		// A pinned sign-off names the version read; a change of the field
		// its gate's resetOn names consumes it by an update of its row, and
		// the gate whose resetOn is [] keeps its own.
		if _, _, err := operation(asCarol, "signOff", fmt.Sprintf(`{"state":"done","version":%d}`, signed)); err != nil {
			t.Fatalf("carol's sign-off for done: %v", err)
		}
		if err := update(asBob, map[string]any{"proof": "Screenshots"}); err != nil {
			t.Fatalf("bob's proof: %v", err)
		}
		wantJSON(t, "the sign-offs after the proof", json.RawMessage(signOffs()), fmt.Sprintf(`[
			{"key":"0000000001","values":{"state":"proving","subject":%q,"note":"Planned well","at":5000}},
			{"key":"0000000002","values":{"state":"done","subject":%q,"at":5000,"consumedAt":5000}}]`, carol, carol))

		// A withdrawal deletes its row.
		if _, _, err := operation(asCarol, "withdrawSignOff", `{"state":"proving"}`); err != nil {
			t.Fatalf("carol's withdrawal: %v", err)
		}
		wantJSON(t, "the sign-offs after the withdrawal", json.RawMessage(signOffs()), fmt.Sprintf(`[{"key":"0000000002","values":{"state":"done","subject":%q,"at":5000,"consumedAt":5000}}]`, carol))

		// A hard delete deletes the row's sub-table rows, and no queue's
		// messages, since the table has none.
		rec, err = run(func(host rules.Host) error { return rules.RunDelete(ctx, host, "Task", uuidKey, asAlice, nil) })
		if err != nil {
			t.Fatalf("the delete: %v", err)
		}
		if len(rec.applied) != 1 {
			t.Fatalf("the delete applied %d units, want 1", len(rec.applied))
		}
		wantStatements(t, "the delete unit", rec.applied[0][1:], []rules.Statement{
			{Text: fmt.Sprintf(`DELETE FROM "task_approvals_sign_offs" WHERE "task_id" = %s`, db.P(1)), Values: []any{uuidColumn}, Expect: rules.AnyRows},
		})
		if left := len(db.Rows(t, `SELECT 1 FROM "task_approvals_sign_offs"`)); left != 0 {
			t.Errorf("the delete left %d sign-offs, want none", left)
		}
	})
}
