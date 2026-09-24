package apigen_test

import (
	"reflect"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
)

// TestToolTypeFieldsCarryShapeAndBounds: TypeFields holds every type a tool
// schema expands, with each field's map shape and Validate<> bounds, and
// every schema scalar carries its canonical name.
func TestToolTypeFieldsCarryShapeAndBounds(t *testing.T) {
	output, err := generateMCP(loadMCPFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(output.ToolKeys, apigen.DefaultToolKeys()) {
		t.Fatalf("ToolKeys = %+v", output.ToolKeys)
	}
	address, ok := output.TypeFields["Address"]
	if !ok || len(address) != 3 || address[2].Name != "postalCode" || address[2].Required {
		t.Fatalf("TypeFields[Address] = %+v", address)
	}
	for _, field := range output.TypeFields["ReturnRequest"] {
		if field.Name == "labels" && !field.IsMap {
			t.Fatalf("ReturnRequest.labels is not a map: %+v", field)
		}
		if field.Name == "reason" && (field.ValidateMinLength == nil || *field.ValidateMinLength != 1) {
			t.Fatalf("ReturnRequest.reason lost its bounds: %+v", field)
		}
	}
	if output.Scalars["Identity.UUID"].CanonicalName != "Identity.UUID" || output.Scalars["string"].CanonicalName != "" {
		t.Fatalf("scalar canonical names = %q, %q", output.Scalars["Identity.UUID"].CanonicalName, output.Scalars["string"].CanonicalName)
	}
}
