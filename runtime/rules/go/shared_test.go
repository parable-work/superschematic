package rules_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	rules "github.com/parable-work/superschematic/runtime/rules/go"
	"github.com/parable-work/superschematic/runtime/rules/go/internal/rulestest"
)

// A use's shared table (D103's step 4) through the adapter on each
// dialect: an invoice whose number is a Numbering field, its label on the
// field's own column and its seq beside it, and Numbering's counts, a table
// of the use kept for the table as a whole, keyed by the scope and held to a
// version of its own, as rules-shared.test.ts holds TypeScript's adapter.

const invoiceLayout = `{"types": {"Invoice": {
  "type": "Invoice", "table": "invoice",
  "key": {"field": "id", "column": "id", "type": "string"},
  "version": "_version",
  "own": [{"field": "title", "column": "title", "type": "string"}, {"field": "customerId", "column": "customer_id", "type": "string"}],
  "uses": [{
    "id": "number", "behavior": "Numbering", "anchor": "number", "member": "number",
    "fields": [
      {"name": "seq", "field": "numberSeq", "column": "number_seq", "type": "Generic.Int64"},
      {"name": "label", "field": "number", "column": "number", "type": "string", "value": true}
    ],
    "operations": [{"name": "assignNumber", "writes": true}],
    "shared": [{
      "name": "counts", "table": "invoice_number_counts",
      "key": {"name": "scope", "field": "scope", "column": "scope", "type": "string"},
      "fields": [
        {"name": "last", "field": "last", "column": "last", "type": "Generic.Int64"},
        {"name": "counting", "field": "counting", "column": "counting", "type": "string"}
      ],
      "version": "_version"
    }]
  }],
  "spec": {
    "name": "Invoice",
    "uses": [{"behavior": "Numbering", "config": {"format": "INV-{seq:3}", "scope": {"link": "customer"}}, "anchor": {"field": "number"}}],
    "fields": [{"name": "title", "type": "string"}, {"name": "customerId", "type": "string", "relation": {"name": "customer", "type": "Customer"}}]
  }
}}}`

const invoiceCounting = `{"format":"INV-{seq:3}","reset":null,"scope":"customer"}`

func createInvoices(t *testing.T, db *rulestest.DB) {
	t.Helper()
	types := db.ColumnTypes()
	db.Exec(t, fmt.Sprintf(`CREATE TABLE invoice (id %s, title %s, customer_id %s, number %s, number_seq %s, _version %s)`, types.Key, types.Text, types.Text, types.Text, types.Integer, types.Version))
	for _, ddl := range db.BumpVersion("invoice") {
		db.Exec(t, ddl)
	}
	db.Exec(t, fmt.Sprintf(`CREATE TABLE invoice_number_counts (scope %s, last %s, counting %s, _version %s)`, types.Key, types.Integer, types.Text, types.Version))
	for _, ddl := range db.BumpVersion("invoice_number_counts") {
		db.Exec(t, ddl)
	}
}

// labelOf is a row's label, "" for none.
func labelOf(t *testing.T, db *rulestest.DB, id string) string {
	t.Helper()
	rows := db.Rows(t, `SELECT number FROM invoice WHERE id = `+db.P(1), id)
	if len(rows) == 0 || rows[0][0] == nil {
		return ""
	}
	return fmt.Sprint(rows[0][0])
}

// countOf is a scope's count, its last and its version, as "last@version".
func countOf(t *testing.T, db *rulestest.DB, scope string) string {
	t.Helper()
	rows := db.Rows(t, `SELECT last, _version FROM invoice_number_counts WHERE scope = `+db.P(1), scope)
	if len(rows) == 0 {
		return ""
	}
	return fmt.Sprintf("%v@%v", rows[0][0], rows[0][1])
}

// racing is a store whose first apply sees a write through the same
// transaction first, as one committed between the read and the apply.
func racing(text string, values ...any) func(tx *rulestest.Tx, store rules.Store) rules.Store {
	return func(tx *rulestest.Tx, store rules.Store) rules.Store {
		wrapped, _ := record(store)
		wrapped.before = func(ctx context.Context, n int) error {
			if n > 0 {
				return nil
			}
			_, err := tx.Exec(ctx, text, values...)
			return err
		}
		return wrapped
	}
}

func TestASharedTableIsReadByKeyAndEachWriteIsHeldToItsVersion(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createInvoices(t, db)
		layout, err := rules.ParseLayout([]byte(invoiceLayout))
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		create := func(id, customer string, wrap func(tx *rulestest.Tx, store rules.Store) rules.Store) *rules.Result {
			t.Helper()
			var result *rules.Result
			err := inTx(t, db, coreHost(layout), wrap, func(host rules.Host, _ *rulestest.Tx) error {
				own := map[string]json.RawMessage{"title": json.RawMessage(fmt.Sprintf("%q", id)), "customerId": json.RawMessage(fmt.Sprintf("%q", customer))}
				var err error
				result, err = rules.Execute(ctx, host, "Invoice", rules.Command{Caller: alice, Now: 5000, Request: id, Action: rules.Action{Kind: rules.ActionCreate, ID: id, Own: own}}, nil)
				return err
			})
			if err != nil {
				t.Fatalf("create %s: %v", id, err)
			}
			return result
		}

		// A create inserts its count at version 1; the next updates it held
		// to its version.
		create("i1", "c1", nil)
		create("i2", "c1", nil)
		if got := labelOf(t, db, "i2"); got != "INV-002" {
			t.Errorf("i2's label = %q, want INV-002", got)
		}
		if got := countOf(t, db, "c1"); got != "2@2" {
			t.Errorf("c1's count = %s, want 2@2", got)
		}
		err = inTx(t, db, coreHost(layout), nil, func(host rules.Host, _ *rulestest.Tx) error {
			row, err := rules.ReadSharedRow(ctx, host, "Invoice", "number", "counts", "c1")
			if err != nil {
				return err
			}
			wantJSON(t, "c1's count", row, `{"key":"c1","values":{"last":2,"counting":`+fmt.Sprintf("%q", invoiceCounting)+`},"version":2}`)
			none, err := rules.ReadSharedRow(ctx, host, "Invoice", "number", "counts", "c9")
			if err != nil {
				return err
			}
			if none != nil {
				t.Errorf("c9's count = %+v, want none", none)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}

		// A count moved between the read and the apply moves the create,
		// which runs again and takes the next number.
		moved := create("i3", "c1", racing(`UPDATE invoice_number_counts SET last = last + 1, _version = _version + 1 WHERE scope = `+db.P(1), "c1"))
		if moved.Attempts != 2 || labelOf(t, db, "i3") != "INV-004" || countOf(t, db, "c1") != "4@4" {
			t.Errorf("after a moved count: attempts %d, label %q, count %s; want 2, INV-004, 4@4", moved.Attempts, labelOf(t, db, "i3"), countOf(t, db, "c1"))
		}

		// Another writer's first insert of a count moves the create too,
		// which then updates it.
		first := create("j1", "c2", racing(fmt.Sprintf(`INSERT INTO invoice_number_counts (scope, last, counting, _version) VALUES (%s, 5, %s, 1)`, db.P(1), db.P(2)), "c2", invoiceCounting))
		if first.Attempts != 2 || labelOf(t, db, "j1") != "INV-006" || countOf(t, db, "c2") != "6@2" {
			t.Errorf("after a first insert: attempts %d, label %q, count %s; want 2, INV-006, 6@2", first.Attempts, labelOf(t, db, "j1"), countOf(t, db, "c2"))
		}

		// A delete leaves the count where it was: a gap.
		err = inTx(t, db, coreHost(layout), nil, func(host rules.Host, _ *rulestest.Tx) error {
			return rules.RunDelete(ctx, host, "Invoice", "i3", alice, nil)
		})
		if err != nil {
			t.Fatal(err)
		}
		create("i5", "c1", nil)
		if got := labelOf(t, db, "i5"); got != "INV-005" {
			t.Errorf("i5's label = %q, want INV-005", got)
		}

		// On Postgres, creates that number at once take one number each:
		// each waits on the one before it and runs again over it.
		if db.Dialect != rules.DialectPostgres {
			return
		}
		var wg sync.WaitGroup
		for _, id := range []string{"k1", "k2", "k3", "k4"} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				create(id, "c3", nil)
			}()
		}
		wg.Wait()
		rows := db.Rows(t, `SELECT number FROM invoice WHERE customer_id = $1 ORDER BY number`, "c3")
		var labels []string
		for _, row := range rows {
			labels = append(labels, fmt.Sprint(row[0]))
		}
		if fmt.Sprint(labels) != "[INV-001 INV-002 INV-003 INV-004]" || countOf(t, db, "c3") != "4@4" {
			t.Errorf("concurrent creates = %v, count %s; want INV-001 to INV-004 and 4@4", labels, countOf(t, db, "c3"))
		}
	})
}
