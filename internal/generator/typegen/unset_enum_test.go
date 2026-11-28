package typegen

import (
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestRequiredEnumLeavesOutItsUnsetValue runs the generated code (D202). A
// required enum that was never set holds the Go zero value "", which no
// reader takes for a member. The encoder leaves the key out instead of
// writing "", so a value Go encodes decodes again: an object nested in
// another one with its enums unset, as a related row read by id alone is,
// survives the trip. An absent key reads as the field's @default, so an
// unset enum with a default decodes as that default; one without a default
// decodes unset, and Validate names it as required. The decoder still
// refuses "". A set value, an optional enum and a list encode as before.
func TestRequiredEnumLeavesOutItsUnsetValue(t *testing.T) {
	draft := "draft"
	schema := ir.NewSchema("unset-enum", ir.SchemaKindGeneral)
	schema.Enums["Stage"] = &ir.EnumDef{Name: "Stage", Values: []ir.EnumValueDef{
		{Name: "Draft", SerializedAs: "draft"},
		{Name: "Done", SerializedAs: "done"},
	}}
	schema.Types["Task"] = &ir.TypeDef{Name: "Task", Role: ir.RoleEmbeddedStruct, Fields: []*ir.FieldDef{
		{Name: "id", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		{Name: "stage", TypeRef: ir.TypeRef{Name: "Stage"}, Required: true},
		{Name: "next", TypeRef: ir.TypeRef{Name: "Stage"}},
		{Name: "start", TypeRef: ir.TypeRef{Name: "Stage"}, Required: true, Default: &draft},
		{Name: "history", TypeRef: ir.TypeRef{Name: "Stage", IsArray: true}, Required: true},
	}}
	schema.Types["Board"] = &ir.TypeDef{Name: "Board", Role: ir.RoleEmbeddedStruct, Fields: []*ir.FieldDef{
		{Name: "task", TypeRef: ir.TypeRef{Name: "Task"}, Required: true},
	}}
	output, err := Generate(schema, Options{
		SchemaName: schema.Name,
		ModulePath: "example.com/schemas/types/go/unset-enum",
	})
	if err != nil {
		t.Fatal(err)
	}
	const code = `package types

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestUnsetRequiredEnum(t *testing.T) {
	unset, err := json.Marshal(&Task{Id: "t1"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(unset), "\"stage\"") {
		t.Fatalf("an unset required enum is written: %s", unset)
	}
	var back Task
	if err := json.Unmarshal(unset, &back); err != nil {
		t.Fatalf("Go does not decode what it encodes: %s: %v", unset, err)
	}
	if back.Stage != "" {
		t.Fatalf("stage decoded as %q, want unset", back.Stage)
	}
	if back.Start != Stage_Draft {
		t.Fatalf("an unset enum with a default decoded as %q, want its default", back.Start)
	}
	if errs := back.Validate(); errs["stage"] == nil {
		t.Fatalf("Validate does not name the unset required enum: %v", errs)
	}

	set, err := json.Marshal(&Task{Id: "t2", Stage: Stage_Done, History: []Stage{Stage_Draft}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(set), "\"stage\":\"done\"") || !strings.Contains(string(set), "\"history\":[\"draft\"]") {
		t.Fatalf("a set enum or a list is not written: %s", set)
	}
	if strings.Contains(string(set), "\"next\"") {
		t.Fatalf("an unset optional enum is written: %s", set)
	}

	board, err := json.Marshal(&Board{Task: Task{Id: "t3"}})
	if err != nil {
		t.Fatal(err)
	}
	var boardBack Board
	if err := json.Unmarshal(board, &boardBack); err != nil || boardBack.Task.Id != "t3" || boardBack.Task.Start != Stage_Draft {
		t.Fatalf("a nested object with an unset enum does not survive: %s: %v", board, err)
	}

	if err := json.Unmarshal([]byte(` + "`" + `{"id":"t4","stage":"","history":[]}` + "`" + `), new(Task)); err == nil {
		t.Fatal("the decoder takes \"\" for a member")
	}
}
`
	runGeneratedModuleTest(t, output, "unset_enum", code)
}
