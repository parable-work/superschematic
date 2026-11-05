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
	"github.com/parable-work/superschematic/internal/loader/verify"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// seatsService is the API that serves fixture-seats-db's Problem's and
// Sandbox's behavior operations, those of the Seats Problem implements and
// of the Reconcile on Sandbox's observed field among them (D104).
const seatsService = "fixture-seats-api"

// loadSeatsAPI loads fixture-seats-api and its database, and generates its
// API output.
func loadSeatsAPI(t *testing.T) (*ir.Schema, *ir.Schema, *apigen.APIOutput) {
	t.Helper()
	load := func(name string) *ir.Schema {
		schema, err := loader.LoadService(filepath.Join(fixturesDir, name), loader.WithWarnings(func(*verify.Diagnostic) {}))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		return schema
	}
	schema, db := load(seatsService), load("fixture-seats-db")
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:     sessionauth.Provider{},
		SchemaName:   seatsService,
		ModulePath:   "example.com/schemas/api/" + seatsService,
		TypesModule:  "example.com/schemas/types/go/" + seatsService,
		Dependencies: map[string]*ir.Schema{"fixture-seats-db": db},
		Clock:        nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return schema, db, apiOutput
}

// TestSeatsSDKGolden pins every file of the Go SDK for fixture-seats-api:
// the operations of the Seats Problem implements are methods of a struct
// of their own, the field of the namespace named for its member
// (ProblemNamespace.Seats.TakeSeat), and those of the Reconcile on
// Sandbox's observed field the field's (SandboxNamespace.Observed.Observe),
// each beside its table's status's. Regenerate with:
// go test ./internal/generator/gosdkgen -run TestSeatsSDKGolden -update
func TestSeatsSDKGolden(t *testing.T) {
	_, _, apiOutput := loadSeatsAPI(t)
	root := t.TempDir()
	sdkDir := filepath.Join(root, "sdk", "go", seatsService)
	typesDir := filepath.Join(root, "types", "go", seatsService)
	if err := os.MkdirAll(typesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sdkOutput, err := Generate(apiOutput, "example.com/schemas/sdk/go/"+seatsService, "sdk", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, typesDir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	compareGoldenTree(t, sdkDir, filepath.Join("testdata", "golden", seatsService))

	var got []string
	for _, ns := range sdkOutput.Namespaces {
		for _, member := range ns.Members {
			for _, ep := range member.Endpoints {
				got = append(got, ns.StructName+"."+member.FieldName+" *"+member.StructName+"."+ep.MethodName)
			}
		}
	}
	for _, want := range []string{
		"ProblemNamespace.Seats *ProblemSeatsOperations.TakeSeat",
		"ProblemNamespace.Seats *ProblemSeatsOperations.LeaveSeat",
		"ProblemNamespace.Seats *ProblemSeatsOperations.ListSeats",
		"ProblemNamespace.Status *ProblemStatusOperations.Transition",
		"SandboxNamespace.Status *SandboxStatusOperations.Transition",
		"SandboxNamespace.Observed *SandboxObservedOperations.Observe",
	} {
		if !strings.Contains(strings.Join(got, "; "), want) {
			t.Errorf("members = %v, want %s among them", got, want)
		}
	}
	if len(got) != 6 {
		t.Errorf("members = %d (%v), want 6", len(got), got)
	}
}

// TestSeatsSDKBuilds builds and vets the Go SDK of fixture-seats-api over
// the types modules of the API and its database: listSeats' nullable
// seatedAt and seatedBy and observe's nullable status and optional drift
// among them.
func TestSeatsSDKBuilds(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	schema, db, apiOutput := loadSeatsAPI(t)
	paths := testpaths.Local(t)
	root := t.TempDir()
	for _, tc := range []struct {
		name   string
		schema *ir.Schema
		deps   map[string]*ir.Schema
	}{
		{"fixture-seats-db", db, nil},
		{seatsService, schema, map[string]*ir.Schema{"fixture-seats-db": db}},
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
	sdkDir := filepath.Join(root, "sdk", "go", seatsService)
	sdkOutput, err := Generate(apiOutput, "example.com/schemas/sdk/go/"+seatsService, "sdk", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, filepath.Join(root, "types", "go", seatsService), nestedArraysClock); err != nil {
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
