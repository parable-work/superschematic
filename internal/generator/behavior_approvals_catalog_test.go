package generator

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/permcatalog"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// TestABuildsCatalogListsTheCoresWritePermissions holds a build's permission
// catalog to the permissions the core checks on an operation's writes
// (D104): the generator gives apigen behaviorops.WritePermissions over the
// API and its dependencies, so fixture-approvals-api's permissions.json
// lists tasks.admin, the permission of Task's Constants, under
// reassignTask, which updates a task through TaskAssignment, a @source
// view of it. The route requires tasks.admin of its own, which would list
// it anyway, so the requirement is taken off the loaded IR first: what
// lists it then is the core's permission alone.
func TestABuildsCatalogListsTheCoresWritePermissions(t *testing.T) {
	schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, "fixture-approvals-api"))
	if err != nil {
		t.Fatalf("load fixture-approvals-api: %v", err)
	}
	var reassign *ir.FieldDef
	for _, set := range schema.OperationSets {
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

	outputRoot := t.TempDir()
	if _, err := Run(schema, cfg, Options{
		OutputRoot: outputRoot,
		Paths:      testpaths.Local(t),
		Naming:     naming.Default(),
		Clock:      codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
		LoadDependency: func(name string) (*ir.Schema, error) {
			return loader.LoadService(filepath.Join(tsFixtures, name))
		},
	}); err != nil {
		t.Fatalf("run fixture-approvals-api: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(APIDir(outputRoot, "fixture-approvals-api"), "permissions.json"))
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := permcatalog.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	var operations []string
	for _, p := range catalog.Permissions {
		if p.Name == "tasks.admin" {
			operations = p.Operations
		}
	}
	if want := []string{"TaskAdminReassignTaskHandler"}; !slices.Equal(operations, want) {
		t.Fatalf("with no requirement of reassignTask's own, tasks.admin is needed by %v, want %v: the build lists Constants' permission under an update of its table\n%s", operations, want, data)
	}
}
