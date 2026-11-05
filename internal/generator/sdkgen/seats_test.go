package sdkgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// seatsAPI is the API that serves fixture-seats-db's Problem's and
// Sandbox's behavior operations, those of the Seats Problem implements and
// of the Reconcile on Sandbox's observed field among them (D104).
const seatsAPI = "fixture-seats-api"

// seatsAPIGolden is fixture-seats-api's SDK, whose types package depends
// on fixture-seats-db's.
func seatsAPIGolden(t *testing.T) goldenSDK {
	schema, db, apiOutput, parseable := loadBehaviorAPI(t, seatsAPI, "fixture-seats-db")
	return goldenSDK{
		apiOutput: apiOutput,
		parseable: parseable,
		clock:     nestedArraysClock,
		types: []goldenTypes{
			{name: seatsAPI, schema: schema, deps: map[string]*ir.Schema{"fixture-seats-db": db}},
			{name: "fixture-seats-db", schema: db},
		},
	}
}

// TestSeatsSDKGolden pins every file of the TypeScript SDK for
// fixture-seats-api, its tool documents included: the operations of the
// Seats Problem implements sit in a class of their own under its member,
// sdk.problem.seats.takeSeat(id, { seat }), and those of the Reconcile on
// Sandbox's observed field under the field's, sdk.sandbox.observed.observe(id,
// { state, at }), each beside its table's status; their tools are
// problem.seats.takeSeat and sandbox.observed.observe.
// TestEveryGoldenSDKTypeChecks type-checks it. Regenerate with:
// go test ./internal/generator/sdkgen -run TestSeatsSDKGolden -update
func TestSeatsSDKGolden(t *testing.T) {
	_, _, apiOutput, parseable := loadBehaviorAPI(t, seatsAPI, "fixture-seats-db")
	outDir := t.TempDir()
	sdkOutput, err := Generate(apiOutput, parseable, nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, outDir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	compareGoldenTree(t, outDir, filepath.Join("testdata", "golden", seatsAPI))

	var members []string
	for _, ns := range sdkOutput.Namespaces {
		for _, member := range ns.Members {
			for _, ep := range member.Endpoints {
				members = append(members, ns.Name+" "+member.ClassName+"."+ep.MethodPath()+" "+ep.ToolName)
			}
		}
	}
	for _, want := range []string{
		"problem ProblemSeatsOperations.seats.takeSeat problem.seats.takeSeat",
		"problem ProblemSeatsOperations.seats.leaveSeat problem.seats.leaveSeat",
		"problem ProblemSeatsOperations.seats.listSeats problem.seats.listSeats",
		"problem ProblemStatusOperations.status.transition problem.status.transition",
		"sandbox SandboxStatusOperations.status.transition sandbox.status.transition",
		"sandbox SandboxObservedOperations.observed.observe sandbox.observed.observe",
	} {
		if !strings.Contains(strings.Join(members, "; "), want) {
			t.Errorf("members = %v, want %s among them", members, want)
		}
	}
	if len(members) != 6 {
		t.Errorf("members = %d (%v), want 6", len(members), members)
	}
	for file, wants := range map[string][]string{
		"problem.ts": {"public readonly seats: ProblemSeatsOperations;", "export class ProblemSeatsOperations {", "public async takeSeat(", "`/api/problem/${encodeURIComponent(id)}/seats/take-seat`"},
		"sandbox.ts": {"public readonly observed: SandboxObservedOperations;", "export class SandboxObservedOperations {", "public async observe(", "`/api/sandbox/${encodeURIComponent(id)}/observed/observe`"},
	} {
		source, err := os.ReadFile(filepath.Join(outDir, "namespaces", file))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range wants {
			if !strings.Contains(string(source), want) {
				t.Errorf("namespaces/%s lacks %q", file, want)
			}
		}
	}
}
