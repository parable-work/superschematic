package rules_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	rules "github.com/parable-work/superschematic/runtime/rules/go"
	"github.com/parable-work/superschematic/runtime/rules/go/internal/rulestest"
)

// A table's audit fields through the adapter (D104): createdBy, which the
// layout lists under audit, read into a row's own fields as an own column
// is, a UUID in its base62 wire form and NULL absent, by every row read
// the core asks for, and never written; and an operation the core answers
// with a result and no row write, as a Workflow's repeat, which writes
// nothing.

// The UUIDs of the question's two subjects: bob, whom it is asked of, and
// alice, who asked it, hyphenated as their columns hold them.
const (
	bobColumn   = "0f8fad5b-d9cb-469f-a165-70867728950e"
	aliceColumn = "7c9e6679-7425-40de-944b-e07fc1f90ae7"
)

// questionTable is Question's layout as the generator writes it for
// fixture-workflow-principals-db: its prompt, the UUID askedOf among its
// own fields, the audit field createdBy, and a Workflow on status.
func questionTable() *rules.Table {
	return &rules.Table{
		Type:    "Question",
		Table:   "question",
		Key:     rules.Column{Field: "id", Column: "id", Type: "Identity.UUID"},
		Version: "_version",
		Own: []rules.Column{
			{Field: "prompt", Column: "prompt", Type: "string"},
			{Field: "askedOf", Column: "asked_of", Type: "Identity.UUID"},
		},
		Audit: []rules.Column{{Field: "createdBy", Column: "created_by", Type: "Identity.UUID"}},
		Uses: []rules.Use{{
			ID:       "status",
			Behavior: "Workflow",
			Member:   "status",
			Anchor:   "status",
			Fields: []rules.OwnedField{
				{Name: "status", Column: rules.Column{Field: "status", Column: "status", Type: "string"}, Value: true},
				{Name: "enteredAt", Column: rules.Column{Field: "statusEnteredAt", Column: "status_entered_at", Type: "Temporal.DateTime", Convert: rules.ConvertEpochMillis}},
			},
			Operations: []rules.Operation{{Name: "transition", Writes: true}},
		}},
		Spec: rules.TypeSpec{
			Name: "Question",
			Uses: []rules.UseSpec{{Behavior: "Workflow", Config: json.RawMessage(workflowConfig), Anchor: rules.Anchor{Field: "status"}}},
			Fields: []rules.FieldSpec{
				{Name: "prompt", Type: "string"},
				{Name: "askedOf", Type: "Identity.UUID"},
				{Name: "createdBy", Type: "Identity.UUID"},
			},
		},
	}
}

func questionLayout() *rules.Layout {
	return &rules.Layout{Types: map[string]*rules.Table{"Question": questionTable()}}
}

// createQuestions writes Question's table as sqlgen writes it, its UUID
// columns uuid on Postgres and TEXT on SQLite, with @optimistic's trigger.
func createQuestions(t *testing.T, db *rulestest.DB) {
	t.Helper()
	types := db.ColumnTypes()
	uuid := "TEXT"
	if db.Dialect == rules.DialectPostgres {
		uuid = "UUID"
	}
	db.Exec(t, fmt.Sprintf(`CREATE TABLE "question" (id %s PRIMARY KEY, prompt %s, asked_of %s, created_by %s, status %s, status_entered_at %s, _version %s)`,
		uuid, types.Text, uuid, uuid, types.Text, types.Timestamp, types.Version))
	for _, ddl := range db.BumpVersion("question") {
		db.Exec(t, ddl)
	}
}

func TestReadRowHoldsTheAuditFields(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createQuestions(t, db)
		db.Exec(t, fmt.Sprintf(`INSERT INTO "question" (id, prompt, asked_of, created_by, status) VALUES (%s, 'Which table?', %s, %s, 'open')`, db.P(1), db.P(2), db.P(3)), uuidColumn, bobColumn, aliceColumn)
		db.Exec(t, fmt.Sprintf(`INSERT INTO "question" (id, prompt, status) VALUES (%s, 'Whose?', 'open')`, db.P(1)), absentColumn)
		layout := questionLayout()
		ctx := context.Background()
		bob, alice := wire(t, bobColumn), wire(t, aliceColumn)

		// A row read selects the audit column after the own columns and
		// before the owned ones, and holds it among the own fields in its
		// wire form; a NULL one is absent.
		var rec *recorded
		err := inTx(t, db, coreHost(layout), func(_ *rulestest.Tx, store rules.Store) rules.Store {
			wrapped, r := record(store)
			rec = r
			return wrapped
		}, func(host rules.Host, _ *rulestest.Tx) error {
			row, err := rules.ReadRow(ctx, host, "Question", uuidKey)
			if err != nil {
				return err
			}
			wantJSON(t, "the row read", row, fmt.Sprintf(`{"own":{"prompt":"Which table?","askedOf":%q,"createdBy":%q},"owned":{"status":{"status":"open"}},"version":1}`, bob, alice))
			row, err = rules.ReadRow(ctx, host, "Question", absentKey)
			if err != nil {
				return err
			}
			wantJSON(t, "a row with no createdBy", row, `{"own":{"prompt":"Whose?"},"owned":{"status":{"status":"open"}},"version":1}`)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		wantStatements(t, "the row read", rec.queries[:1], []rules.Statement{{
			Text:   `SELECT "prompt" AS c0, "asked_of" AS c1, "created_by" AS c2, "status" AS c3, "status_entered_at" AS c4, "_version" AS v FROM "question" WHERE "id" = ` + db.P(1),
			Values: []any{uuidColumn},
		}})

		// The core's row read and its read of the rows that link to a row
		// hold it too.
		linked := string(jsonOf(t, map[string]any{"kind": "linked", "type": "Question", "field": "askedOf", "key": bob, "limit": 10}))
		core := script(t, `{"read":[{"kind":"row","type":"Question","key":"`+uuidKey+`"},`+linked+`]}`, changed(""))
		err = inTx(t, db, coreHost(layout), nil, func(host rules.Host, _ *rulestest.Tx) error {
			host.Core = core
			_, err := rules.Execute(ctx, host, "Question", transition(uuidKey, "paid"), nil)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		row := fmt.Sprintf(`{"own":{"prompt":"Which table?","askedOf":%q,"createdBy":%q},"owned":{"status":{"status":"open"}},"version":1}`, bob, alice)
		wantJSON(t, "the core's reads", core.answers[1], `{"reads":[
			{"request":{"kind":"row","type":"Question","key":"`+uuidKey+`"},"answer":{"row":`+row+`},"held":0},
			{"request":`+linked+`,"answer":{"linked":{"rows":[{"key":"`+uuidKey+`","row":`+row+`}],"more":false}},"held":0}]}`)

		// No write sets it: a change set that names it among a row's own
		// fields is the adapter's own failure, and writes nothing.
		core = script(t, changed(`{"rows":[{"kind":"update","type":"Question","key":"`+uuidKey+`","expectedVersion":1,"own":{"createdBy":"`+bob+`"}}]}`))
		err = inTx(t, db, coreHost(layout), nil, func(host rules.Host, _ *rulestest.Tx) error {
			host.Core = core
			_, err := rules.Execute(ctx, host, "Question", transition(uuidKey, "paid"), nil)
			return err
		})
		wantRefusal(t, err, rules.Refusal{Code: rules.CodeInternal, Status: 500, Class: "Error", Message: "rules: Question has no own field createdBy in the behaviors' layout"})
		if got := db.Rows(t, fmt.Sprintf(`SELECT CAST(created_by AS TEXT), _version FROM "question" WHERE id = %s`, db.P(1)), uuidColumn); len(got) != 1 || fmt.Sprint(got[0][0]) != aliceColumn || fmt.Sprint(got[0][1]) != "1" {
			t.Errorf("the row's created_by and version = %v, want %s at version 1", got, aliceColumn)
		}
	})
}

// An operation the core answers with a result and no row write, as a
// Workflow whose repeat is unchanged answers a move to the state the row is
// in (D104), applies no statement: the row keeps its version and when it
// entered its state, and the stamps the caller encoded for its write
// (updatedAt, updatedBy) bind to none.
func TestAnOperationTheCoreAnswersWithNoWriteWritesNothing(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createOrders(t, db, "order")
		insertOrder(t, db, uuidColumn, "Desk", "paid", 1000)
		layout := orderLayout("Identity.UUID", false)
		core := script(t, `{"read":[{"kind":"row","type":"Order","key":"`+uuidKey+`"}]}`, changed(`{"result":{"from":"paid","to":"paid","unchanged":true}}`))
		var rec *recorded
		var result *rules.Result
		err := inTx(t, db, coreHost(layout), func(_ *rulestest.Tx, store rules.Store) rules.Store {
			wrapped, r := record(store)
			rec = r
			return wrapped
		}, func(host rules.Host, _ *rulestest.Tx) error {
			host.Core = core
			var err error
			result, err = rules.Execute(context.Background(), host, "Order", transition(uuidKey, "paid"), &rules.ExecuteOptions{OwnColumns: map[string]any{"updated_at": at(db, 7)}})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		wantJSON(t, "the result", result.Result, `{"from":"paid","to":"paid","unchanged":true}`)
		for i, unit := range rec.applied {
			if len(unit) > 0 {
				t.Errorf("unit %d applied %d statements, want none: %+v", i, len(unit), unit)
			}
		}
		if got := storedOrder(t, db, uuidColumn); got == nil || *got != (stored{Title: "Desk", Status: "paid", EnteredAt: 1000, Version: 1}) {
			t.Errorf("the row = %+v, want paid since 1000 at version 1", got)
		}
	})
}
