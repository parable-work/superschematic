package loader

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader/verify"
	ir "github.com/parable-work/superschematic/ir"
)

// The fixtures of D149's Blueprint: a database whose Batch implements
// Blueprint over the jobs that point at it beside its status Workflow, its
// template stamping a job per step, and whose Job implements Constants
// over the fields a stamp sets and Dependencies beside its own Workflow;
// and an API that serves both tables' behavior operations.
const (
	blueprintDBFixture  = "tsreader/testdata/services/fixture-blueprint-db"
	blueprintAPIFixture = "tsreader/testdata/services/fixture-blueprint-api"
)

// TestBlueprintFixturesLoad: both fixtures load with one warning, the
// API's, that JobActions leaves Dependencies' listDependents out, and no
// error; Batch and Job stay tables, Batch implementing Blueprint and Job
// Constants and Dependencies beside their status Workflows, Blueprint's
// config read as the TypeScript form wrote it, its steps in their order;
// Blueprint adds no owned field and no sub-table, since it holds no state
// once its rows exist and its children are computed at a read; neither
// table gets a queue; the API's sets serve Batch's transition and Job's
// transition and blockers; and the expanded IR of both is held to its
// golden (D149).
func TestBlueprintFixturesLoad(t *testing.T) {
	var warnings []string
	warn := WithWarnings(func(d *verify.Diagnostic) { warnings = append(warnings, d.Msg) })
	db, err := LoadService(filepath.FromSlash(blueprintDBFixture), warn)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][]string{
		"Batch": {"Blueprint:Blueprint", "status:Workflow"},
		"Job":   {"Constants:Constants", "Dependencies:Dependencies", "status:Workflow"},
	} {
		td := db.Types[name]
		if td == nil || td.IsEngineType() || !td.Optimistic {
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
	var config string
	for _, ref := range db.Types["Batch"].ImplementsBehaviors() {
		if ref.Name == "Blueprint" {
			config = string(ref.Config)
		}
	}
	for _, text := range []string{
		`"copyFields":["topic"]`,
		`"keyField":"step"`,
		`"parentLink":"batch"`,
		`"schema":"Job"`,
		`"audit":{"after":["index"],"data":{"title":"Audit the index"},"when":{"field":"flags","includes":"audit"}}`,
	} {
		if !strings.Contains(config, text) {
			t.Errorf("Blueprint's config = %s, want it to hold %s", config, text)
		}
	}
	for _, name := range []string{"BatchBlueprintChildren", "BatchBlueprint"} {
		if sub := db.Types[name]; sub != nil {
			t.Errorf("Blueprint added %s, want no sub-table", sub.Name)
		}
	}
	compareExpanded(t, db, "fixture-blueprint-db.expanded.json")

	api, err := LoadService(filepath.FromSlash(blueprintAPIFixture), warn)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"JobActions: Job's Dependencies is a Dependencies, whose operation listDependents a table leaves out: the rows a row blocks are a query over every table's blockers, which the behaviors' core cannot enumerate, so the engine serves it alone, and @behaviorOperations leaves it out (D104)"}; !slices.Equal(warnings, want) {
		t.Errorf("the fixtures' warnings =\n%q\nwant\n%q", warnings, want)
	}
	var operations []string
	for _, set := range api.OperationSets {
		for _, op := range set.Operations {
			if marker := op.BehaviorOperation; marker != nil {
				operations = append(operations, set.Name+"."+op.Name+" "+op.RestPath+" "+marker.Use)
			}
		}
	}
	if want := []string{
		"BatchActions.statusTransition batch/{id}/status/transition status",
		"JobActions.dependenciesAddBlocker job/{id}/dependencies/add-blocker Dependencies",
		"JobActions.dependenciesRemoveBlocker job/{id}/dependencies/remove-blocker Dependencies",
		"JobActions.dependenciesListBlockers job/{id}/dependencies/list-blockers Dependencies",
		"JobActions.statusTransition job/{id}/status/transition status",
	}; !slices.Equal(operations, want) {
		t.Errorf("the behavior operations =\n%s\nwant\n%s", strings.Join(operations, "\n"), strings.Join(want, "\n"))
	}
	compareExpanded(t, api, "fixture-blueprint-api.expanded.json")
}
