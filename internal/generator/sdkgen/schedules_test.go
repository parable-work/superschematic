package sdkgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// schedulesAPI is the API that serves fixture-schedules-db's Problem's
// behavior operations, those of the Schedules it implements among them, and
// runs its behavior worker (D104).
const schedulesAPI = "fixture-schedules-api"

// schedulesAPIGolden is fixture-schedules-api's SDK, whose types package
// depends on fixture-schedules-db's.
func schedulesAPIGolden(t *testing.T) goldenSDK {
	schema, db, apiOutput, parseable := loadBehaviorAPI(t, schedulesAPI, "fixture-schedules-db")
	return goldenSDK{
		apiOutput: apiOutput,
		parseable: parseable,
		clock:     nestedArraysClock,
		types: []goldenTypes{
			{name: schedulesAPI, schema: schema, deps: map[string]*ir.Schema{"fixture-schedules-db": db}},
			{name: "fixture-schedules-db", schema: db},
		},
	}
}

// TestSchedulesSDKGolden pins every file of the TypeScript SDK for
// fixture-schedules-api, its tool documents included: the operations of the
// Schedules Problem implements sit in a class of their own under its
// member, sdk.problem.schedules.setSchedule(id, { name, every }), beside its
// status; their tools are problem.schedules.setSchedule and its siblings.
// The event run's types reach no endpoint, so neither the SDK nor a tool
// names them. TestEveryGoldenSDKTypeChecks type-checks it. Regenerate with:
// go test ./internal/generator/sdkgen -run TestSchedulesSDKGolden -update
func TestSchedulesSDKGolden(t *testing.T) {
	_, _, apiOutput, parseable := loadBehaviorAPI(t, schedulesAPI, "fixture-schedules-db")
	outDir := t.TempDir()
	sdkOutput, err := Generate(apiOutput, parseable, nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, outDir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	compareGoldenTree(t, outDir, filepath.Join("testdata", "golden", schedulesAPI))

	var members []string
	for _, ns := range sdkOutput.Namespaces {
		for _, member := range ns.Members {
			for _, ep := range member.Endpoints {
				members = append(members, ns.Name+" "+member.ClassName+"."+ep.MethodPath()+" "+ep.ToolName)
			}
		}
	}
	want := []string{
		"problem ProblemSchedulesOperations.schedules.setSchedule problem.schedules.setSchedule",
		"problem ProblemSchedulesOperations.schedules.pauseSchedule problem.schedules.pauseSchedule",
		"problem ProblemSchedulesOperations.schedules.resumeSchedule problem.schedules.resumeSchedule",
		"problem ProblemSchedulesOperations.schedules.cancelSchedule problem.schedules.cancelSchedule",
		"problem ProblemSchedulesOperations.schedules.reportRun problem.schedules.reportRun",
		"problem ProblemSchedulesOperations.schedules.listSchedules problem.schedules.listSchedules",
		"problem ProblemStatusOperations.status.transition problem.status.transition",
	}
	for _, each := range want {
		if !strings.Contains(strings.Join(members, "; "), each) {
			t.Errorf("members = %v, want %s among them", members, each)
		}
	}
	if len(members) != len(want) {
		t.Errorf("members = %d (%v), want %d", len(members), members, len(want))
	}
	source, err := os.ReadFile(filepath.Join(outDir, "namespaces", "problem.ts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, each := range []string{"public readonly schedules: ProblemSchedulesOperations;", "export class ProblemSchedulesOperations {", "public async setSchedule(", "`/api/problem/${encodeURIComponent(id)}/schedules/set-schedule`"} {
		if !strings.Contains(string(source), each) {
			t.Errorf("namespaces/problem.ts lacks %q", each)
		}
	}
	err = filepath.WalkDir(outDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, name := range []string{"ProblemSchedulesRunEvent", "ProblemSchedulesRunPayload", "ProblemWakeUps"} {
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
