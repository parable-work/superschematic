package behaviorlayout_test

import (
	"encoding/json"
	"strings"
	"testing"
)

// poolFixture is the DB service whose Account implements Pool and Budget on
// one table (D104).
const poolFixture = "../../loader/tsreader/testdata/services/fixture-pool-db"

// The layout document of a table that implements Pool beside Budget
// (D104): Account's first use is Pool's, its four owned fields, the two
// instants converted as epoch milliseconds, markOut and reportRoom on a row
// and pick, an operation of the table as a whole that writes nothing, and
// no sub-table; then Budget's, with its three sub-tables. Pool's recheck is
// a message of Account's deadline queue and its event of the event queue,
// and nothing hears Account's writes. A read across the table takes its
// rows from the layout's own, live, key, version and uses, which hold no
// member of their own for it. testdata/fixture-pool-db.layout.json is the
// document the adapters of every language are written against.
func TestLayoutOfPool(t *testing.T) {
	layout := layoutOfFixture(t, poolFixture, "fixture-pool-db")
	td := layout.Types["Account"]
	if td == nil {
		t.Fatalf("the layout has no Account: %+v", layout.Types)
	}
	if len(td.Uses) != 2 || td.Uses[0].ID != "Pool" || td.Uses[0].Member != "pool" || td.Uses[1].ID != "Budget" || len(td.Uses[0].Tables) != 0 || len(td.Uses[1].Tables) != 3 {
		t.Fatalf("Account's uses = %+v, want Pool's and Budget's", td.Uses)
	}
	pool, err := json.Marshal(td.Uses[0])
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":"Pool","behavior":"Pool","member":"pool","fields":[` +
		`{"name":"availability","field":"poolAvailability","column":"pool_availability","type":"string"},` +
		`{"name":"outUntil","field":"poolOutUntil","column":"pool_out_until","type":"Temporal.DateTime","convert":"epochMillis"},` +
		`{"name":"outReason","field":"poolOutReason","column":"pool_out_reason","type":"string"},` +
		`{"name":"checkedAt","field":"poolCheckedAt","column":"pool_checked_at","type":"Temporal.DateTime","convert":"epochMillis"}],` +
		`"operations":[{"name":"markOut","writes":true},{"name":"reportRoom","writes":true},{"name":"pick","writes":false,"table":true}]}`
	if string(pool) != want {
		t.Errorf("Account's Pool =\n%s\nwant\n%s", pool, want)
	}
	var budget []string
	for _, op := range td.Uses[1].Operations {
		if op.Table {
			budget = append(budget, op.Name)
		}
	}
	if len(budget) != 0 {
		t.Errorf("Budget's operations of the table = %v, want none", budget)
	}
	for name, want := range map[string]any{
		"events":    `{"queue":"AccountEvent","table":"account_event_queue","rowKey":"row_key","use":"use_id","kind":"kind","at":"at","payload":"payload"}`,
		"deadlines": `{"queue":"AccountDeadline","table":"account_deadline_queue","rowKey":"row_key","use":"use_id","name":"name","at":"at","payload":"payload"}`,
		"changes":   `null`,
	} {
		member := map[string]any{"events": td.Events, "deadlines": td.Deadlines, "changes": td.Changes}[name]
		got, err := json.Marshal(member)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Errorf("Account's %s = %s, want %s", name, got, want)
		}
	}
	var own []string
	for _, column := range td.Own {
		own = append(own, column.Field)
	}
	if want := "owner, name, vendor, kind, rank"; strings.Join(own, ", ") != want {
		t.Errorf("Account's own = %s, want %s", strings.Join(own, ", "), want)
	}
}
