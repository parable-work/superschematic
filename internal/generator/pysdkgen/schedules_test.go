package pysdkgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/verify"
	ir "github.com/parable-work/superschematic/ir"
)

// schedulesService is the API that serves fixture-schedules-db's Problem's
// behavior operations, those of the Schedules it implements among them,
// and runs its behavior worker (D104).
const schedulesService = "fixture-schedules-api"

// TestSchedulesSDKGolden pins every file of the Python SDK for
// fixture-schedules-api: the operations of the Schedules Problem implements
// are methods of a class of their own, the property of the namespace named
// for its member, sdk.problem.schedules.set_schedule, whose results name a
// ProblemSchedulesKind and a ProblemSchedulesState. The event run's types
// reach no endpoint, so the SDK names none of them. Regenerate with:
// go test ./internal/generator/pysdkgen -run TestSchedulesSDKGolden -update
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
		Dependencies: map[string]*ir.Schema{"fixture-schedules-db": db},
		Clock:        nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	outDir := t.TempDir()
	sdkOutput, err := Generate(apiOutput, "", "", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDK(sdkOutput, outDir); err != nil {
		t.Fatalf("WriteSDK: %v", err)
	}
	compareGoldenTree(t, outDir, filepath.Join("testdata", "golden", schedulesService))

	var got []string
	for _, ns := range sdkOutput.Namespaces {
		for _, member := range ns.Members {
			for _, ep := range member.Endpoints {
				got = append(got, ns.AccessorName+"."+member.FieldName+" "+member.ClassName+"."+ep.MethodName)
			}
		}
	}
	want := []string{
		"problem.schedules ProblemSchedulesOperations.set_schedule",
		"problem.schedules ProblemSchedulesOperations.pause_schedule",
		"problem.schedules ProblemSchedulesOperations.resume_schedule",
		"problem.schedules ProblemSchedulesOperations.cancel_schedule",
		"problem.schedules ProblemSchedulesOperations.report_run",
		"problem.schedules ProblemSchedulesOperations.list_schedules",
		"problem.status ProblemStatusOperations.transition",
	}
	for _, each := range want {
		if !strings.Contains(strings.Join(got, "; "), each) {
			t.Errorf("members = %v, want %s among them", got, each)
		}
	}
	if len(got) != len(want) {
		t.Errorf("members = %d (%v), want %d", len(got), got, len(want))
	}
	err = filepath.WalkDir(outDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, name := range []string{"ProblemSchedulesRunEvent", "ProblemSchedulesRunPayload"} {
			if strings.Contains(string(data), name) {
				t.Errorf("%s names %s, which no endpoint references", strings.TrimPrefix(path, outDir), name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
