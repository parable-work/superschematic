package apigen_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/naming"
)

// numberingFixtures are the fixtures of Numbering (D103's step 4):
// fixture-numbering-api serves the operations of fixture-numbering-db's
// Invoice and Ticket, whose fields carry Numbering, from a
// @behaviorOperations set each, and runs no worker.
var numberingFixtures = behaviorFixtures{"fixture-numbering-api", "fixture-numbering-db"}

// TestWriteAPIGoldenNumbering generates the Go API module of
// fixture-numbering-api and holds the files the tables' Numbering fields
// change to their golden copies: the routes, whose handlers of each
// field's assignNumber run it with runBehaviorOperation on the row; the
// behaviors' runner; the interfaces; and the OpenAPI document. Its
// operations name no permission, so it has no permission catalog.
// Regenerate with:
// go test ./internal/generator/apigen -run TestWriteAPIGoldenNumbering -update
func TestWriteAPIGoldenNumbering(t *testing.T) {
	api, db := loadFixturePair(t, numberingFixtures)
	output := generatePairAPI(t, numberingFixtures, api, db)

	var calls []string
	for _, ep := range output.Endpoints {
		if bo := ep.BehaviorOperation(); bo == nil {
			continue
		}
		calls = append(calls, ep.Method+" "+ep.Path+" "+ep.BehaviorRepositoryCall()+" "+ep.BehaviorRunner())
	}
	slices.Sort(calls)
	want := []string{
		"POST /api/invoice/{id}/number/assign-number GetInvoiceRepository().Number().AssignNumber runBehaviorOperation",
		"POST /api/ticket/{id}/number/assign-number GetTicketRepository().Number().AssignNumber runBehaviorOperation",
		"POST /api/ticket/{id}/reference/assign-number GetTicketRepository().Reference().AssignNumber runBehaviorOperation",
	}
	if !slices.Equal(calls, want) {
		t.Fatalf("Numbering's endpoints = %q, want %q", calls, want)
	}
	if len(output.Workers) != 0 {
		t.Fatalf("workers = %+v, want none: Numbering keeps nothing for one to run", output.Workers)
	}

	paths := naming.LocalPaths{
		ScalarGo:        "/repo/third_party/superscalar/go",
		SchemaIR:        "/repo/ir",
		SchemaRuntimeGo: "/repo/runtime/schema/go",
		HTTPRuntimeGo:   "/repo/runtime/http/go",
		RulesGo:         "/repo/runtime/rules/go",
	}
	if err := apigen.SetReplacePaths(output, paths, "/repo/schemas/dist/api/"+numberingFixtures.api); err != nil {
		t.Fatalf("set replace paths: %v", err)
	}
	outDir := t.TempDir()
	if err := apigen.WriteAPI(output, outDir); err != nil {
		t.Fatalf("write api: %v", err)
	}
	goldenDir := filepath.Join("testdata", "golden", numberingFixtures.api)
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

// TestNumberingAPICompiles generates fixture-numbering-db's Go types and
// ORM and fixture-numbering-api's Go types and API module into a temporary
// tree shaped as the dist, and builds and vets the API module: its handlers
// of each Numbering field's assignNumber call the ORM's AssignNumber under
// the field, through runBehaviorOperation, with no input: the operation
// declares no parameter, and the core is sent the empty object. Linking
// needs the core's archive, which a build does not; go vet type-checks the
// cgo package.
func TestNumberingAPICompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	api, db := loadFixturePair(t, numberingFixtures)
	apiDir, _ := writeFixtureModulesWith(t, numberingFixtures, api, db, nil)
	for _, args := range [][]string{{"mod", "tidy"}, {"build", "./..."}, {"vet", "./..."}} {
		cmd := exec.Command("go", args...)
		cmd.Dir = apiDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go %s in the generated API module: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	routes, err := os.ReadFile(filepath.Join(apiDir, "routes.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"runBehaviorOperation(r.Context(), db.GetInvoiceRepository().Number().AssignNumber, Id, nil, &result)",
		"runBehaviorOperation(r.Context(), db.GetTicketRepository().Reference().AssignNumber, Id, nil, &result)",
	} {
		if !strings.Contains(string(routes), want) {
			t.Errorf("routes.go holds no %q", want)
		}
	}
}
