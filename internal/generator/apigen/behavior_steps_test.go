package apigen_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/naming"
)

// stepsFixtures are the fixtures of D104's step 4's first wave:
// fixture-steps-api serves the operations of fixture-steps-db's Batch,
// which implements Rollups beside its status Workflow, and its Step, which
// implements Dependencies beside its own, from two @behaviorOperations
// sets.
var stepsFixtures = behaviorFixtures{"fixture-steps-api", "fixture-steps-db"}

// TestWriteAPIGoldenSteps generates the Go API module of fixture-steps-api
// and holds the files Rollups and Dependencies change to their golden
// copies: the routes, whose handlers of Step's addBlocker, removeBlocker
// and listBlockers run them with runBehaviorOperation and whose set leaves
// listDependents out; Batch's transition, which Rollups' gate holds, and
// no route of Rollups, which declares no operation; behaviors.go; the
// interfaces; and the OpenAPI document.
// Regenerate with:
// go test ./internal/generator/apigen -run TestWriteAPIGoldenSteps -update
func TestWriteAPIGoldenSteps(t *testing.T) {
	api, db := loadFixturePair(t, stepsFixtures)
	output := generatePairAPI(t, stepsFixtures, api, db)

	var calls []string
	for _, ep := range output.Endpoints {
		if ep.BehaviorOperation() == nil {
			continue
		}
		calls = append(calls, ep.Method+" "+ep.Path+" "+ep.BehaviorRepositoryCall()+" "+ep.BehaviorRunner())
	}
	slices.Sort(calls)
	want := []string{
		"POST /api/batch/{id}/status/transition GetBatchRepository().Status().Transition runBehaviorOperation",
		"POST /api/step/{id}/dependencies/add-blocker GetStepRepository().Dependencies().AddBlocker runBehaviorOperation",
		"POST /api/step/{id}/dependencies/list-blockers GetStepRepository().Dependencies().ListBlockers runBehaviorOperation",
		"POST /api/step/{id}/dependencies/remove-blocker GetStepRepository().Dependencies().RemoveBlocker runBehaviorOperation",
		"POST /api/step/{id}/status/transition GetStepRepository().Status().Transition runBehaviorOperation",
	}
	if !slices.Equal(calls, want) {
		t.Fatalf("the behavior endpoints = %q, want %q", calls, want)
	}
	paths := naming.LocalPaths{
		ScalarGo:        "/repo/third_party/superscalar/go",
		SchemaIR:        "/repo/ir",
		SchemaRuntimeGo: "/repo/runtime/schema/go",
		HTTPRuntimeGo:   "/repo/runtime/http/go",
		RulesGo:         "/repo/runtime/rules/go",
	}
	if err := apigen.SetReplacePaths(output, paths, "/repo/schemas/dist/api/"+stepsFixtures.api); err != nil {
		t.Fatalf("set replace paths: %v", err)
	}
	outDir := t.TempDir()
	if err := apigen.WriteAPI(output, outDir); err != nil {
		t.Fatalf("write api: %v", err)
	}
	goldenDir := filepath.Join("testdata", "golden", stepsFixtures.api)
	for _, name := range []string{"go.mod", "routes.go", "interfaces.go", "behaviors.go", "openapi.json"} {
		got, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil {
			t.Fatalf("read generated %s: %v", name, err)
		}
		goldenPath := filepath.Join(goldenDir, name)
		if *update {
			if err := os.MkdirAll(goldenDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(goldenPath)
		if err != nil {
			t.Fatalf("read golden %s: %v", name, err)
		}
		if string(got) != string(want) {
			t.Errorf("%s differs from golden (run with -update to accept)", name)
		}
	}
}
