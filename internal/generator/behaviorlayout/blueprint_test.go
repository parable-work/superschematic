package behaviorlayout_test

import (
	"encoding/json"
	"strings"
	"testing"
)

// blueprintFixture is the DB service whose Batch implements Blueprint over
// the jobs that point at it, beside its status Workflow, and whose Job
// implements Constants and Dependencies beside its own (D149).
const blueprintFixture = "../../loader/tsreader/testdata/services/fixture-blueprint-db"

// The layout document of Blueprint on a table (D149): Batch's Blueprint use
// has no owned field, no operation and no sub-table, since it holds no
// state once its rows exist and its children are computed at a read
// through the jobs' relation's column, which Job's spec names as batchId, a
// relation to Batch, beside step and topic, the fields a stamp sets. Job's
// Dependencies use has its blockers sub-table. Neither table keeps a queue.
// testdata/fixture-blueprint-db.layout.json is the document.
func TestLayoutOfBlueprint(t *testing.T) {
	layout := layoutOfFixture(t, blueprintFixture, "fixture-blueprint-db")
	batch, job := layout.Types["Batch"], layout.Types["Job"]
	if batch == nil || job == nil {
		t.Fatalf("the layout's types = %+v, want Batch and Job", layout.Types)
	}
	blueprint, err := json.Marshal(batch.Uses[0])
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"id":"Blueprint","behavior":"Blueprint","member":"blueprint","fields":[],"operations":[]}`; string(blueprint) != want {
		t.Errorf("Batch's Blueprint use = %s, want %s", blueprint, want)
	}
	fields, err := json.Marshal(job.Spec.Fields)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`{"name":"batchId","type":"Identity.UUID","relation":{"name":"batch","type":"Batch"}}`,
		`{"name":"step","type":"string"}`,
		`{"name":"topic","type":"string"}`,
	} {
		if !strings.Contains(string(fields), want) {
			t.Errorf("Job's spec fields = %s, want them to hold %s", fields, want)
		}
	}
	for _, td := range []string{"Batch", "Job"} {
		if table := layout.Types[td]; table.Deadlines != nil || table.Changes != nil || table.Events != nil {
			t.Errorf("%s keeps a queue: %+v", td, table)
		}
	}
}
