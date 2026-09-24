package pysdkgen

import (
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/codegen"
)

// TestNestedArraysGuard: pysdkgen refuses arrays of arrays (T[][]) until it
// renders them, rather than emitting T[]. The change that teaches pysdkgen
// T[][] deletes its nested-arrays guard and replaces this test with output
// for the fixture-nested-arrays services.
// apigen is guarded too, so the API output here is built by hand: one
// endpoint whose response is a list of lists.
func TestNestedArraysGuard(t *testing.T) {
	out := &apigen.APIOutput{
		SchemaName: "fixture-nested-arrays-api",
		Endpoints: []apigen.EndpointInfo{{
			Namespace: "grid", Name: "gridLabels", OutputType: "string", OutputIsArray: true, OutputIsArrayOfArrays: true,
		}},
	}
	_, err := Generate(out, "", "", codegen.DefaultClock())
	if err == nil || err.Error() != "pysdkgen does not support arrays of arrays yet (grid.gridLabels)" {
		t.Fatalf("err = %v", err)
	}
}
