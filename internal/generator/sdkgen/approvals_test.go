package sdkgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// approvalsAPI is the API that serves fixture-approvals-db's Task's
// behavior operations, Approvals' under approvals among them (D104);
// Constants and StateRules, which Task implements too, have none.
const approvalsAPI = "fixture-approvals-api"

// approvalsAPIGolden is fixture-approvals-api's SDK, whose types package
// depends on fixture-approvals-db's.
func approvalsAPIGolden(t *testing.T) goldenSDK {
	schema, db, apiOutput, parseable := loadBehaviorAPI(t, approvalsAPI, "fixture-approvals-db")
	return goldenSDK{
		apiOutput: apiOutput,
		parseable: parseable,
		clock:     nestedArraysClock,
		types: []goldenTypes{
			{name: approvalsAPI, schema: schema, deps: map[string]*ir.Schema{"fixture-approvals-db": db}},
			{name: "fixture-approvals-db", schema: db},
		},
	}
}

// TestApprovalsSDKGolden pins every file of the TypeScript SDK for
// fixture-approvals-api, its tool documents included: the operations of the
// Approvals Task implements sit in a class of their own under the use's
// member, sdk.task.approvals.signOff(id, { state, version }), whose state
// is the enum TaskApprovalsGate, and its tool is task.approvals.signOff.
// TestEveryGoldenSDKTypeChecks type-checks it. Regenerate with:
// go test ./internal/generator/sdkgen -run TestApprovalsSDKGolden -update
func TestApprovalsSDKGolden(t *testing.T) {
	_, _, apiOutput, parseable := loadBehaviorAPI(t, approvalsAPI, "fixture-approvals-db")
	outDir := t.TempDir()
	sdkOutput, err := Generate(apiOutput, parseable, nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, outDir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	compareGoldenTree(t, outDir, filepath.Join("testdata", "golden", approvalsAPI))

	var members []string
	for _, ns := range sdkOutput.Namespaces {
		for _, member := range ns.Members {
			for _, ep := range member.Endpoints {
				members = append(members, ns.Name+" "+member.ClassName+"."+ep.MethodPath()+" "+ep.ToolName)
			}
		}
	}
	want := []string{
		"task TaskApprovalsOperations.approvals.listSignOffs task.approvals.listSignOffs",
		"task TaskApprovalsOperations.approvals.signOff task.approvals.signOff",
		"task TaskApprovalsOperations.approvals.withdrawSignOff task.approvals.withdrawSignOff",
		"task TaskStatusOperations.status.transition task.status.transition",
	}
	if strings.Join(members, "; ") != strings.Join(want, "; ") {
		t.Errorf("members = %v, want %v", members, want)
	}
	source, err := os.ReadFile(filepath.Join(outDir, "namespaces", "task.ts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"public readonly approvals: TaskApprovalsOperations;",
		"export class TaskApprovalsOperations {",
		"public async signOff(",
		"`/api/task/${encodeURIComponent(id)}/approvals/sign-off`",
	} {
		if !strings.Contains(string(source), want) {
			t.Errorf("namespaces/task.ts lacks %q", want)
		}
	}
}
