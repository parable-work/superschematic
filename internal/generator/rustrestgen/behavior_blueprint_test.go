package rustrestgen

import (
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
)

// The blueprint fixture (D149): fixture-blueprint-api serves BatchActions
// and JobActions, @behaviorOperations of Batch and Job for any signed-in
// caller, over fixture-blueprint-db, its one DB dependency, whose Batch
// implements Blueprint, its create stamping a job per step of its template,
// and whose Job implements Constants and Dependencies. Blueprint declares
// no operation, so Batch's routes are its Workflow's; each is a route of
// the row run through the behaviors' core with run_operation; the API runs
// no worker.
var blueprintFixture = behaviorFixture{api: "fixture-blueprint-api", db: "fixture-blueprint-db"}

// TestWriteRustAPIGoldenBlueprint pins the crate of fixture-blueprint-api:
// the router mounts Batch's transition and Job's transition and blockers,
// each run through the behaviors' core with run_operation, and the layout
// it embeds holds Batch's Blueprint use, its config the template the core
// stamps from. Regenerate with:
// go test ./internal/generator/rustrestgen -run TestWriteRustAPIGoldenBlueprint -update
func TestWriteRustAPIGoldenBlueprint(t *testing.T) {
	_, db, reg := blueprintFixture.load(t)
	output := blueprintFixture.generate(t, db)
	deps := behaviorDeps(behaviorLayout(t, db, reg))
	deps.Database = blueprintFixture.db
	deps.DatabaseTypesCrate = naming.Default().RustTypesCrate(blueprintFixture.db)
	deps.DatabaseTypesPath = "../../types/rust/" + blueprintFixture.db
	if err := output.SetDeps(deps); err != nil {
		t.Fatalf("set deps: %v", err)
	}
	var behaviors []string
	for _, endpoint := range output.Endpoints {
		if b := endpoint.Behavior; b != nil {
			behaviors = append(behaviors, strings.ToUpper(endpoint.Method)+" "+endpoint.Path+" "+b.Type+"."+b.Use+"."+b.Operation)
		}
	}
	slices.Sort(behaviors)
	if want := []string{
		"POST /api/batch/{id}/status/transition Batch.status.transition",
		"POST /api/job/{id}/dependencies/add-blocker Job.Dependencies.addBlocker",
		"POST /api/job/{id}/dependencies/list-blockers Job.Dependencies.listBlockers",
		"POST /api/job/{id}/dependencies/remove-blocker Job.Dependencies.removeBlocker",
		"POST /api/job/{id}/status/transition Job.status.transition",
	}; !slices.Equal(behaviors, want) {
		t.Errorf("behavior operations\n%q, want\n%q", behaviors, want)
	}
	if len(output.Workers) != 0 || len(output.BehaviorWorkers) != 0 {
		t.Errorf("workers %+v and behavior workers %+v, want none", output.Workers, output.BehaviorWorkers)
	}
	generated := writeGoldenAPI(t, blueprintFixture.api, output)
	for file, want := range map[string][]string{
		"src/router.rs": {
			"behaviors::run_operation(\n        &state.implementations.behaviors,\n        &ctx,\n        \"Job\",\n        &args.id,\n        \"Dependencies\",\n        \"addBlocker\",",
		},
		"src/behaviors.rs": {
			`pub const RULES_LAYOUT: &str = r#"{`,
			`"behavior": "Blueprint"`,
			`"job_dependencies_blockers"`,
		},
	} {
		for _, text := range want {
			if !strings.Contains(generated[file], text) {
				t.Errorf("%s does not contain %q", file, text)
			}
		}
	}
}
