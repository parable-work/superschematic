package apigen_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/permcatalog"
	"github.com/parable-work/superschematic/internal/loader/behaviorops"
	ir "github.com/parable-work/superschematic/ir"
)

// approvalsFixtures are the Approvals fixtures (D104): fixture-approvals-api
// serves the operations of fixture-approvals-db's Task, which implements
// Constants, StateRules and Approvals beside its Workflow on status, from a
// @behaviorOperations set, beside TaskAdmin.reassignTask, which updates a
// task's reviewer and owner through TaskAssignment, a @source view of it.
var approvalsFixtures = behaviorFixtures{"fixture-approvals-api", "fixture-approvals-db"}

// generateApprovalsAPI is the API output of fixture-approvals-api over
// fixture-approvals-db as the generator package generates it: its
// WritePermissions behaviorops.WritePermissions over the API and its
// database, unless writes is false.
func generateApprovalsAPI(t *testing.T, api, db *ir.Schema, writes bool) *apigen.APIOutput {
	t.Helper()
	f := approvalsFixtures
	deps := map[string]*ir.Schema{f.db: db}
	opts := apigen.Options{
		Provider:           sessionauth.Provider{},
		SchemaName:         f.api,
		ModulePath:         "example.com/schemas/api/" + f.api,
		TypesModule:        "example.com/schemas/types/go/" + f.api,
		Dependencies:       deps,
		ModuleDependencies: []string{"example.com/schemas/types/go/" + f.db},
		Clock:              goModuleClock,
	}
	if writes {
		opts.WritePermissions = func(op *ir.FieldDef) []string { return behaviorops.WritePermissions(api, op, deps) }
	}
	output, err := apigen.Generate(api, opts)
	if err != nil {
		t.Fatalf("generate %s: %v", f.api, err)
	}
	if output == nil {
		t.Fatalf("no API output for %s", f.api)
	}
	return output
}

// TestWriteAPIGoldenApprovals generates the Go API module of
// fixture-approvals-api and holds the files the table's behaviors change to
// their golden copies: the routes, whose handlers call the ORM's
// Approvals().SignOff, WithdrawSignOff and ListSignOffs and
// Status().Transition; the interfaces, whose one method is reassignTask's;
// behaviors.go; deps.go; the OpenAPI document; and the permission catalog.
// Constants and StateRules add no route. Regenerate with:
// go test ./internal/generator/apigen -run TestWriteAPIGoldenApprovals -update
func TestWriteAPIGoldenApprovals(t *testing.T) {
	api, db := loadFixturePair(t, approvalsFixtures)
	output := generateApprovalsAPI(t, api, db, true)

	var behaviors []string
	for _, ep := range output.Endpoints {
		if bo := ep.BehaviorOperation(); bo != nil {
			behaviors = append(behaviors, ep.Method+" "+ep.Path+" "+ep.BehaviorRepositoryCall())
		}
	}
	want := []string{
		"POST /api/task/{id}/approvals/list-sign-offs GetTaskRepository().Approvals().ListSignOffs",
		"POST /api/task/{id}/approvals/sign-off GetTaskRepository().Approvals().SignOff",
		"POST /api/task/{id}/approvals/withdraw-sign-off GetTaskRepository().Approvals().WithdrawSignOff",
		"POST /api/task/{id}/status/transition GetTaskRepository().Status().Transition",
	}
	if !slices.Equal(behaviors, want) {
		t.Fatalf("behavior endpoints = %q, want %q", behaviors, want)
	}
	if output.Deps.Database != approvalsFixtures.db || !output.Deps.Rules {
		t.Fatalf("Deps = %+v, want the ORM of %s, which requires the rules module", output.Deps, approvalsFixtures.db)
	}
	if len(output.Workers) != 0 {
		t.Fatalf("workers = %+v, want none: Task's behaviors arm no deadline and hear no change", output.Workers)
	}

	paths := naming.LocalPaths{
		ScalarGo:        "/repo/third_party/superscalar/go",
		SchemaIR:        "/repo/ir",
		SchemaRuntimeGo: "/repo/runtime/schema/go",
		HTTPRuntimeGo:   "/repo/runtime/http/go",
		RulesGo:         "/repo/runtime/rules/go",
	}
	if err := apigen.SetReplacePaths(output, paths, "/repo/schemas/dist/api/"+approvalsFixtures.api); err != nil {
		t.Fatalf("set replace paths: %v", err)
	}
	outDir := t.TempDir()
	if err := apigen.WriteAPI(output, outDir); err != nil {
		t.Fatalf("write api: %v", err)
	}
	goldenDir := filepath.Join("testdata", "golden", approvalsFixtures.api)
	for _, name := range []string{"go.mod", "routes.go", "interfaces.go", "behaviors.go", "deps.go", "openapi.json", "permissions.json"} {
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

// TestCatalogListsTheConstantsPermissionUnderAnUpdate holds the permission
// catalog of fixture-approvals-api to the permissions the core checks
// (D104): tasks.review, Approvals' gates', under each of its three
// operations; and tasks.admin, Constants' permission, which the core asks
// of an update that changes the owner or moves urgent or attempts back,
// under reassignTask, which updates a task through TaskAssignment, a
// @source view of it. That route requires tasks.admin of its own, so the
// catalog is held again with its requirement taken off: Constants'
// permission still lists it, and without WritePermissions nothing does.
func TestCatalogListsTheConstantsPermissionUnderAnUpdate(t *testing.T) {
	api, db := loadFixturePair(t, approvalsFixtures)
	operationsOf := func(catalog permcatalog.Catalog, name string) []string {
		for _, p := range catalog.Permissions {
			if p.Name == name {
				return p.Operations
			}
		}
		return nil
	}
	catalog, err := permcatalog.Parse([]byte(generateApprovalsAPI(t, api, db, true).PermissionCatalogJSON))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := catalog.Names(), []string{"tasks.admin", "tasks.review"}; !slices.Equal(got, want) {
		t.Fatalf("the catalog lists %v, want %v", got, want)
	}
	if got, want := operationsOf(catalog, "tasks.admin"), []string{"TaskAdminReassignTaskHandler"}; !slices.Equal(got, want) {
		t.Errorf("tasks.admin is needed by %v, want %v", got, want)
	}
	if got, want := operationsOf(catalog, "tasks.review"), []string{"TaskApprovalsListSignOffsHandler", "TaskApprovalsSignOffHandler", "TaskApprovalsWithdrawSignOffHandler"}; !slices.Equal(got, want) {
		t.Errorf("tasks.review is needed by %v, want %v", got, want)
	}

	// reassignTask with no requirement of its own.
	var reassign *ir.FieldDef
	for _, set := range api.OperationSets {
		for _, op := range set.Operations {
			if set.Name == "TaskAdmin" && op.Name == "reassignTask" {
				reassign = op
			}
		}
	}
	if reassign == nil {
		t.Fatal("fixture-approvals-api has no TaskAdmin.reassignTask")
	}
	reassign.Permissions = nil
	for _, writes := range []bool{true, false} {
		catalog, err := permcatalog.Parse([]byte(generateApprovalsAPI(t, api, db, writes).PermissionCatalogJSON))
		if err != nil {
			t.Fatal(err)
		}
		var want []string
		if writes {
			want = []string{"TaskAdminReassignTaskHandler"}
		}
		if got := operationsOf(catalog, "tasks.admin"); !slices.Equal(got, want) {
			t.Errorf("with WritePermissions %t and no requirement of reassignTask's own, tasks.admin is needed by %v, want %v", writes, got, want)
		}
	}
}

// TestApprovalsAPICompiles generates fixture-approvals-db's Go types and
// ORM and fixture-approvals-api's Go types and API module into a temporary
// tree shaped as the dist, and builds and vets the API module: its
// handlers call the ORM's Approvals and Workflow operations with its types,
// TaskApprovalsGate among them (D104). Linking needs the core's archive,
// which a build does not; go vet type-checks the cgo package.
func TestApprovalsAPICompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	api, db := loadFixturePair(t, approvalsFixtures)
	apiDir := writeFixtureModules(t, approvalsFixtures, api, db)
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
		"runBehaviorOperation(r.Context(), db.GetTaskRepository().Approvals().SignOff, Id, input, &result)",
		"runBehaviorOperation(r.Context(), db.GetTaskRepository().Approvals().ListSignOffs, Id, input, &result)",
	} {
		if !strings.Contains(string(routes), want) {
			t.Errorf("routes.go holds no %q", want)
		}
	}
}
