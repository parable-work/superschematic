package rules_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	rules "github.com/parable-work/superschematic/runtime/rules/go"
	"github.com/parable-work/superschematic/runtime/rules/go/internal/rulestest"
)

// The adapter's host loop over a real database of each dialect, SQLite
// through database/sql and Postgres through pgx, each in a transaction a
// test opens, with the core of the archive and with cores a test scripts:
// rules.test.ts's and rules-store.test.ts's cases. A store that records
// what the adapter asks of it holds the statements to TypeScript's, and the
// rows the database holds after each command are read back past the
// adapter. The Postgres cases skip without
// SUPERSCHEMATIC_RULES_TEST_DATABASE_URL.

// transition is rules.test.ts's command: a transition of the row of key to
// a state.
func transition(key, to string) rules.Command {
	return rules.Command{
		Caller:  alice,
		Now:     2000,
		Request: "r2",
		Action:  rules.Action{Kind: rules.ActionOperation, Key: key, UseID: "status", Operation: "transition", Params: json.RawMessage(`{"to":"` + to + `"}`)},
	}
}

// coreHost is a host on the archive's core at the time 5000, its request
// ids "req".
func coreHost(layout *rules.Layout) rules.Host {
	return rules.Host{Layout: layout, Now: func() int64 { return 5000 }, Request: func() string { return "req" }}
}

func TestCreateInsertsTheKeyOwnFieldsAndOwnedColumns(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createOrders(t, db, "order")
		layout := orderLayout("Identity.UUID", false)
		var rec *recorded
		err := inTx(t, db, coreHost(layout), func(_ *rulestest.Tx, store rules.Store) rules.Store {
			wrapped, r := record(store)
			rec = r
			return wrapped
		}, func(host rules.Host, _ *rulestest.Tx) error {
			key, err := rules.RunCreate(context.Background(), host, "Order", map[string]any{"title": "Desk"}, alice, &rules.CreateOptions{ID: uuidKey})
			if err != nil {
				return err
			}
			if key != uuidKey {
				t.Errorf("key = %q, want %q", key, uuidKey)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		wantStatements(t, "queries", rec.queries, []rules.Statement{
			{Text: `SELECT 1 AS one FROM "order" WHERE "id" = ` + db.P(1), Values: []any{uuidColumn}},
		})
		if len(rec.applied) != 1 {
			t.Fatalf("applied %d units, want 1", len(rec.applied))
		}
		p := db.P
		wantStatements(t, "unit", rec.applied[0], []rules.Statement{{
			Text:   fmt.Sprintf(`INSERT INTO "order" ("id", "title", "status", "status_entered_at") VALUES (%s, %s, %s, %s)`, p(1), p(2), p(3), p(4)),
			Values: []any{uuidColumn, "Desk", "draft", at(db, 5000)},
			Expect: 1,
		}})
		if got := storedOrder(t, db, uuidColumn); got == nil || *got != (stored{Title: "Desk", Status: "draft", EnteredAt: 5000, Version: 1}) {
			t.Errorf("the row = %+v, want Desk, draft since 5000, at version 1", got)
		}
		err = inTx(t, db, coreHost(layout), nil, func(host rules.Host, _ *rulestest.Tx) error {
			row, err := rules.ReadRow(context.Background(), host, "Order", uuidKey)
			if err != nil {
				return err
			}
			wantJSON(t, "the row read", row, `{"own":{"title":"Desk"},"owned":{"status":{"status":"draft","enteredAt":5000}},"version":1}`)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}

func TestCreateWithNoIDGetsAUUIDKeyInItsWireForm(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createOrders(t, db, "order")
		layout := orderLayout("Identity.UUID", false)
		var rec *recorded
		var key string
		err := inTx(t, db, coreHost(layout), func(_ *rulestest.Tx, store rules.Store) rules.Store {
			wrapped, r := record(store)
			rec = r
			return wrapped
		}, func(host rules.Host, _ *rulestest.Tx) error {
			var err error
			key, err = rules.RunCreate(context.Background(), host, "Order", map[string]any{"title": "Desk"}, alice, &rules.CreateOptions{
				ExecuteOptions: rules.ExecuteOptions{OwnColumns: map[string]any{"title": "Desk", "created_by": "u1"}},
			})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if !regexp.MustCompile(`^[0-9A-Za-z]{1,22}$`).MatchString(key) {
			t.Fatalf("key = %q, want a UUID's base62 text", key)
		}
		insert := rec.applied[0][0]
		p := db.P
		if want := fmt.Sprintf(`INSERT INTO "order" ("id", "created_by", "title", "status", "status_entered_at") VALUES (%s, %s, %s, %s, %s)`, p(1), p(2), p(3), p(4), p(5)); insert.Text != want {
			t.Errorf("insert = %s\nwant     %s", insert.Text, want)
		}
		wantStatements(t, "values", []rules.Statement{{Values: insert.Values[1:]}}, []rules.Statement{{Values: []any{"u1", "Desk", "draft", at(db, 5000)}}})
		rows := db.Rows(t, `SELECT id, created_by FROM "order"`)
		if len(rows) != 1 || fmt.Sprint(rows[0][1]) != "u1" {
			t.Fatalf("rows = %v, want one created by u1", rows)
		}
		if column := fmt.Sprint(rows[0][0]); len(column) != 36 || !strings.Contains(column, "-") {
			t.Errorf("the key's column = %q, want a hyphenated UUID", column)
		}

		// A string key gets a UUID's hyphenated text, and a key of another
		// type none.
		stringKeys := &rules.Layout{Types: map[string]*rules.Table{"Order": orderTable("order", "string", false, workflowConfig)}}
		err = inTx(t, db, coreHost(stringKeys), nil, func(host rules.Host, _ *rulestest.Tx) error {
			key, err := rules.RunCreate(context.Background(), host, "Order", map[string]any{"title": "Lamp"}, alice, nil)
			if err != nil {
				return err
			}
			if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(key) {
				t.Errorf("a string key = %q, want a version 4 UUID's text", key)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		numbered := &rules.Layout{Types: map[string]*rules.Table{"Order": orderTable("order", "number", false, workflowConfig)}}
		err = inTx(t, db, coreHost(numbered), nil, func(host rules.Host, _ *rulestest.Tx) error {
			_, err := rules.RunCreate(context.Background(), host, "Order", map[string]any{"title": "Lamp"}, alice, nil)
			return err
		})
		wantRefusal(t, err, rules.Refusal{Code: rules.CodeInvalidArgument, Status: 400, Class: "EngineError", Message: "a create of Order names no key, and the adapter makes none for a key of type number: pass the id"})
	})
}

func TestOperationReadsItsRowAndWritesItHeldToItsVersion(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createOrders(t, db, "order")
		insertOrder(t, db, uuidColumn, "Desk", "draft", 1000)
		layout := orderLayout("Identity.UUID", false)
		var rec *recorded
		err := inTx(t, db, coreHost(layout), func(_ *rulestest.Tx, store rules.Store) rules.Store {
			wrapped, r := record(store)
			rec = r
			return wrapped
		}, func(host rules.Host, _ *rulestest.Tx) error {
			ctx := context.Background()
			row, err := rules.ReadRow(ctx, host, "Order", uuidKey)
			if err != nil {
				return err
			}
			wantJSON(t, "the row read", row, `{"own":{"title":"Desk"},"owned":{"status":{"status":"draft","enteredAt":1000}},"version":1}`)
			rec.queries = nil
			result, err := rules.Execute(ctx, host, "Order", transition(uuidKey, "paid"), &rules.ExecuteOptions{OwnColumns: map[string]any{"updated_at": at(db, 7)}})
			if err != nil {
				return err
			}
			wantJSON(t, "the result", map[string]any{"changes": result.Changes, "result": result.Result, "key": result.Key, "attempts": result.Attempts}, `{
				"changes": {
					"rows": [{"kind": "update", "type": "Order", "key": "`+uuidKey+`", "expectedVersion": 1, "owned": {"status": {"status": "paid", "enteredAt": 2000}}}],
					"subRows": [], "events": [], "deadlines": [],
					"result": {"from": "draft", "to": "paid"}
				},
				"result": {"from": "draft", "to": "paid"},
				"key": "`+uuidKey+`",
				"attempts": 1
			}`)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		p := db.P
		wantStatements(t, "queries", rec.queries, []rules.Statement{
			{Text: `SELECT "title" AS c0, "status" AS c1, "status_entered_at" AS c2, "_version" AS v FROM "order" WHERE "id" = ` + p(1), Values: []any{uuidColumn}},
		})
		wantStatements(t, "unit", rec.applied[0], []rules.Statement{{
			Text:   fmt.Sprintf(`UPDATE "order" SET "updated_at" = %s, "status" = %s, "status_entered_at" = %s, "_version" = "_version" + 1 WHERE "id" = %s AND "_version" = %s`, p(1), p(2), p(3), p(4), p(5)),
			Values: []any{at(db, 7), "paid", at(db, 2000), uuidColumn, int64(1)},
			Expect: 1,
		}})
		if got := storedOrder(t, db, uuidColumn); got == nil || *got != (stored{Title: "Desk", Status: "paid", EnteredAt: 2000, Version: 2}) {
			t.Errorf("the row = %+v, want paid since 2000 at version 2", got)
		}
	})
}

func TestAMovedRowRunsAgainAndOneThatKeepsMovingIsBusy(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createOrders(t, db, "order")
		layout := orderLayout("string", false)
		insertOrder(t, db, "o2", "Chair", "draft", 1000)
		var result *rules.Result
		err := inTx(t, db, coreHost(layout), moving(db, "o2", 1), func(host rules.Host, _ *rulestest.Tx) error {
			var err error
			result, err = rules.Execute(context.Background(), host, "Order", transition("o2", "paid"), nil)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.Attempts != 2 {
			t.Errorf("attempts = %d, want 2", result.Attempts)
		}
		wantJSON(t, "the result", result.Result, `{"from":"draft","to":"paid"}`)
		// The other write and the command's: both kept.
		if got := storedOrder(t, db, "o2"); got == nil || *got != (stored{Title: "Chair!", Status: "paid", EnteredAt: 2000, Version: 3}) {
			t.Errorf("the row = %+v, want Chair!, paid at version 3", got)
		}

		insertOrder(t, db, "o3", "Lamp", "draft", 1000)
		var applied int
		err = inTx(t, db, coreHost(layout), func(tx *rulestest.Tx, store rules.Store) rules.Store {
			wrapped := moving(db, "o3", rules.MaxAttempts)(tx, store)
			return &countingApplies{Store: wrapped, n: &applied}
		}, func(host rules.Host, tx *rulestest.Tx) error {
			_, err := rules.RunOperation(context.Background(), host, "Order", "o3", "status", "transition", map[string]string{"to": "paid"}, alice, nil)
			wantRefusal(t, err, rules.Refusal{
				Code: rules.CodeBusy, Status: 503, Class: "EngineError",
				Message: "transition of Order o3: the row kept moving while the command ran; it ran 8 times and nothing was written, so it may be tried again",
			})
			if busy := rules.BusyRefusal("Order", transition("o3", "paid"), rules.MaxAttempts); !reflect.DeepEqual(refusalOf(t, err), busy) {
				t.Errorf("the refusal = %+v, want BusyRefusal's %+v", refusalOf(t, err), busy)
			}
			// Nothing of the command was written; each move was.
			rows, qerr := host.Store.Query(context.Background(), `SELECT title, status, status_entered_at, _version, deleted_at FROM "order" WHERE id = `+db.P(1), "o3")
			if qerr != nil {
				return qerr
			}
			if got := storedOf(t, rows[0]); *got != (stored{Title: "Lamp" + strings.Repeat("!", rules.MaxAttempts), Status: "draft", EnteredAt: 1000, Version: 1 + rules.MaxAttempts}) {
				t.Errorf("the row = %+v, want draft, at version 9", got)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if applied != rules.MaxAttempts {
			t.Errorf("applied %d units, want %d", applied, rules.MaxAttempts)
		}
	})
}

// countingApplies counts a store's applies.
type countingApplies struct {
	rules.Store
	n *int
}

func (c *countingApplies) Apply(ctx context.Context, statements []rules.Statement) (bool, error) {
	*c.n++
	return c.Store.Apply(ctx, statements)
}

func TestARefusalIsTheCoresWholeAndWritesNothing(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createOrders(t, db, "order")
		insertOrder(t, db, uuidColumn, "Desk", "draft", 1000)
		var rec *recorded
		err := inTx(t, db, coreHost(orderLayout("Identity.UUID", false)), func(_ *rulestest.Tx, store rules.Store) rules.Store {
			wrapped, r := record(store)
			rec = r
			return wrapped
		}, func(host rules.Host, _ *rulestest.Tx) error {
			_, err := rules.RunOperation(context.Background(), host, "Order", uuidKey, "status", "transition", map[string]any{"to": "shipped"}, alice, nil)
			return err
		})
		wantRefusal(t, err, rules.Refusal{
			Code: rules.CodeVetoed, Status: 409, Class: "BehaviorVetoError",
			Message: "behavior Workflow vetoes transition of Order " + uuidKey + ": no transition leads from draft to shipped; from draft it can move to paid",
		})
		var details struct {
			VetoCode string `json:"vetoCode"`
		}
		if err := json.Unmarshal(refusalOf(t, err).Details, &details); err != nil || details.VetoCode != "transition_not_allowed" {
			t.Errorf("details = %s, want vetoCode transition_not_allowed", refusalOf(t, err).Details)
		}
		var refusal *rules.Refusal
		if wrapped := fmt.Errorf("orm: %w", err); !errors.As(wrapped, &refusal) || refusal.Code != rules.CodeVetoed || rules.IsNotFound(wrapped) {
			t.Errorf("a wrapped refusal reads as %v", refusal)
		}
		if err.Error() != "vetoed: "+refusal.Message {
			t.Errorf("the refusal's text = %q", err.Error())
		}
		if len(rec.applied) != 0 {
			t.Errorf("applied %v, want nothing", rec.applied)
		}
		if got := storedOrder(t, db, uuidColumn); got.Status != "draft" || got.Version != 1 {
			t.Errorf("the row = %+v, want it as it was", got)
		}
	})
}

func TestAnUpdateNamingAnOwnedFieldIsRefused(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createOrders(t, db, "order")
		insertOrder(t, db, uuidColumn, "Desk", "draft", 1000)
		layout := orderLayout("Identity.UUID", false)
		update := func(patch map[string]any) error {
			return inTx(t, db, coreHost(layout), nil, func(host rules.Host, _ *rulestest.Tx) error {
				return rules.RunUpdate(context.Background(), host, "Order", uuidKey, patch, alice, nil)
			})
		}
		// The anchor in the core's words, another owned field in the same
		// shape.
		wantRefusal(t, update(map[string]any{"status": "paid"}), rules.Refusal{
			Code: rules.CodeInvalidInstance, Status: 422, Class: "InstanceValidationError",
			Message: "Order: status: status is behavior Workflow's field: only its operations change it",
			Details: json.RawMessage(`{"issues":[{"path":"status","rule":"owned","message":"status is behavior Workflow's field: only its operations change it"}]}`),
		})
		wantRefusal(t, update(map[string]any{"statusEnteredAt": nil}), rules.Refusal{
			Code: rules.CodeInvalidInstance, Status: 422, Class: "InstanceValidationError",
			Message: "Order: statusEnteredAt: statusEnteredAt is behavior Workflow's field: only its operations change it",
			Details: json.RawMessage(`{"issues":[{"path":"statusEnteredAt","rule":"owned","message":"statusEnteredAt is behavior Workflow's field: only its operations change it"}]}`),
		})
		// An update of own fields writes them and keeps the state.
		if err := update(map[string]any{"title": "Chair"}); err != nil {
			t.Fatal(err)
		}
		if got := storedOrder(t, db, uuidColumn); *got != (stored{Title: "Chair", Status: "draft", EnteredAt: 1000, Version: 2}) {
			t.Errorf("the row = %+v, want Chair, draft, at version 2", got)
		}
	})
}

func TestACreateOfAHeldKeyIsAConflict(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createOrders(t, db, "order")
		insertOrder(t, db, uuidColumn, "Desk", "draft", 1000)
		db.Exec(t, `UPDATE "order" SET deleted_at = `+db.P(1)+` WHERE id = `+db.P(2), db.Timestamp(time.UnixMilli(9)), uuidColumn)
		// The key is held by a row the repository holds deleted: a create
		// conflicts with any row, live or not.
		for _, live := range []bool{false, true} {
			var rec *recorded
			err := inTx(t, db, coreHost(orderLayout("Identity.UUID", live)), func(_ *rulestest.Tx, store rules.Store) rules.Store {
				wrapped, r := record(store)
				rec = r
				return wrapped
			}, func(host rules.Host, _ *rulestest.Tx) error {
				_, err := rules.RunCreate(context.Background(), host, "Order", map[string]any{"title": "Desk"}, alice, &rules.CreateOptions{ID: uuidKey})
				return err
			})
			wantRefusal(t, err, rules.Refusal{Code: rules.CodeConflict, Status: 409, Class: "EngineError", Message: "Order " + uuidKey + " already exists"})
			if len(rec.applied) != 0 {
				t.Errorf("live %v: applied %v, want nothing", live, rec.applied)
			}
		}
	})
}

func TestATypeTheLayoutLacksIsInternal(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		run := func(layout *rules.Layout) error {
			return inTx(t, db, coreHost(layout), nil, func(host rules.Host, _ *rulestest.Tx) error {
				_, err := rules.Execute(context.Background(), host, "Invoice", transition(uuidKey, "paid"), nil)
				return err
			})
		}
		wantRefusal(t, run(orderLayout("Identity.UUID", false)), rules.Refusal{Code: rules.CodeInternal, Status: 500, Class: "Error", Message: "rules: the behaviors' layout has no type Invoice; it holds Order"})
		wantRefusal(t, run(&rules.Layout{Types: map[string]*rules.Table{}}), rules.Refusal{Code: rules.CodeInternal, Status: 500, Class: "Error", Message: "rules: the behaviors' layout has no type Invoice; it holds none"})
	})
}

func TestASQLiteRowReadsISOTextAsEpochMillis(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createOrders(t, db, "order")
		layout := orderLayout("string", false)
		db.Exec(t, `INSERT INTO "order" (id, title, status, _version) VALUES ('o1', 'Desk', 'paid', 3)`)
		db.Exec(t, `INSERT INTO "order" (id, status_entered_at) VALUES ('o2', `+db.P(1)+`)`, db.Timestamp(time.UnixMilli(1234)))
		reads := map[string]string{
			// A NULL column is absent, and the version a number.
			"o1": `{"own":{"title":"Desk"},"owned":{"status":{"status":"paid"}},"version":3}`,
			"o2": `{"owned":{"status":{"enteredAt":1234}},"version":1}`,
		}
		if db.Dialect == rules.DialectSQLite {
			// SQLite's own text, a space and no zone, is UTC.
			db.Exec(t, `INSERT INTO "order" (id, status_entered_at) VALUES ('o3', '1970-01-01 00:00:01.5')`)
			reads["o3"] = `{"owned":{"status":{"enteredAt":1500}},"version":1}`
		}
		err := inTx(t, db, coreHost(layout), nil, func(host rules.Host, _ *rulestest.Tx) error {
			for key, want := range reads {
				row, err := rules.ReadRow(context.Background(), host, "Order", key)
				if err != nil {
					return err
				}
				wantJSON(t, key, row, want)
			}
			row, err := rules.ReadRow(context.Background(), host, "Order", "o9")
			if err != nil || row != nil {
				t.Errorf("a key no row holds reads %+v, %v", row, err)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}

// A spec the core refuses refuses every command on its type, checked once
// per core and layout, as rules.ts keeps the check: another core over the
// same layout makes its own, and the first's stays its own.
func TestALayoutSpecTheCoreRefusesRefusesEveryCommandCheckedOncePerCore(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createOrders(t, db, "order")
		insertOrder(t, db, uuidColumn, "Desk", "draft", 1000)
		broken := &rules.Layout{Types: map[string]*rules.Table{"Order": orderTable("order", "Identity.UUID", false, `{"states":[]}`)}}
		core := &counting{Core: rules.Native()}
		execute := func(core rules.Core) error {
			return inTx(t, db, rules.Host{Core: core, Layout: broken}, nil, func(host rules.Host, _ *rulestest.Tx) error {
				_, err := rules.Execute(context.Background(), host, "Order", transition(uuidKey, "paid"), nil)
				return err
			})
		}
		refused := func(err error) {
			t.Helper()
			refusal := refusalOf(t, err)
			if refusal.Code != rules.CodeInvalidSchema || refusal.Status != 422 {
				t.Errorf("refusal = %+v, want invalid_schema, 422", refusal)
			}
		}
		refused(execute(core))
		// A core that takes every spec runs over the layout the first
		// refused.
		taking := script(t, changed(""))
		for range 2 {
			if err := execute(taking); err != nil {
				t.Errorf("a core that takes the spec: %v", err)
			}
		}
		refused(execute(core))
		if core.checks != 1 || taking.checks != 1 {
			t.Errorf("checks = %d and %d, want 1 each", core.checks, taking.checks)
		}
	})
}

func TestALockIsTakenOnceAndEachReadRecordsTheLocksHeld(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createOrders(t, db, "order")
		insertOrder(t, db, uuidColumn, "Desk", "draft", 1000)
		lock := "Stock/" + db.Name + db.Schema
		core := script(t,
			`{"read":[{"kind":"row","type":"Order","key":"`+uuidKey+`"}]}`,
			`{"lock":["`+lock+`"]}`,
			`{"read":[{"kind":"row","type":"Order","key":"`+uuidKey+`"}]}`,
			`{"lock":["`+lock+`"]}`,
			changed(`{"result":"ok"}`),
		)
		var rec *recorded
		var result *rules.Result
		err := inTx(t, db, rules.Host{Core: core, Layout: orderLayout("Identity.UUID", false)}, func(_ *rulestest.Tx, store rules.Store) rules.Store {
			wrapped, r := record(store)
			rec = r
			return wrapped
		}, func(host rules.Host, _ *rulestest.Tx) error {
			var err error
			result, err = rules.Execute(context.Background(), host, "Order", transition(uuidKey, "paid"), nil)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		wantJSON(t, "the result", result.Result, `"ok"`)
		if len(rec.locks) != 1 || rec.locks[0] != lock {
			t.Errorf("locks taken = %v, want %s once", rec.locks, lock)
		}
		type seen struct {
			Held  []int
			Locks []string
		}
		var got []seen
		for _, answers := range core.answers {
			s := seen{Held: []int{}, Locks: append([]string{}, answers.Locks...)}
			for _, read := range answers.Reads {
				s.Held = append(s.Held, read.Held)
			}
			got = append(got, s)
		}
		wantJSON(t, "the answers", got, `[
			{"Held": [], "Locks": []},
			{"Held": [0], "Locks": []},
			{"Held": [0], "Locks": ["`+lock+`"]},
			{"Held": [1], "Locks": ["`+lock+`"]},
			{"Held": [1], "Locks": ["`+lock+`"]}
		]`)
		// The read asked again replaced the one made before the lock.
		if row := core.answers[3].Reads[0].Answer.Row; row == nil || row.Version != 1 {
			t.Errorf("the read under the lock = %+v", core.answers[3].Reads[0])
		}
	})
}

func TestAFencedStoreChecksEachReadMadeUnderALock(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createOrders(t, db, "order")
		insertOrder(t, db, uuidColumn, "Desk", "draft", 1000)
		lock := "Stock/" + db.Name + db.Schema
		steps := func() *scripted {
			return script(t,
				`{"read":[{"kind":"spec","type":"Order"}]}`,
				`{"lock":["`+lock+`"]}`,
				`{"read":[{"kind":"row","type":"Order","key":"`+uuidKey+`"},{"kind":"row","type":"Order","key":"`+absentKey+`"},{"kind":"hasRows","type":"Order"},{"kind":"spec","type":"Order"}]}`,
				changed(`{"rows":[{"kind":"update","type":"Order","key":"`+uuidKey+`","expectedVersion":1,"own":{"title":"Chair"},"owned":{}}]}`),
			)
		}
		live := orderLayout("Identity.UUID", true)
		var rec *recorded
		err := inTx(t, db, rules.Host{Core: steps(), Layout: live}, func(_ *rulestest.Tx, store rules.Store) rules.Store {
			wrapped, r := record(store)
			rec = r
			return wrapped
		}, func(host rules.Host, _ *rulestest.Tx) error {
			_, err := rules.Execute(context.Background(), host, "Order", transition(uuidKey, "paid"), nil)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(rec.locks) != 1 {
			t.Errorf("locks = %v", rec.locks)
		}
		unit := rec.applied[0]
		update := regexp.MustCompile(`^UPDATE "order" SET "title" = .*, "_version" = "_version" \+ 1 WHERE "id" = [$?]\d+ AND "_version" = [$?]\d+ AND "deleted_at" IS NULL$`)
		if db.Dialect == rules.DialectSQLite {
			if len(unit) != 4 {
				t.Fatalf("unit = %v, want three checks and the update", unit)
			}
			wantStatements(t, "fences", unit[:3], []rules.Statement{
				{Text: `EXISTS (SELECT 1 FROM "order" WHERE "id" = ?1 AND "_version" = ?2 AND "deleted_at" IS NULL)`, Values: []any{uuidColumn, int64(1)}, Check: true},
				{Text: `NOT EXISTS (SELECT 1 FROM "order" WHERE "id" = ?1 AND "deleted_at" IS NULL)`, Values: []any{absentColumn}, Check: true},
				{Text: `EXISTS (SELECT 1 FROM "order" WHERE "deleted_at" IS NULL)`, Values: []any{}, Check: true},
			})
			if !update.MatchString(unit[3].Text) || unit[3].Expect != 1 {
				t.Errorf("update = %s", statementText(unit[3]))
			}
		} else {
			// A store that holds its locks gets no check.
			if len(unit) != 1 || unit[0].Check || !update.MatchString(unit[0].Text) {
				t.Errorf("unit = %v, want the update alone", unit)
			}
		}
		if got := storedOrder(t, db, uuidColumn); got.Title != "Chair" || got.Version != 2 {
			t.Errorf("the row = %+v, want Chair at version 2", got)
		}

		// A read made under the lock that another write changed before the
		// unit moves the command, which runs again over the write.
		bookings := &rules.Layout{Types: map[string]*rules.Table{"Booking": {
			Type: "Booking", Table: "booking", Key: rules.Column{Field: "id", Column: "id", Type: "string"}, Version: "_version",
			Own: []rules.Column{}, Uses: []rules.Use{}, Spec: rules.TypeSpec{Name: "Booking"},
		}}}
		types := db.ColumnTypes()
		db.Exec(t, fmt.Sprintf(`CREATE TABLE booking (id %s, _version %s)`, types.Key, types.Version))
		var result *rules.Result
		err = inTx(t, db, rules.Host{Core: oneBooking{room: lock}, Layout: bookings}, func(tx *rulestest.Tx, store rules.Store) rules.Store {
			wrapped, _ := record(store)
			wrapped.before = func(ctx context.Context, n int) error {
				if n > 0 {
					return nil
				}
				_, err := tx.Exec(ctx, `INSERT INTO booking (id) VALUES ('b0')`)
				return err
			}
			return wrapped
		}, func(host rules.Host, _ *rulestest.Tx) error {
			var err error
			result, err = rules.Execute(context.Background(), host, "Booking", bookingCreate("b1"), nil)
			return err
		})
		if db.Dialect == rules.DialectSQLite {
			// The unit found what it read under the lock changed, so the
			// command ran again and read the other's row.
			wantRefusal(t, err, rules.Refusal{Code: rules.CodeConflict, Status: 409, Class: "EngineError", Message: "room r1 is booked"})
		} else {
			// Postgres holds the lock instead, which a writer of its own
			// would have waited on (TestTwoCommandsThatTakeOneLockWriteOneRow).
			if err != nil || result.Attempts != 1 {
				t.Errorf("a held store = %+v, %v; want applied after 1", result, err)
			}
		}
	})
}

// oneBooking is rules-store.test.ts's core: a create of a Booking is a rule
// across rows, as two bookings of one room are. It takes the lock room,
// reads under it whether Booking has rows, and creates its row only when
// there is none, refusing conflict otherwise. Only the row it creates is
// written, so only the lock holds two such commands apart.
type oneBooking struct{ room string }

func (b oneBooking) Run(_ rules.TypeSpec, command rules.Command, answers rules.Answers) (rules.Step, error) {
	if len(answers.Locks) == 0 {
		return rules.Step{Lock: []string{b.room}}, nil
	}
	for _, read := range answers.Reads {
		if read.Request.Kind != rules.ReadKindHasRows || read.Held == 0 {
			continue
		}
		if read.Answer.HasRows {
			return rules.Step{Done: &rules.Outcome{Refused: &rules.Refusal{Code: rules.CodeConflict, Status: 409, Class: "EngineError", Message: "room r1 is booked"}}}, nil
		}
		key := command.Action.ID
		return rules.Step{Done: &rules.Outcome{Changed: &rules.ChangeSet{Rows: []rules.RowWrite{{Kind: rules.RowCreate, Type: "Booking", Key: &key}}, Result: json.RawMessage(`null`)}}}, nil
	}
	return rules.Step{Read: []rules.ReadRequest{{Kind: rules.ReadKindHasRows, Type: "Booking"}}}, nil
}

func (oneBooking) Declarations() ([]rules.Declaration, error) { return nil, nil }

func (oneBooking) Check(specs []rules.TypeSpec) ([]*rules.Refusal, error) {
	return make([]*rules.Refusal, len(specs)), nil
}

func bookingCreate(id string) rules.Command {
	return rules.Command{Caller: alice, Now: 1000, Request: id, Action: rules.Action{Kind: rules.ActionCreate, ID: id}}
}

// bookingsOn makes the table Booking on db and returns its layout.
func bookingsOn(t *testing.T, db *rulestest.DB) *rules.Layout {
	t.Helper()
	types := db.ColumnTypes()
	db.Exec(t, fmt.Sprintf(`CREATE TABLE booking (id %s, _version %s)`, types.Key, types.Version))
	return &rules.Layout{Types: map[string]*rules.Table{"Booking": {
		Type: "Booking", Table: "booking", Key: rules.Column{Field: "id", Column: "id", Type: "string"}, Version: "_version",
		Own: []rules.Column{}, Uses: []rules.Use{}, Spec: rules.TypeSpec{Name: "Booking"},
	}}}
}

// Of two commands in transactions of their own that take one lock, the
// second waits for the first and reads its row. On Postgres it waits on
// the advisory lock; on SQLite, whose transactions the store asks to begin
// IMMEDIATE, at its BEGIN, for the one writer the first holds.
func TestTwoCommandsThatTakeOneLockWriteOneRow(t *testing.T) {
	rulestest.Each(t, testTwoCommandsThatTakeOneLockWriteOneRow)
}

func testTwoCommandsThatTakeOneLockWriteOneRow(t *testing.T, db *rulestest.DB) {
	bookings := bookingsOn(t, db)
	core := oneBooking{room: "Room/" + db.Schema}
	// The first command pauses after its read under the lock, until the
	// second has run as far as it can.
	read := make(chan struct{})
	release := make(chan struct{})
	outcome := func(result *rules.Result, err error) string {
		if err != nil {
			if refusal, ok := rules.AsRefusal(err); ok {
				return string(refusal.Code)
			}
			return err.Error()
		}
		return fmt.Sprintf("applied after %d", result.Attempts)
	}
	first := make(chan string, 1)
	go func() {
		var result *rules.Result
		err := inTx(t, db, rules.Host{Core: core, Layout: bookings}, func(_ *rulestest.Tx, store rules.Store) rules.Store {
			return &pausing{Store: store, read: read, release: release}
		}, func(host rules.Host, _ *rulestest.Tx) error {
			var err error
			result, err = rules.Execute(context.Background(), host, "Booking", bookingCreate("b1"), nil)
			return err
		})
		first <- outcome(result, err)
	}()
	<-read
	second := make(chan string, 1)
	go func() {
		var result *rules.Result
		err := inTx(t, db, rules.Host{Core: core, Layout: bookings}, nil, func(host rules.Host, _ *rulestest.Tx) error {
			var err error
			result, err = rules.Execute(context.Background(), host, "Booking", bookingCreate("b2"), nil)
			return err
		})
		second <- outcome(result, err)
	}()
	select {
	case got := <-second:
		t.Fatalf("the second command ran past the lock the first holds: %s", got)
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	if got := <-first; got != "applied after 1" {
		t.Errorf("the first = %s, want applied after 1", got)
	}
	if got := <-second; got != "conflict" {
		t.Errorf("the second = %s, want conflict", got)
	}
	rows := db.Rows(t, `SELECT id FROM booking ORDER BY id`)
	if len(rows) != 1 || rows[0][0] != "b1" {
		t.Errorf("rows = %v, want b1 alone", rows)
	}
}

// A SQLite transaction begun DEFERRED reads a snapshot another connection
// can commit past before its unit: the unit's checks pass on that
// snapshot, and its write is refused with SQLITE_BUSY (in WAL mode,
// SQLITE_BUSY_SNAPSHOT). The command is refused as busy, nothing of it
// written, and not with the driver's error.
func TestACommandInADeferredSQLiteTransactionAnotherWrotePastIsBusy(t *testing.T) {
	db := rulestest.SQLiteDeferred(t)
	bookings := bookingsOn(t, db)
	host := rules.Host{Core: oneBooking{room: "Room/r1"}, Layout: bookings}
	var first *rules.Result
	err := inTx(t, db, host, func(_ *rulestest.Tx, store rules.Store) rules.Store {
		wrapped, _ := record(store)
		wrapped.before = func(ctx context.Context, n int) error {
			if n > 0 {
				return nil
			}
			// After the second's reads and before its unit, the first runs
			// whole on another connection and commits.
			return inTx(t, db, host, nil, func(host rules.Host, _ *rulestest.Tx) error {
				var err error
				first, err = rules.Execute(ctx, host, "Booking", bookingCreate("b1"), nil)
				return err
			})
		}
		return wrapped
	}, func(host rules.Host, _ *rulestest.Tx) error {
		_, err := rules.Execute(context.Background(), host, "Booking", bookingCreate("b2"), nil)
		return err
	})
	if first == nil || first.Attempts != 1 {
		t.Fatalf("the first = %+v, want applied after 1 (err %v)", first, err)
	}
	wantRefusal(t, err, rules.Refusal{Code: rules.CodeBusy, Status: 503, Class: "EngineError", Message: "create of Booking b2: the row kept moving while the command ran; it ran 1 times and nothing was written, so it may be tried again"})
	rows := db.Rows(t, `SELECT id FROM booking ORDER BY id`)
	if len(rows) != 1 || rows[0][0] != "b1" {
		t.Errorf("rows = %v, want b1 alone", rows)
	}
}

// pausing is a store that pauses after its first read of whether a table
// has rows, saying so on read, until release closes.
type pausing struct {
	rules.Store
	read, release chan struct{}
	paused        bool
}

func (p *pausing) Query(ctx context.Context, text string, values ...any) ([][]any, error) {
	rows, err := p.Store.Query(ctx, text, values...)
	if !p.paused && strings.Contains(text, "LIMIT 1") {
		p.paused = true
		close(p.read)
		<-p.release
	}
	return rows, err
}

func TestHasRowsAndSpecAnswerFromTheStoreAndTheLayout(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createOrders(t, db, "order")
		layout := orderLayout("Identity.UUID", true)
		run := func() *scripted {
			core := script(t,
				`{"read":[{"kind":"hasRows","type":"Order"},{"kind":"spec","type":"Order"},{"kind":"spec","type":"Ghost"},{"kind":"hasRows","type":"Ghost"},{"kind":"row","type":"Ghost","key":"g1"}]}`,
				changed(""),
			)
			err := inTx(t, db, rules.Host{Core: core, Layout: layout}, nil, func(host rules.Host, _ *rulestest.Tx) error {
				_, err := rules.Execute(context.Background(), host, "Order", transition(uuidKey, "paid"), nil)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			return core
		}
		answers := func(core *scripted) []rules.Answer {
			var out []rules.Answer
			for _, read := range core.answers[1].Reads {
				out = append(out, read.Answer)
			}
			return out
		}
		spec, _ := json.Marshal(layout.Types["Order"].Spec)
		// No row, then one the repository holds deleted: a table of no live
		// rows.
		wantJSON(t, "no rows", answers(run()), `[{"hasRows":false},{"spec":`+string(spec)+`},{"spec":null},{"hasRows":false},{"row":null}]`)
		insertOrder(t, db, uuidColumn, "Desk", "draft", 1000)
		db.Exec(t, `UPDATE "order" SET deleted_at = `+db.P(1), db.Timestamp(time.UnixMilli(9)))
		wantJSON(t, "a deleted row", answers(run()), `[{"hasRows":false},{"spec":`+string(spec)+`},{"spec":null},{"hasRows":false},{"row":null}]`)
		insertOrder(t, db, absentColumn, "Lamp", "draft", 1000)
		wantJSON(t, "a live row", answers(run()), `[{"hasRows":true},{"spec":`+string(spec)+`},{"spec":null},{"hasRows":false},{"row":null}]`)
	})
}

func TestAKindTheAdapterDoesNotTakeIsInternal(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createOrders(t, db, "order")
		insertOrder(t, db, uuidColumn, "Desk", "draft", 1000)
		layout := orderLayout("Identity.UUID", false)
		refusal := func(steps ...string) *rules.Refusal {
			t.Helper()
			err := inTx(t, db, rules.Host{Core: script(t, steps...), Layout: layout}, nil, func(host rules.Host, _ *rulestest.Tx) error {
				_, err := rules.Execute(context.Background(), host, "Order", transition(uuidKey, "paid"), nil)
				return err
			})
			return refusalOf(t, err)
		}
		internal := func(message string) rules.Refusal {
			return rules.Refusal{Code: rules.CodeInternal, Status: 500, Class: "Error", Message: message}
		}
		cases := []struct {
			steps []string
			want  string
		}{
			{[]string{`{"read":[{"kind":"count","type":"Order"}]}`}, `rules: the adapter answers no read of this kind: {"kind":"count","type":"Order"}`},
			{[]string{`{"wait":1}`}, `rules: the core's step is of a kind the adapter does not take: {"wait":1}`},
			{[]string{`{"done":{"postponed":true}}`}, `rules: the core's outcome is of a kind the adapter does not take: {"postponed":true}`},
			{[]string{changed(`{"rows":[{"kind":"touch","type":"Order","key":"` + uuidKey + `"}]}`)}, `rules: the adapter takes no row write of this kind: {"kind":"touch","type":"Order","key":"` + uuidKey + `"}`},
			{[]string{changed(`{"events":[{"row":{"type":"Order","key":"` + uuidKey + `"},"kind":"paid","useId":"status","payload":{}}]}`)}, "rules: transition of Order writes outbox events, which the adapter takes once each has a table of its own (D103's order of the work, step 3)"},
			{[]string{changed(`{"subRows":[{}]}`)}, "rules: transition of Order writes sub-table rows, which the adapter takes once each has a table of its own (D103's order of the work, step 3)"},
			{[]string{changed(`{"deadlines":[{}]}`)}, "rules: transition of Order writes deadlines, which the adapter takes once each has a table of its own (D103's order of the work, step 3)"},
			{[]string{`{"read":[{"kind":"hasRows","type":"Order"}]}`}, "rules: transition of Order: the run still asked for reads or locks after 256 rounds"},
			{[]string{changed(`{"rows":[{"kind":"update","type":"Order","key":"` + uuidKey + `","expectedVersion":1,"own":{"colour":"red"},"owned":{}}]}`)}, "rules: Order has no own field colour in the behaviors' layout"},
			{[]string{changed(`{"rows":[{"kind":"update","type":"Order","key":"` + uuidKey + `","expectedVersion":1,"own":{},"owned":{"fulfilment":{"status":"x"}}}]}`)}, "rules: Order has no use fulfilment in the behaviors' layout"},
			{[]string{changed(`{"rows":[{"kind":"update","type":"Order","key":"` + uuidKey + `","expectedVersion":1,"own":{},"owned":{"status":{"leftAt":1}}}]}`)}, "rules: Order's use status (Workflow) has no field leftAt in the behaviors' layout"},
			{[]string{changed(`{"rows":[{"kind":"update","type":"Order","key":"` + uuidKey + `","expectedVersion":1,"own":{},"owned":{"status":{"enteredAt":"soon"}}}]}`)}, `rules: Order.statusEnteredAt: "soon" is no epoch milliseconds`},
			{[]string{changed(`{"rows":[{"kind":"create","type":"Order","key":null,"own":{},"owned":{}}]}`)}, "rules: a create of Order reached the adapter without a key"},
			{[]string{changed(`{"rows":[{"kind":"delete","type":"Invoice","key":"i1","expectedVersion":1}]}`)}, "rules: the behaviors' layout has no type Invoice; it holds Order"},
		}
		for _, c := range cases {
			got := refusal(c.steps...)
			want := internal(c.want)
			if got.Code != want.Code || got.Status != want.Status || got.Class != want.Class || got.Message != want.Message || got.Details != nil {
				t.Errorf("refusal = %+v\nwant      %+v", *got, want)
			}
		}
		// A null own field the layout has no column for clears nothing.
		err := inTx(t, db, rules.Host{Core: script(t, changed(`{"rows":[{"kind":"update","type":"Order","key":"`+uuidKey+`","expectedVersion":1,"own":{"colour":null,"title":"Chair"},"owned":{}}]}`)), Layout: layout}, nil, func(host rules.Host, _ *rulestest.Tx) error {
			_, err := rules.Execute(context.Background(), host, "Order", transition(uuidKey, "paid"), nil)
			return err
		})
		if err != nil {
			t.Fatalf("a null own field the layout lacks: %v", err)
		}
		if got := storedOrder(t, db, uuidColumn); got.Title != "Chair" {
			t.Errorf("the row = %+v, want Chair", got)
		}
	})
}

func TestACommandKeepsOneRequestIDForEveryAttempt(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createOrders(t, db, "order")
		insertOrder(t, db, uuidColumn, "Desk", "draft", 1000)
		core := script(t, changed(`{"rows":[{"kind":"delete","type":"Order","key":"`+uuidKey+`","expectedVersion":1}]}`))
		requests := 0
		host := rules.Host{Core: core, Layout: orderLayout("Identity.UUID", false), Request: func() string {
			requests++
			return fmt.Sprintf("r%d", requests)
		}}
		command := transition(uuidKey, "paid")
		command.Request = ""
		var result *rules.Result
		err := inTx(t, db, host, func(_ *rulestest.Tx, store rules.Store) rules.Store {
			wrapped, _ := record(store)
			wrapped.applies = []bool{false}
			return wrapped
		}, func(host rules.Host, _ *rulestest.Tx) error {
			var err error
			result, err = rules.Execute(context.Background(), host, "Order", command, nil)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(core.commands) != 2 || core.commands[0].Request != "r1" || core.commands[1].Request != "r1" || result.Attempts != 2 {
			t.Errorf("requests seen = %v, %v; attempts %d; want r1 twice", core.commands[0].Request, core.commands[len(core.commands)-1].Request, result.Attempts)
		}
		if got := storedOrder(t, db, uuidColumn); got != nil {
			t.Errorf("the row = %+v, want it deleted", got)
		}
	})
}

func TestPrepareCreateAppliesNothingAndRefusesMoreThanTheRow(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createOrders(t, db, "order")
		layout := orderLayout("Identity.UUID", false)
		var rec *recorded
		err := inTx(t, db, coreHost(layout), func(_ *rulestest.Tx, store rules.Store) rules.Store {
			wrapped, r := record(store)
			rec = r
			return wrapped
		}, func(host rules.Host, _ *rulestest.Tx) error {
			ctx := context.Background()
			owned, err := rules.PrepareCreate(ctx, host, "Order", map[string]any{"title": "Desk"}, alice, nil)
			if err != nil {
				return err
			}
			wantJSON(t, "the owned fields", owned, `{"status":{"status":"draft","enteredAt":5000}}`)
			owned, err = rules.PrepareCreate(ctx, host, "Order", map[string]any{"title": "Desk"}, alice, &rules.CreateOptions{ID: uuidKey})
			if err != nil {
				return err
			}
			wantJSON(t, "the owned fields of a create with an id", owned, `{"status":{"status":"draft","enteredAt":5000}}`)
			_, err = rules.PrepareCreate(ctx, host, "Order", map[string]any{"statusEnteredAt": 1}, alice, nil)
			if refusalOf(t, err).Code != rules.CodeInvalidInstance {
				t.Errorf("an owned field in a prepared create = %v", err)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(rec.applied) != 0 || len(db.Rows(t, `SELECT id FROM "order"`)) != 0 {
			t.Errorf("applied %v, want nothing", rec.applied)
		}

		prepare := func(core rules.Core) error {
			return inTx(t, db, rules.Host{Core: core, Layout: layout}, nil, func(host rules.Host, _ *rulestest.Tx) error {
				_, err := rules.PrepareCreate(context.Background(), host, "Order", map[string]any{"title": "Desk"}, alice, nil)
				return err
			})
		}
		more := "rules: an upsert of Order inserts the row a create of the core gives it, and that create writes more than the row; create the row with a create of its own"
		for _, step := range []string{
			changed(`{"rows":[{"kind":"create","type":"Order","key":null,"own":{},"owned":{}},{"kind":"create","type":"Order","key":"o2","own":{},"owned":{}}]}`),
			changed(`{"rows":[{"kind":"create","type":"Order","key":null,"own":{},"owned":{}}],"events":[{}]}`),
			changed(`{"rows":[{"kind":"update","type":"Order","key":"o2","expectedVersion":1,"own":{},"owned":{}}]}`),
			changed(``),
		} {
			wantRefusal(t, prepare(script(t, step)), rules.Refusal{Code: rules.CodeInternal, Status: 500, Class: "Error", Message: more})
		}
		lock := "Stock/" + db.Name + db.Schema
		err = prepare(script(t,
			`{"lock":["`+lock+`"]}`,
			`{"read":[{"kind":"hasRows","type":"Order"}]}`,
			changed(`{"rows":[{"kind":"create","type":"Order","key":null,"own":{},"owned":{"status":{"status":"draft"}}}]}`),
		))
		if db.Dialect == rules.DialectSQLite {
			wantRefusal(t, err, rules.Refusal{Code: rules.CodeInternal, Status: 500, Class: "Error", Message: "rules: an upsert of Order binds what a create of the core gives the row, and that create reads under a lock this store holds only in a unit of its own; create the row with a create of its own"})
		} else if err != nil {
			t.Errorf("a prepared create under a held lock: %v", err)
		}
	})
}

func TestSoftDeleteStampsTheLiveRow(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createOrders(t, db, "order")
		layout := orderLayout("string", true)
		run := func(wrap func(*rulestest.Tx, rules.Store) rules.Store, fn func(host rules.Host) error) error {
			return inTx(t, db, coreHost(layout), wrap, func(host rules.Host, _ *rulestest.Tx) error { return fn(host) })
		}
		ctx := context.Background()
		soft := &rules.ExecuteOptions{SoftDelete: map[string]any{"deleted_at": at(db, 9000), "deleted_by": "u1"}}
		if err := run(nil, func(host rules.Host) error {
			_, err := rules.RunCreate(ctx, host, "Order", map[string]any{"title": "Leak"}, alice, &rules.CreateOptions{ID: "t1"})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		var rec *recorded
		if err := run(func(_ *rulestest.Tx, store rules.Store) rules.Store {
			wrapped, r := record(store)
			rec = r
			return wrapped
		}, func(host rules.Host) error {
			return rules.RunDelete(ctx, host, "Order", "t1", alice, soft)
		}); err != nil {
			t.Fatal(err)
		}
		p := db.P
		wantStatements(t, "unit", rec.applied[0], []rules.Statement{{
			Text:   fmt.Sprintf(`UPDATE "order" SET "deleted_at" = %s, "deleted_by" = %s, "_version" = "_version" + 1 WHERE "id" = %s AND "_version" = %s AND "deleted_at" IS NULL`, p(1), p(2), p(3), p(4)),
			Values: []any{at(db, 9000), "u1", "t1", int64(1)},
			Expect: 1,
		}})
		if got := storedOrder(t, db, "t1"); got == nil || !got.Deleted || got.Version != 2 || got.Status != "draft" {
			t.Errorf("the row = %+v, want it stamped deleted at version 2", got)
		}
		// A row its table holds deleted is not_found to the core.
		err := run(nil, func(host rules.Host) error {
			row, err := rules.ReadRow(ctx, host, "Order", "t1")
			if err != nil || row != nil {
				t.Errorf("a deleted row reads %+v, %v", row, err)
			}
			_, err = rules.RunOperation(ctx, host, "Order", "t1", "status", "transition", map[string]string{"to": "paid"}, alice, nil)
			return err
		})
		wantRefusal(t, err, rules.Refusal{Code: rules.CodeNotFound, Status: 404, Class: "EngineError", Message: "Order t1 does not exist"})
		if !rules.IsNotFound(fmt.Errorf("orm: %w", err)) {
			t.Error("IsNotFound reads no not_found through a wrap")
		}
		err = run(nil, func(host rules.Host) error { return rules.RunDelete(ctx, host, "Order", "t1", alice, soft) })
		if !rules.IsNotFound(err) {
			t.Errorf("a second delete = %v, want not_found", err)
		}

		// A soft delete another commits under a command moves the row to
		// not_found.
		if err := run(nil, func(host rules.Host) error {
			_, err := rules.RunCreate(ctx, host, "Order", map[string]any{"title": "Drip"}, alice, &rules.CreateOptions{ID: "t2"})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		applies := 0
		err = run(func(tx *rulestest.Tx, store rules.Store) rules.Store {
			wrapped, _ := record(store)
			wrapped.before = func(ctx context.Context, n int) error {
				applies++
				if n > 0 {
					return nil
				}
				_, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE "order" SET deleted_at = %s, _version = _version + 1 WHERE id = %s`, db.P(1), db.P(2)), db.Timestamp(time.Now()), "t2")
				return err
			}
			return wrapped
		}, func(host rules.Host) error {
			_, err := rules.RunOperation(ctx, host, "Order", "t2", "status", "transition", map[string]string{"to": "paid"}, alice, nil)
			return err
		})
		if !rules.IsNotFound(err) || applies != 1 {
			t.Errorf("a command whose row was deleted under it = %v after %d applies, want not_found after 1", err, applies)
		}
		// The rolled-back transaction leaves the row as it was.
		if got := storedOrder(t, db, "t2"); got == nil || got.Deleted || got.Status != "draft" || got.Version != 1 {
			t.Errorf("t2 = %+v", got)
		}
	})
}

func TestARawWriteOfAnOwnedColumnIsNeverRefused(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createOrders(t, db, "order")
		insertOrder(t, db, "o1", "Desk", "paid", 1000)
		// Past the adapter, as an operator's repair: the escape hatch D103
		// keeps open, which no trigger or check refuses.
		if n := db.Exec(t, `UPDATE "order" SET status = 'shipped', status_entered_at = NULL WHERE id = `+db.P(1), "o1"); n != 1 {
			t.Fatalf("the raw write changed %d rows", n)
		}
		if got := storedOrder(t, db, "o1"); got.Status != "shipped" || got.Version != 2 {
			t.Errorf("the row = %+v, want shipped at version 2 (the trigger moved it)", got)
		}
		layout := orderLayout("string", false)
		shipper := rules.Caller{Subject: "sam", Permissions: []string{"orders.ship"}}
		err := inTx(t, db, coreHost(layout), nil, func(host rules.Host, _ *rulestest.Tx) error {
			row, err := rules.ReadRow(context.Background(), host, "Order", "o1")
			if err != nil {
				return err
			}
			wantJSON(t, "the row read", row.Owned, `{"status":{"status":"shipped"}}`)
			_, err = rules.RunOperation(context.Background(), host, "Order", "o1", "status", "transition", map[string]string{"to": "paid"}, shipper, nil)
			return err
		})
		// shipped is terminal: no transition leaves it.
		wantRefusal(t, err, rules.Refusal{Code: rules.CodeVetoed, Status: 409, Class: "BehaviorVetoError", Message: "behavior Workflow vetoes transition of Order o1: shipped is a terminal state: no transition leaves it"})
	})
}

func TestAFailedCommandLeavesTheTransactionGoingOn(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createOrders(t, db, "order")
		insertOrder(t, db, "o3", "Lamp", "draft", 1000)
		layout := orderLayout("string", false)
		var after json.RawMessage
		err := inTx(t, db, coreHost(layout), nil, func(host rules.Host, tx *rulestest.Tx) error {
			ctx := context.Background()
			_, err := rules.RunOperation(ctx, host, "Order", "o3", "status", "transition", map[string]string{"to": "shipped"}, alice, nil)
			if refusalOf(t, err).Code != rules.CodeVetoed {
				t.Errorf("err = %v, want vetoed", err)
			}
			// A statement of a unit that fails undoes the unit and leaves the
			// transaction going on.
			if _, err := host.Store.Apply(ctx, []rules.Statement{
				{Text: `UPDATE "order" SET title = 'gone' WHERE id = ` + db.P(1), Values: []any{"o3"}, Expect: 1},
				{Text: `UPDATE "no_such_table" SET x = 1`, Expect: rules.AnyRows},
			}); err == nil {
				t.Error("a unit with a failed statement applied")
			}
			after, err = rules.RunOperation(ctx, host, "Order", "o3", "status", "transition", map[string]string{"to": "paid"}, alice, nil)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		wantJSON(t, "the result", after, `{"from":"draft","to":"paid"}`)
		if got := storedOrder(t, db, "o3"); got.Title != "Lamp" || got.Status != "paid" {
			t.Errorf("the row = %+v, want Lamp, paid", got)
		}
	})
}

func TestAJSONOwnFieldRoundTrips(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createOrders(t, db, "order")
		layout := orderLayout("string", false)
		layout.Types["Order"].Own = append(layout.Types["Order"].Own, rules.Column{Field: "data", Column: "data", Type: "Generic.JSON"})
		values := map[string]string{
			"j1": `{"a":[1,"x",true],"b":null}`,
			"j2": `"123"`,
			"j3": `[]`,
			"j4": `12.5`,
		}
		err := inTx(t, db, coreHost(layout), nil, func(host rules.Host, _ *rulestest.Tx) error {
			ctx := context.Background()
			for id, value := range values {
				if _, err := rules.RunCreate(ctx, host, "Order", map[string]any{"title": "Desk", "data": json.RawMessage(value)}, alice, &rules.CreateOptions{ID: id}); err != nil {
					return err
				}
			}
			// An update's patch writes the column too.
			return rules.RunUpdate(ctx, host, "Order", "j3", map[string]any{"data": map[string]any{"c": "d"}}, alice, nil)
		})
		if err != nil {
			t.Fatal(err)
		}
		values["j3"] = `{"c":"d"}`
		err = inTx(t, db, coreHost(layout), nil, func(host rules.Host, _ *rulestest.Tx) error {
			for id, value := range values {
				row, err := rules.ReadRow(context.Background(), host, "Order", id)
				if err != nil {
					return err
				}
				wantJSON(t, id, row.Own["data"], value)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}

func TestAHostWithoutALayoutOrAStoreIsAnError(t *testing.T) {
	ctx := context.Background()
	if _, err := rules.Execute(ctx, rules.Host{}, "Order", transition("o1", "paid"), nil); err == nil || err.Error() != "rules: the host has no layout" {
		t.Errorf("no layout: %v", err)
	}
	if _, err := rules.Execute(ctx, rules.Host{Layout: orderLayout("string", false)}, "Order", transition("o1", "paid"), nil); err == nil || err.Error() != "rules: the host has no store" {
		t.Errorf("no store: %v", err)
	}
}

func TestParseLayout(t *testing.T) {
	layout, err := json.Marshal(orderLayout("Identity.UUID", true))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := rules.ParseLayout(layout)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := json.Marshal(parsed)
	if !sameJSON(layout, again) {
		t.Errorf("the layout read back as %s", again)
	}
	order := parsed.Types["Order"]
	if order.Live == nil || order.Uses[0].Fields[1].Convert != rules.ConvertEpochMillis || order.Uses[0].Fields[1].Column.Column != "status_entered_at" || order.Spec.Uses[0].Anchor.Field != "status" {
		t.Errorf("the layout read = %+v", order)
	}
	for _, bad := range []string{`{`, `{}`, `{"types":{"Order":null}}`, `{"types":{"Order":{"type":"Invoice","table":"t","key":{"column":"id"},"version":"_version"}}}`, `{"types":{"Order":{"type":"Order","key":{"column":"id"},"version":"_version"}}}`} {
		if _, err := rules.ParseLayout([]byte(bad)); err == nil {
			t.Errorf("ParseLayout(%s) took it", bad)
		}
	}
	defer func() {
		if recover() == nil {
			t.Error("MustParseLayout took a document that does not parse")
		}
	}()
	rules.MustParseLayout([]byte(`{}`))
}
