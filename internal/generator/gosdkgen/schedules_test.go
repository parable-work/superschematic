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

// schedulesService is the API that serves fixture-schedules-db's Problem's
// behavior operations, those of the Schedules it implements among them,
// and runs its behavior worker (D104).
const schedulesService = "fixture-schedules-api"

// loadSchedulesAPI loads fixture-schedules-api and its database, and
// generates its API output.
func loadSchedulesAPI(t *testing.T) (*ir.Schema, *ir.Schema, *apigen.APIOutput) {
	t.Helper()
	load := func(name string) *ir.Schema {
		schema, err := loader.LoadService(filepath.Join(fixturesDir, name), loader.WithWarnings(func(*verify.Diagnostic) {}))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		return schema
	}
	schema, db := load(schedulesService), load("fixture-schedules-db")
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:     sessionauth.Provider{},
		SchemaName:   schedulesService,
		ModulePath:   "example.com/schemas/api/" + schedulesService,
		TypesModule:  "example.com/schemas/types/go/" + schedulesService,
		Dependencies: map[string]*ir.Schema{"fixture-schedules-db": db},
		Clock:        nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return schema, db, apiOutput
}

// TestSchedulesSDKGolden pins every file of the Go SDK for
// fixture-schedules-api: the operations of the Schedules Problem implements
// are methods of a struct of their own, the field of the namespace named
// for its member (ProblemNamespace.Schedules.SetSchedule), beside its
// status's. The event run's types reach no endpoint, so the SDK names none
// of them. Regenerate with:
// go test ./internal/generator/gosdkgen -run TestSchedulesSDKGolden -update
func TestSchedulesSDKGolden(t *testing.T) {
	_, _, apiOutput := loadSchedulesAPI(t)
	root := t.TempDir()
	sdkDir := filepath.Join(root, "sdk", "go", schedulesService)
	typesDir := filepath.Join(root, "types", "go", schedulesService)
	if err := os.MkdirAll(typesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sdkOutput, err := Generate(apiOutput, "example.com/schemas/sdk/go/"+schedulesService, "sdk", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, typesDir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	compareGoldenTree(t, sdkDir, filepath.Join("testdata", "golden", schedulesService))

	var got []string
	for _, ns := range sdkOutput.Namespaces {
		for _, member := range ns.Members {
			for _, ep := range member.Endpoints {
				got = append(got, ns.StructName+"."+member.FieldName+" *"+member.StructName+"."+ep.MethodName)
			}
		}
	}
	want := []string{
		"ProblemNamespace.Schedules *ProblemSchedulesOperations.SetSchedule",
		"ProblemNamespace.Schedules *ProblemSchedulesOperations.PauseSchedule",
		"ProblemNamespace.Schedules *ProblemSchedulesOperations.ResumeSchedule",
		"ProblemNamespace.Schedules *ProblemSchedulesOperations.CancelSchedule",
		"ProblemNamespace.Schedules *ProblemSchedulesOperations.ReportRun",
		"ProblemNamespace.Schedules *ProblemSchedulesOperations.ListSchedules",
		"ProblemNamespace.Status *ProblemStatusOperations.Transition",
	}
	for _, each := range want {
		if !strings.Contains(strings.Join(got, "; "), each) {
			t.Errorf("members = %v, want %s among them", got, each)
		}
	}
	if len(got) != len(want) {
		t.Errorf("members = %d (%v), want %d", len(got), got, len(want))
	}
	err = filepath.WalkDir(sdkDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, name := range []string{"ProblemSchedulesRunEvent", "ProblemSchedulesRunPayload"} {
			if strings.Contains(string(data), name) {
				t.Errorf("%s names %s, which no endpoint references", strings.TrimPrefix(path, sdkDir), name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestSchedulesSDKBuilds builds and vets the Go SDK of fixture-schedules-api
// over the types modules of the API and its database, whose API types hold
// the event run's and its payload's, which a Workers method takes.
func TestSchedulesSDKBuilds(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	schema, db, apiOutput := loadSchedulesAPI(t)
	paths := testpaths.Local(t)
	root := t.TempDir()
	for _, tc := range []struct {
		name   string
		schema *ir.Schema
		deps   map[string]*ir.Schema
	}{
		{"fixture-schedules-db", db, nil},
		{schedulesService, schema, map[string]*ir.Schema{"fixture-schedules-db": db}},
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
		if tc.name == schedulesService {
			types, err := os.ReadFile(filepath.Join(dir, "types.go"))
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"type ProblemSchedulesRunEvent struct", "type ProblemSchedulesRunPayload struct"} {
				if !strings.Contains(string(types), want) {
					t.Errorf("the API's types lack %q", want)
				}
			}
		}
	}
	sdkDir := filepath.Join(root, "sdk", "go", schedulesService)
	sdkOutput, err := Generate(apiOutput, "example.com/schemas/sdk/go/"+schedulesService, "sdk", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, filepath.Join(root, "types", "go", schedulesService), nestedArraysClock); err != nil {
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
