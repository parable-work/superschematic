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

// commerceFixtures are the fixtures of Stock and Holds (D70 on D103's step
// 4): fixture-commerce-api serves the operations of fixture-commerce-db's
// Inventory and Checkout from a @behaviorOperations set each, the contract
// Supply's among Inventory's, and runs no worker.
var commerceFixtures = behaviorFixtures{"fixture-commerce-api", "fixture-commerce-db"}

// TestWriteAPIGoldenCommerce generates the Go API module of
// fixture-commerce-api and holds the files the tables' Stock and Holds
// uses change to their golden copies: the routes, each a handler run on
// the row; the behaviors' runner; the interfaces; and the OpenAPI
// document. Regenerate with:
// go test ./internal/generator/apigen -run TestWriteAPIGoldenCommerce -update
func TestWriteAPIGoldenCommerce(t *testing.T) {
	api, db := loadFixturePair(t, commerceFixtures)
	output := generatePairAPI(t, commerceFixtures, api, db)

	var calls []string
	for _, ep := range output.Endpoints {
		if bo := ep.BehaviorOperation(); bo == nil {
			continue
		}
		calls = append(calls, ep.Method+" "+ep.Path+" "+ep.BehaviorRepositoryCall()+" "+ep.BehaviorRunner())
	}
	slices.Sort(calls)
	want := []string{
		"POST /api/checkout/{id}/holds/list-holds GetCheckoutRepository().Holds().ListHolds runBehaviorOperation",
		"POST /api/checkout/{id}/holds/retake-holds GetCheckoutRepository().Holds().RetakeHolds runBehaviorOperation",
		"POST /api/checkout/{id}/status/transition GetCheckoutRepository().Status().Transition runBehaviorOperation",
		"POST /api/inventory/{id}/stock/adjust-stock GetInventoryRepository().Stock().AdjustStock runBehaviorOperation",
		"POST /api/inventory/{id}/stock/check-hold GetInventoryRepository().Stock().CheckHold runBehaviorOperation",
		"POST /api/inventory/{id}/stock/commit-for GetInventoryRepository().Stock().CommitFor runBehaviorOperation",
		"POST /api/inventory/{id}/stock/count-stock GetInventoryRepository().Stock().CountStock runBehaviorOperation",
		"POST /api/inventory/{id}/stock/hold-for GetInventoryRepository().Stock().HoldFor runBehaviorOperation",
		"POST /api/inventory/{id}/stock/list-stock-movements GetInventoryRepository().Stock().ListStockMovements runBehaviorOperation",
		"POST /api/inventory/{id}/stock/receive-stock GetInventoryRepository().Stock().ReceiveStock runBehaviorOperation",
		"POST /api/inventory/{id}/stock/release-for GetInventoryRepository().Stock().ReleaseFor runBehaviorOperation",
		"POST /api/inventory/{id}/stock/return-for GetInventoryRepository().Stock().ReturnFor runBehaviorOperation",
	}
	if !slices.Equal(calls, want) {
		t.Fatalf("the endpoints = %q, want %q", calls, want)
	}
	if len(output.Workers) != 0 {
		t.Fatalf("workers = %+v, want none: Stock and Holds hear no write and arm no deadline", output.Workers)
	}

	paths := naming.LocalPaths{
		ScalarGo:        "/repo/third_party/superscalar/go",
		SchemaIR:        "/repo/ir",
		SchemaRuntimeGo: "/repo/runtime/schema/go",
		HTTPRuntimeGo:   "/repo/runtime/http/go",
		RulesGo:         "/repo/runtime/rules/go",
	}
	if err := apigen.SetReplacePaths(output, paths, "/repo/schemas/dist/api/"+commerceFixtures.api); err != nil {
		t.Fatalf("set replace paths: %v", err)
	}
	outDir := t.TempDir()
	if err := apigen.WriteAPI(output, outDir); err != nil {
		t.Fatalf("write api: %v", err)
	}
	goldenDir := filepath.Join("testdata", "golden", commerceFixtures.api)
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

// TestCommerceAPICompiles generates fixture-commerce-db's Go types and ORM
// and fixture-commerce-api's Go types and API module into a temporary tree
// shaped as the dist, and builds and vets the API module: its handlers call
// the ORM's operations of the row under each use's member, Stock's and the
// contract's under stock and Holds' under holds. Linking needs the core's
// archive, which a build does not; go vet type-checks the cgo package.
func TestCommerceAPICompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	api, db := loadFixturePair(t, commerceFixtures)
	apiDir, _ := writeFixtureModulesWith(t, commerceFixtures, api, db, nil)
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
		"db.GetInventoryRepository().Stock().ReceiveStock",
		"db.GetCheckoutRepository().Holds().RetakeHolds",
	} {
		if !strings.Contains(string(routes), want) {
			t.Errorf("routes.go holds no %q", want)
		}
	}
}
