package typegen

import (
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestGeneratedEnumValues runs the generated Values method: it returns every
// member in declaration order, not sorted, each member is valid, and a caller
// that edits the result cannot change the next call's.
func TestGeneratedEnumValues(t *testing.T) {
	schema := ir.NewSchema("enum-values", ir.SchemaKindGeneral)
	schema.Enums["Stage"] = &ir.EnumDef{Name: "Stage", Values: []ir.EnumValueDef{
		{Name: "Draft", SerializedAs: "draft"},
		{Name: "Review", SerializedAs: "in_review"},
		{Name: "Done", SerializedAs: "done"},
	}}
	output, err := Generate(schema, Options{
		SchemaName: schema.Name,
		ModulePath: "example.com/schemas/types/go/enum-values",
	})
	if err != nil {
		t.Fatal(err)
	}
	const code = `package types

import (
	"reflect"
	"testing"
)

func TestStageValues(t *testing.T) {
	got := Stage("").Values()
	if want := []Stage{"draft", "in_review", "done"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Values() = %v, want %v", got, want)
	}
	for _, member := range got {
		if !member.IsValid() {
			t.Errorf("Values() returned invalid member %q", member)
		}
	}
	got[0] = "edited"
	if again := Stage("").Values(); again[0] != Stage_Draft {
		t.Fatalf("Values() shares its slice between calls: %v", again)
	}
}
`
	runGeneratedModuleTest(t, output, "enum_values", code)
}
