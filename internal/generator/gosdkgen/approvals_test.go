package gosdkgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/typegen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// approvalsService is the API that serves fixture-approvals-db's Task's
// behavior operations, those of the Approvals it implements among them
// (D104); Constants and StateRules, which it implements too, have none.
const approvalsService = "fixture-approvals-api"

// loadApprovalsAPI loads fixture-approvals-api and its database, and
// generates its API output.
func loadApprovalsAPI(t *testing.T) (*ir.Schema, *ir.Schema, *apigen.APIOutput) {
	t.Helper()
	load := func(name string) *ir.Schema {
		schema, err := loader.LoadService(filepath.Join(fixturesDir, name))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		return schema
	}
	schema, db := load(approvalsService), load("fixture-approvals-db")
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:     sessionauth.Provider{},
		SchemaName:   approvalsService,
		ModulePath:   "example.com/schemas/api/" + approvalsService,
		TypesModule:  "example.com/schemas/types/go/" + approvalsService,
		Dependencies: map[string]*ir.Schema{"fixture-approvals-db": db},
		Clock:        nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return schema, db, apiOutput
}

// TestApprovalsSDKGolden pins every file of the Go SDK for
// fixture-approvals-api: the operations of the Approvals Task implements are
// methods of a struct of their own, the field Approvals of the namespace,
// TaskNamespace.Approvals.SignOff, beside its status's; signOff's input
// names the gated state (TaskApprovalsGate) and the version its signer
// read. Regenerate with:
// go test ./internal/generator/gosdkgen -run TestApprovalsSDKGolden -update
func TestApprovalsSDKGolden(t *testing.T) {
	_, _, apiOutput := loadApprovalsAPI(t)
	root := t.TempDir()
	sdkDir := filepath.Join(root, "sdk", "go", approvalsService)
	typesDir := filepath.Join(root, "types", "go", approvalsService)
	if err := os.MkdirAll(typesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sdkOutput, err := Generate(apiOutput, "example.com/schemas/sdk/go/"+approvalsService, "sdk", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, typesDir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	compareGoldenTree(t, sdkDir, filepath.Join("testdata", "golden", approvalsService))

	var got []string
	for _, ns := range sdkOutput.Namespaces {
		for _, member := range ns.Members {
			for _, ep := range member.Endpoints {
				got = append(got, ns.StructName+"."+member.FieldName+" *"+member.StructName+"."+ep.MethodName)
			}
		}
	}
	want := "TaskNamespace.Approvals *TaskApprovalsOperations.ListSignOffs; TaskNamespace.Approvals *TaskApprovalsOperations.SignOff; " +
		"TaskNamespace.Approvals *TaskApprovalsOperations.WithdrawSignOff; TaskNamespace.Status *TaskStatusOperations.Transition"
	if strings.Join(got, "; ") != want {
		t.Errorf("members = %v, want %s", got, want)
	}
}

// TestApprovalsSDKBuilds builds and vets the Go SDK of fixture-approvals-api
// over the types modules of the API and its database, the sub-table's types
// among the database's.
func TestApprovalsSDKBuilds(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	schema, db, apiOutput := loadApprovalsAPI(t)
	paths := testpaths.Local(t)
	root := t.TempDir()
	for _, tc := range []struct {
		name   string
		schema *ir.Schema
		deps   map[string]*ir.Schema
	}{
		{"fixture-approvals-db", db, nil},
		{approvalsService, schema, map[string]*ir.Schema{"fixture-approvals-db": db}},
	} {
		modules := map[string]string{}
		for dep := range tc.deps {
			modules[dep] = "example.com/schemas/types/go/" + dep
		}
		output, err := typegen.Generate(tc.schema, typegen.Options{
			SchemaName:        tc.name,
			ModulePath:        "example.com/schemas/types/go/" + tc.name,
			Dependencies:      tc.deps,
			DependencyModules: modules,
			Registry:          registry.New(naming.Default()),
			Clock:             nestedArraysClock,
		})
		if err != nil {
			t.Fatalf("typegen.Generate %s: %v", tc.name, err)
		}
		dir := filepath.Join(root, "types", "go", tc.name)
		if err := typegen.SetReplacePaths(output, paths, dir); err != nil {
			t.Fatal(err)
		}
		if err := typegen.WriteTypes(output, dir); err != nil {
			t.Fatal(err)
		}
	}
	sdkDir := filepath.Join(root, "sdk", "go", approvalsService)
	sdkOutput, err := Generate(apiOutput, "example.com/schemas/sdk/go/"+approvalsService, "sdk", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, filepath.Join(root, "types", "go", approvalsService), nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	for _, args := range [][]string{{"mod", "tidy"}, {"build", "./..."}, {"vet", "./..."}} {
		cmd := exec.Command("go", args...)
		cmd.Dir = sdkDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go %s in the generated SDK: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
}
