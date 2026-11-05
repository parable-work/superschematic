package pysdkgen

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/verify"
	ir "github.com/parable-work/superschematic/ir"
)

// seatsService is the API that serves fixture-seats-db's Problem's and
// Sandbox's behavior operations, those of the Seats Problem implements and
// of the Reconcile on Sandbox's observed field among them (D104).
const seatsService = "fixture-seats-api"

// TestSeatsSDKGolden pins every file of the Python SDK for
// fixture-seats-api: the operations of the Seats Problem implements are
// methods of a class of their own, the property of the namespace named for
// its member, sdk.problem.seats.take_seat, and those of the Reconcile on
// Sandbox's observed field the field's, sdk.sandbox.observed.observe, whose
// inputs name a ProblemSeatsSeat and a SandboxObservedState. Regenerate
// with:
// go test ./internal/generator/pysdkgen -run TestSeatsSDKGolden -update
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
		Dependencies: map[string]*ir.Schema{"fixture-seats-db": db},
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
	compareGoldenTree(t, outDir, filepath.Join("testdata", "golden", seatsService))

	var got []string
	for _, ns := range sdkOutput.Namespaces {
		for _, member := range ns.Members {
			for _, ep := range member.Endpoints {
				got = append(got, ns.AccessorName+"."+member.FieldName+" "+member.ClassName+"."+ep.MethodName)
			}
		}
	}
	for _, want := range []string{
		"problem.seats ProblemSeatsOperations.take_seat",
		"problem.seats ProblemSeatsOperations.leave_seat",
		"problem.seats ProblemSeatsOperations.list_seats",
		"problem.status ProblemStatusOperations.transition",
		"sandbox.status SandboxStatusOperations.transition",
		"sandbox.observed SandboxObservedOperations.observe",
	} {
		if !strings.Contains(strings.Join(got, "; "), want) {
			t.Errorf("members = %v, want %s among them", got, want)
		}
	}
	if len(got) != 6 {
		t.Errorf("members = %d (%v), want 6", len(got), got)
	}
}
