package loader

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader/verify"
	ir "github.com/parable-work/superschematic/ir"
)

// The fixtures of D104's step 4's first wave: a database whose Batch
// implements Rollups over the steps that point at it, the rows its steps
// HasMany lists, beside its Workflow field, a done rollup gating its done
// state, and whose Step implements Dependencies beside its own, gating
// doing and done; and an API that serves both tables' behavior operations.
const (
	stepsDBFixture  = "tsreader/testdata/services/fixture-steps-db"
	stepsAPIFixture = "tsreader/testdata/services/fixture-steps-api"
)

// TestStepsFixturesLoad: both fixtures load with one warning, the API's,
// that StepActions leaves Dependencies' listDependents out, and no error;
// Batch and Step stay tables, implementing Rollups and Dependencies beside
// their status Workflows, their configs read as the TypeScript form wrote
// them; Rollups adds no owned field and no sub-table, since its values are
// computed at a read, and Dependencies its blockers sub-table and no owned
// field; neither table gets a queue, since neither behavior arms deadlines,
// hears a write or emits an event; the API's sets serve Batch's transition
// and Step's transition, addBlocker, removeBlocker and listBlockers; and
// the expanded IR of both is held to its golden (D104's step 4).
func TestStepsFixturesLoad(t *testing.T) {
	var warnings []string
	warn := WithWarnings(func(d *verify.Diagnostic) { warnings = append(warnings, d.Msg) })
	db, err := LoadService(filepath.FromSlash(stepsDBFixture), warn)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][]string{
		"Batch": {"Rollups:Rollups", "status:Workflow"},
		"Step":  {"Dependencies:Dependencies", "status:Workflow"},
	} {
		td := db.Types[name]
		if td == nil || len(td.TypeBehaviors()) > 0 || !td.Optimistic {
			t.Fatalf("%s = %s", name, marshal(t, td))
		}
		var uses []string
		for _, ref := range td.TableBehaviors() {
			uses = append(uses, ref.UseID()+":"+ref.Name)
		}
		if !slices.Equal(uses, want) {
			t.Errorf("%s's uses = %v, want %v", name, uses, want)
		}
		var owned []string
		for _, fd := range td.Fields {
			if fd.Owned() {
				owned = append(owned, fd.Name)
			}
		}
		if want := []string{"status", "statusEnteredAt"}; !slices.Equal(owned, want) {
			t.Errorf("%s's owned fields = %v, want %v, its Workflow's alone", name, owned, want)
		}
		for _, kind := range []string{ir.BehaviorQueueDeadlines, ir.BehaviorQueueChanges, ir.BehaviorQueueEvents} {
			if queue := db.BehaviorQueueOf(name, kind); queue != nil {
				t.Errorf("%s's %s queue = %s, want none", name, kind, queue.Name)
			}
		}
	}
	configs := map[string]string{}
	for _, name := range []string{"Batch", "Step"} {
		for _, ref := range db.Types[name].ImplementsBehaviors() {
			configs[ref.Name] = string(ref.Config)
		}
	}
	for use, held := range map[string][]string{
		"Rollups":      {`"done":{"function":"all","gatedStates":["done"],"link":"batch","outcomes":["success"],"schema":"Step"}`, `"byStatus":{"field":"status","function":"countBy","link":"batch","schema":"Step"}`},
		"Dependencies": {`"gatedStates":["doing","done"]`},
	} {
		for _, text := range held {
			if !strings.Contains(configs[use], text) {
				t.Errorf("%s's config = %s, want it to hold %s", use, configs[use], text)
			}
		}
	}
	blockers := db.Types["StepDependenciesBlockers"]
	if blockers == nil || blockers.Origin != ir.OriginBehavior || blockers.BehaviorTable == nil || blockers.BehaviorTable.Use != "Dependencies" {
		t.Fatalf("StepDependenciesBlockers = %s", marshal(t, blockers))
	}
	var fields []string
	for _, fd := range blockers.Fields {
		fields = append(fields, fd.Name+":"+fd.TypeRef.Name)
	}
	if want := []string{"step:Step", "number:string", "blockerType:string", "blockerKey:string", "addedBy:string", "addedAt:Temporal.DateTime"}; !slices.Equal(fields, want) {
		t.Errorf("StepDependenciesBlockers' fields = %v, want %v", fields, want)
	}
	if sub := db.Types["BatchRollups"]; sub != nil {
		t.Errorf("Rollups added %s, want no sub-table", sub.Name)
	}
	compareExpanded(t, db, "fixture-steps-db.expanded.json")

	api, err := LoadService(filepath.FromSlash(stepsAPIFixture), warn)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"StepActions: Step's Dependencies is a Dependencies, whose operation listDependents a table leaves out: the rows a row blocks are a query over every table's blockers, which the behaviors' core cannot enumerate, so the engine serves it alone, and @behaviorOperations leaves it out (D104)"}; !slices.Equal(warnings, want) {
		t.Errorf("the fixtures' warnings =\n%q\nwant\n%q", warnings, want)
	}
	var operations []string
	for _, set := range api.OperationSets {
		for _, op := range set.Operations {
			if marker := op.BehaviorOperation; marker != nil {
				operations = append(operations, set.Name+"."+op.Name+" "+op.RestPath+" "+op.MCP.Handle+" "+marker.Use)
			}
		}
	}
	if want := []string{
		"BatchActions.statusTransition batch/{id}/status/transition batch_status_transition status",
		"StepActions.dependenciesAddBlocker step/{id}/dependencies/add-blocker step_dependencies_add_blocker Dependencies",
		"StepActions.dependenciesRemoveBlocker step/{id}/dependencies/remove-blocker step_dependencies_remove_blocker Dependencies",
		"StepActions.dependenciesListBlockers step/{id}/dependencies/list-blockers step_dependencies_list_blockers Dependencies",
		"StepActions.statusTransition step/{id}/status/transition step_status_transition status",
	}; !slices.Equal(operations, want) {
		t.Errorf("the behavior operations =\n%s\nwant\n%s", strings.Join(operations, "\n"), strings.Join(want, "\n"))
	}
	compareExpanded(t, api, "fixture-steps-api.expanded.json")
}
