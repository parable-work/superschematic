package permcatalog_test

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/permcatalog"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/verify"
	ir "github.com/parable-work/superschematic/ir"
)

const fixturesDir = "../../loader/tsreader/testdata/services"

// TestCatalogListsAWorkflowsOverridePermission (D104): an API that serves
// a table's behavior operations lists, under its Workflow's transition,
// the permission a transition names and the overridePermission that moves
// a caller past by, both of which the core checks; a Workflow that names
// neither lists none. fixture-workflow-principals-api serves
// fixture-workflow-principals-db's Question, whose status names
// questions.close and questions.moderate, which reassignQuestion's route
// requires too.
func TestCatalogListsAWorkflowsOverridePermission(t *testing.T) {
	const api, db = "fixture-workflow-principals-api", "fixture-workflow-principals-db"
	dbSchema, err := loader.LoadService(filepath.Join(fixturesDir, db))
	if err != nil {
		t.Fatal(err)
	}
	apiSchema, err := loader.LoadService(filepath.Join(fixturesDir, api), loader.WithWarnings(func(*verify.Diagnostic) {}))
	if err != nil {
		t.Fatal(err)
	}
	output, err := apigen.Generate(apiSchema, apigen.Options{
		Provider:           sessionauth.Provider{},
		SchemaName:         api,
		ModulePath:         "example.com/schemas/api/" + api,
		TypesModule:        "example.com/schemas/types/go/" + api,
		Dependencies:       map[string]*ir.Schema{db: dbSchema},
		ModuleDependencies: []string{"example.com/schemas/types/go/" + db},
	})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := permcatalog.Parse([]byte(output.PermissionCatalogJSON))
	if err != nil {
		t.Fatalf("permissions.json: %v\n%s", err, output.PermissionCatalogJSON)
	}
	operations := map[string][]string{}
	for _, p := range catalog.Permissions {
		operations[p.Name] = p.Operations
	}
	want := map[string][]string{
		"questions.close":    {"QuestionStatusTransitionHandler"},
		"questions.moderate": {"QuestionAdminReassignQuestionHandler", "QuestionStatusTransitionHandler"},
	}
	if len(operations) != len(want) {
		t.Errorf("the catalog = %v, want %v", operations, want)
	}
	for name, ops := range want {
		if got := operations[name]; !slices.Equal(got, ops) {
			t.Errorf("%s is required by %v, want %v", name, got, ops)
		}
	}
}

// TestCatalogListsASeatsTakeAndAssign (D104): an API that serves a table's
// Seats lists, under each of Seats' operations, the take and assign
// permissions its seats name, which no member's name says is a permission,
// and under Reconcile's observe its observePermission, each of which the
// core checks. fixture-seats-api serves fixture-seats-db's Problem, whose
// seats take problems.own and problems.work and assign problems.assign and
// problems.release, and whose status names problems.override, and its
// Sandbox, whose observed names sandboxes.observe and whose status's stop
// sandboxes.stop.
func TestCatalogListsASeatsTakeAndAssign(t *testing.T) {
	const api, db = "fixture-seats-api", "fixture-seats-db"
	dbSchema, err := loader.LoadService(filepath.Join(fixturesDir, db))
	if err != nil {
		t.Fatal(err)
	}
	apiSchema, err := loader.LoadService(filepath.Join(fixturesDir, api), loader.WithWarnings(func(*verify.Diagnostic) {}))
	if err != nil {
		t.Fatal(err)
	}
	output, err := apigen.Generate(apiSchema, apigen.Options{
		Provider:           sessionauth.Provider{},
		SchemaName:         api,
		ModulePath:         "example.com/schemas/api/" + api,
		TypesModule:        "example.com/schemas/types/go/" + api,
		Dependencies:       map[string]*ir.Schema{db: dbSchema},
		ModuleDependencies: []string{"example.com/schemas/types/go/" + db},
	})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := permcatalog.Parse([]byte(output.PermissionCatalogJSON))
	if err != nil {
		t.Fatalf("permissions.json: %v\n%s", err, output.PermissionCatalogJSON)
	}
	operations := map[string][]string{}
	for _, p := range catalog.Permissions {
		operations[p.Name] = p.Operations
	}
	seats := []string{"ProblemSeatsLeaveSeatHandler", "ProblemSeatsListSeatsHandler", "ProblemSeatsTakeSeatHandler"}
	want := map[string][]string{
		"problems.assign":   seats,
		"problems.own":      seats,
		"problems.release":  seats,
		"problems.work":     seats,
		"problems.override": {"ProblemStatusTransitionHandler"},
		"sandboxes.observe": {"SandboxObservedObserveHandler"},
		"sandboxes.stop":    {"SandboxStatusTransitionHandler"},
	}
	if len(operations) != len(want) {
		t.Errorf("the catalog = %v, want %v", operations, want)
	}
	for name, ops := range want {
		if got := operations[name]; !slices.Equal(got, ops) {
			t.Errorf("%s is required by %v, want %v", name, got, ops)
		}
	}
}
