package rustsdkgen

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/verify"
	ir "github.com/parable-work/superschematic/ir"
)

// schedulesService is the API that serves fixture-schedules-db's Problem's
// behavior operations, those of the Schedules it implements among them,
// and runs its behavior worker (D104).
const schedulesService = "fixture-schedules-api"

// TestSchedulesSDKGolden pins every file of the Rust SDK for
// fixture-schedules-api, its tool documents included: the operations of
// the Schedules Problem implements are methods of a struct of their own,
// the field of the namespace named for its member,
// problem.schedules.set_schedule, whose tools are named
// problem.schedules.setSchedule with the method schedules.set_schedule. The
// event run's types reach no endpoint, so no tool names them. Regenerate
// with:
// go test ./internal/generator/rustsdkgen -run TestSchedulesSDKGolden -update
func TestSchedulesSDKGolden(t *testing.T) {
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
	root := t.TempDir()
	sdkDir := filepath.Join(root, "sdk", "rust", schedulesService)
	sdkOutput, err := Generate(apiOutput, naming.Default().RustSDKCrate(schedulesService), naming.Default().RustTypesCrate(schedulesService), nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, filepath.Join(root, "types", "rust", schedulesService), nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	compareGoldenTree(t, sdkDir, filepath.Join("testdata", "golden", schedulesService))

	var got []string
	for _, ns := range sdkOutput.Namespaces {
		for _, member := range ns.Members {
			for _, ep := range member.Endpoints {
				got = append(got, ns.FieldName+"."+member.FieldName+" "+member.StructName+"::"+ep.MethodName)
			}
		}
	}
	want := []string{
		"problem.schedules ProblemSchedulesOperations::set_schedule",
		"problem.schedules ProblemSchedulesOperations::pause_schedule",
		"problem.schedules ProblemSchedulesOperations::resume_schedule",
		"problem.schedules ProblemSchedulesOperations::cancel_schedule",
		"problem.schedules ProblemSchedulesOperations::report_run",
		"problem.schedules ProblemSchedulesOperations::list_schedules",
		"problem.status ProblemStatusOperations::transition",
	}
	for _, each := range want {
		if !strings.Contains(strings.Join(got, "; "), each) {
			t.Errorf("members = %v, want %s among them", got, each)
		}
	}
	if len(got) != len(want) {
		t.Errorf("members = %d (%v), want %d", len(got), got, len(want))
	}
	tools, err := GenerateTools(sdkOutput, apiOutput, nestedArraysClock)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name+" "+tool.MethodName)
	}
	for _, each := range []string{"problem.schedules.setSchedule schedules.set_schedule", "problem.schedules.reportRun schedules.report_run", "problem.schedules.listSchedules schedules.list_schedules"} {
		if !strings.Contains(strings.Join(names, "; "), each) {
			t.Errorf("tools = %v, want %s among them", names, each)
		}
	}
	if strings.Contains(strings.Join(names, "; "), "RunEvent") || len(names) != len(want) {
		t.Errorf("tools = %v, want one per operation", names)
	}
}
