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

// seatsService is the API that serves fixture-seats-db's Problem's and
// Sandbox's behavior operations, those of the Seats Problem implements and
// of the Reconcile on Sandbox's observed field among them (D104).
const seatsService = "fixture-seats-api"

// TestSeatsSDKGolden pins every file of the Rust SDK for fixture-seats-api,
// its tool documents included: the operations of the Seats Problem
// implements are methods of a struct of their own, the field of the
// namespace named for its member, problem.seats.take_seat, and those of the
// Reconcile on Sandbox's observed field the field's, sandbox.observed.observe,
// whose tools are named problem.seats.takeSeat with the method
// seats.take_seat. Regenerate with:
// go test ./internal/generator/rustsdkgen -run TestSeatsSDKGolden -update
func TestSeatsSDKGolden(t *testing.T) {
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
	root := t.TempDir()
	sdkDir := filepath.Join(root, "sdk", "rust", seatsService)
	sdkOutput, err := Generate(apiOutput, naming.Default().RustSDKCrate(seatsService), naming.Default().RustTypesCrate(seatsService), nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, filepath.Join(root, "types", "rust", seatsService), nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	compareGoldenTree(t, sdkDir, filepath.Join("testdata", "golden", seatsService))

	var got []string
	for _, ns := range sdkOutput.Namespaces {
		for _, member := range ns.Members {
			for _, ep := range member.Endpoints {
				got = append(got, ns.FieldName+"."+member.FieldName+" "+member.StructName+"::"+ep.MethodName)
			}
		}
	}
	for _, want := range []string{
		"problem.seats ProblemSeatsOperations::take_seat",
		"problem.seats ProblemSeatsOperations::leave_seat",
		"problem.seats ProblemSeatsOperations::list_seats",
		"problem.status ProblemStatusOperations::transition",
		"sandbox.status SandboxStatusOperations::transition",
		"sandbox.observed SandboxObservedOperations::observe",
	} {
		if !strings.Contains(strings.Join(got, "; "), want) {
			t.Errorf("members = %v, want %s among them", got, want)
		}
	}
	if len(got) != 6 {
		t.Errorf("members = %d (%v), want 6", len(got), got)
	}
	tools, err := GenerateTools(sdkOutput, apiOutput, nestedArraysClock)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name+" "+tool.MethodName)
	}
	for _, want := range []string{"problem.seats.takeSeat seats.take_seat", "problem.seats.listSeats seats.list_seats", "sandbox.observed.observe observed.observe"} {
		if !strings.Contains(strings.Join(names, "; "), want) {
			t.Errorf("tools = %v, want %s among them", names, want)
		}
	}
}
