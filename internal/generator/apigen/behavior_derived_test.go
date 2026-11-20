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

// derivedProvenanceFixtures are the fixtures of Derived and Provenance (D73
// on D103's step 4): fixture-derived-provenance-api serves the operations
// of fixture-derived-provenance-db's Ticket and Article from a
// @behaviorOperations set each, Derived's pulls and settles routes of the
// table as a whole, and runs no worker.
var derivedProvenanceFixtures = behaviorFixtures{"fixture-derived-provenance-api", "fixture-derived-provenance-db"}

// TestWriteAPIGoldenDerivedProvenance generates the Go API module of
// fixture-derived-provenance-api and holds the files the tables' Derived
// and Provenance uses change to their golden copies: the routes, whose
// handlers of staleDerivations, settleDerivations and failedDerivations run
// on the table as a whole and whose others run on the row; the behaviors'
// runner; the interfaces; and the OpenAPI document. Regenerate with:
// go test ./internal/generator/apigen -run TestWriteAPIGoldenDerivedProvenance -update
func TestWriteAPIGoldenDerivedProvenance(t *testing.T) {
	api, db := loadFixturePair(t, derivedProvenanceFixtures)
	output := generatePairAPI(t, derivedProvenanceFixtures, api, db)

	var calls []string
	for _, ep := range output.Endpoints {
		if bo := ep.BehaviorOperation(); bo == nil {
			continue
		}
		calls = append(calls, ep.Method+" "+ep.Path+" "+ep.BehaviorRepositoryCall()+" "+ep.BehaviorRunner())
	}
	slices.Sort(calls)
	want := []string{
		"POST /api/article/derived/failed-derivations GetArticleRepository().Derived().FailedDerivations runTableBehaviorOperation",
		"POST /api/article/derived/settle-derivations GetArticleRepository().Derived().SettleDerivations runTableBehaviorOperation",
		"POST /api/article/derived/stale-derivations GetArticleRepository().Derived().StaleDerivations runTableBehaviorOperation",
		"POST /api/article/{id}/derived/apply-derived GetArticleRepository().Derived().ApplyDerived runBehaviorOperation",
		"POST /api/article/{id}/derived/rederive GetArticleRepository().Derived().Rederive runBehaviorOperation",
		"POST /api/ticket/derived/failed-derivations GetTicketRepository().Derived().FailedDerivations runTableBehaviorOperation",
		"POST /api/ticket/derived/settle-derivations GetTicketRepository().Derived().SettleDerivations runTableBehaviorOperation",
		"POST /api/ticket/derived/stale-derivations GetTicketRepository().Derived().StaleDerivations runTableBehaviorOperation",
		"POST /api/ticket/{id}/derived/apply-derived GetTicketRepository().Derived().ApplyDerived runBehaviorOperation",
		"POST /api/ticket/{id}/derived/rederive GetTicketRepository().Derived().Rederive runBehaviorOperation",
		"POST /api/ticket/{id}/provenance/accept GetTicketRepository().Provenance().Accept runBehaviorOperation",
		"POST /api/ticket/{id}/provenance/reject GetTicketRepository().Provenance().Reject runBehaviorOperation",
		"POST /api/ticket/{id}/status/transition GetTicketRepository().Status().Transition runBehaviorOperation",
	}
	if !slices.Equal(calls, want) {
		t.Fatalf("the endpoints = %q, want %q", calls, want)
	}
	if len(output.Workers) != 0 {
		t.Fatalf("workers = %+v, want none: Derived and Provenance hear no write and arm no deadline", output.Workers)
	}

	paths := naming.LocalPaths{
		ScalarGo:        "/repo/third_party/superscalar/go",
		SchemaIR:        "/repo/ir",
		SchemaRuntimeGo: "/repo/runtime/schema/go",
		HTTPRuntimeGo:   "/repo/runtime/http/go",
		RulesGo:         "/repo/runtime/rules/go",
	}
	if err := apigen.SetReplacePaths(output, paths, "/repo/schemas/dist/api/"+derivedProvenanceFixtures.api); err != nil {
		t.Fatalf("set replace paths: %v", err)
	}
	outDir := t.TempDir()
	if err := apigen.WriteAPI(output, outDir); err != nil {
		t.Fatalf("write api: %v", err)
	}
	goldenDir := filepath.Join("testdata", "golden", derivedProvenanceFixtures.api)
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

// TestDerivedProvenanceAPICompiles generates fixture-derived-provenance-db's
// Go types and ORM and fixture-derived-provenance-api's Go types and API
// module into a temporary tree shaped as the dist, and builds and vets the
// API module: its handlers of Derived's pulls and settles call the ORM's
// operations of the table as a whole, which take no key, and its others
// the row's, under each use's member. Linking needs the core's archive,
// which a build does not; go vet type-checks the cgo package.
func TestDerivedProvenanceAPICompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	api, db := loadFixturePair(t, derivedProvenanceFixtures)
	apiDir, _ := writeFixtureModulesWith(t, derivedProvenanceFixtures, api, db, nil)
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
		"db.GetTicketRepository().Derived().StaleDerivations",
		"db.GetTicketRepository().Provenance().Accept",
	} {
		if !strings.Contains(string(routes), want) {
			t.Errorf("routes.go holds no %q", want)
		}
	}
}
