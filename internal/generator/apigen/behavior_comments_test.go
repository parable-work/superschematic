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

// commentsFixtures are the fixtures of Comments (D103's step 4):
// fixture-comments-api serves the operations of fixture-comments-db's
// Ticket, which implements Comments, from a @behaviorOperations set, and
// runs no worker.
var commentsFixtures = behaviorFixtures{"fixture-comments-api", "fixture-comments-db"}

// commentsRoutes are the routes of Ticket's Comments, each a call of the
// repository's operation under Comments(): the operations a table runs,
// every writing one beside the row.
var commentsRoutes = []string{
	"delete-message DeleteMessage", "edit Edit", "follow Follow", "get-message GetMessage", "list-edits ListEdits",
	"list-messages ListMessages", "list-pinned ListPinned", "list-reactions ListReactions", "mark-read MarkRead", "pin Pin",
	"post Post", "purge Purge", "react React", "reopen Reopen", "resolve Resolve", "unpin Unpin", "unreact Unreact",
}

// TestWriteAPIGoldenComments generates the Go API module of
// fixture-comments-api and holds the files Ticket's Comments changes to
// their golden copies: the routes, whose handlers of each operation run it
// with runBehaviorOperation on the row; the behaviors' runner; the
// interfaces; and the OpenAPI document. Its operations name no
// permission, since the config's permissions are the core's to ask.
// Regenerate with:
// go test ./internal/generator/apigen -run TestWriteAPIGoldenComments -update
func TestWriteAPIGoldenComments(t *testing.T) {
	api, db := loadFixturePair(t, commentsFixtures)
	output := generatePairAPI(t, commentsFixtures, api, db)

	var calls []string
	for _, ep := range output.Endpoints {
		if bo := ep.BehaviorOperation(); bo == nil {
			continue
		}
		calls = append(calls, ep.Method+" "+ep.Path+" "+ep.BehaviorRepositoryCall()+" "+ep.BehaviorRunner())
	}
	slices.Sort(calls)
	var want []string
	for _, route := range commentsRoutes {
		path, method, _ := strings.Cut(route, " ")
		want = append(want, "POST /api/ticket/{id}/comments/"+path+" GetTicketRepository().Comments()."+method+" runBehaviorOperation")
	}
	if !slices.Equal(calls, want) {
		t.Fatalf("Comments' endpoints =\n%s\nwant\n%s", strings.Join(calls, "\n"), strings.Join(want, "\n"))
	}
	if len(output.Workers) != 0 {
		t.Fatalf("workers = %+v, want none: Comments keeps nothing for one to run", output.Workers)
	}

	paths := naming.LocalPaths{
		ScalarGo:        "/repo/third_party/superscalar/go",
		SchemaIR:        "/repo/ir",
		SchemaRuntimeGo: "/repo/runtime/schema/go",
		HTTPRuntimeGo:   "/repo/runtime/http/go",
		RulesGo:         "/repo/runtime/rules/go",
	}
	if err := apigen.SetReplacePaths(output, paths, "/repo/schemas/dist/api/"+commentsFixtures.api); err != nil {
		t.Fatalf("set replace paths: %v", err)
	}
	outDir := t.TempDir()
	if err := apigen.WriteAPI(output, outDir); err != nil {
		t.Fatalf("write api: %v", err)
	}
	goldenDir := filepath.Join("testdata", "golden", commentsFixtures.api)
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

// TestCommentsAPICompiles generates fixture-comments-db's Go types and ORM
// and fixture-comments-api's Go types and API module into a temporary tree
// shaped as the dist, and builds and vets the API module: its handlers of
// each of Comments' operations call the ORM's operation under Comments(),
// through runBehaviorOperation, with the input the declaration types.
// Linking needs the core's archive, which a build does not; go vet
// type-checks the cgo package.
func TestCommentsAPICompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	api, db := loadFixturePair(t, commentsFixtures)
	apiDir, _ := writeFixtureModulesWith(t, commentsFixtures, api, db, nil)
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
		"runBehaviorOperation(r.Context(), db.GetTicketRepository().Comments().Post, Id, ",
		"runBehaviorOperation(r.Context(), db.GetTicketRepository().Comments().ListMessages, Id, ",
	} {
		if !strings.Contains(string(routes), want) {
			t.Errorf("routes.go holds no %q", want)
		}
	}
}
