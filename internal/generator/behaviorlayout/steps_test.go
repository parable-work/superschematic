package behaviorlayout_test

import (
	"encoding/json"
	"strings"
	"testing"
)

// stepsFixture is the DB service whose Batch implements Rollups over the
// steps that point at it and whose Step implements Dependencies, each
// beside its status Workflow (D104's step 4).
const stepsFixture = "../../loader/tsreader/testdata/services/fixture-steps-db"

// The layout document of Rollups and Dependencies on tables (D104's step
// 4): Batch's Rollups use has no owned field, no operation and no
// sub-table, since its values are computed at a read and its gate reads
// the steps through their relation's column, which Step's spec names as
// batchId, a relation to Batch; Step's Dependencies use has its blockers
// sub-table, keyed by number, and its addBlocker, removeBlocker and
// listBlockers, and leaves listDependents out. Neither table keeps a
// queue. testdata/fixture-steps-db.layout.json is the document.
func TestLayoutOfRollupsAndDependencies(t *testing.T) {
	layout := layoutOfFixture(t, stepsFixture, "fixture-steps-db")
	batch, step := layout.Types["Batch"], layout.Types["Step"]
	if batch == nil || step == nil {
		t.Fatalf("the layout's types = %+v, want Batch and Step", layout.Types)
	}
	rollups, err := json.Marshal(batch.Uses[0])
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"id":"Rollups","behavior":"Rollups","member":"rollups","fields":[],"operations":[]}`; string(rollups) != want {
		t.Errorf("Batch's Rollups use = %s, want %s", rollups, want)
	}
	deps := step.Uses[0]
	if deps.ID != "Dependencies" || len(deps.Fields) != 0 || len(deps.Tables) != 1 || deps.Tables[0].Name != "blockers" {
		t.Fatalf("Step's Dependencies use = %+v", deps)
	}
	var operations []string
	for _, op := range deps.Operations {
		operations = append(operations, op.Name)
	}
	if got := strings.Join(operations, ","); got != "addBlocker,removeBlocker,listBlockers" {
		t.Errorf("Step's Dependencies operations = %s, want addBlocker, removeBlocker and listBlockers", got)
	}
	relation, err := json.Marshal(step.Spec.Fields)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(relation), `{"name":"batchId","type":"Identity.UUID","relation":{"name":"batch","type":"Batch"}}`) {
		t.Errorf("Step's spec fields = %s, want batchId, the relation batch to Batch", relation)
	}
	for _, td := range []string{"Batch", "Step"} {
		if table := layout.Types[td]; table.Deadlines != nil || table.Changes != nil || table.Events != nil {
			t.Errorf("%s keeps a queue: %+v", td, table)
		}
	}
}
