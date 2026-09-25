package typegen

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// Unions without an @internalMetadata discriminator are decoded by member
// shape. A member's UnmarshalJSON ignores keys it does not declare, so before
// shape checks the first member accepted every payload, and the containing
// type's Validate skipped union fields. A generated API route therefore
// accepted a trigger whose new message had no idempotency key, and decoded
// an existing-message trigger as a new-message trigger with an empty message.
func TestShapeDispatchedUnionDecodesAndValidatesTheMember(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping generated-module test run in -short mode")
	}
	paths := testpaths.Local(t)
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-db"))
	if err != nil {
		t.Fatal(err)
	}
	newMessage, existingMessage, page, pane := "new_message", "existing_message", "page", "pane"
	schema.Enums["TriggerKind"] = &ir.EnumDef{Name: "TriggerKind", Values: []ir.EnumValueDef{{Name: "NEW_MESSAGE", SerializedAs: newMessage}, {Name: "EXISTING_MESSAGE", SerializedAs: existingMessage}}}
	schema.Enums["PieceKind"] = &ir.EnumDef{Name: "PieceKind", Values: []ir.EnumValueDef{{Name: "PAGE", SerializedAs: page}, {Name: "PANE", SerializedAs: pane}}}
	schema.Types["UnionMessageInput"] = &ir.TypeDef{Name: "UnionMessageInput", Role: ir.RoleAPIInput, Fields: []*ir.FieldDef{
		{Name: "idempotencyKey", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
		{Name: "mentions", TypeRef: ir.TypeRef{Name: "UnionMessageInput", IsArray: true}, Required: true},
	}}
	schema.Types["NewMessageUnionTrigger"] = &ir.TypeDef{Name: "NewMessageUnionTrigger", Role: ir.RoleAPIInput, Fields: []*ir.FieldDef{
		{Name: "kind", TypeRef: ir.TypeRef{Name: "TriggerKind"}, Required: true, Default: &newMessage},
		{Name: "message", TypeRef: ir.TypeRef{Name: "UnionMessageInput"}, Required: true},
	}}
	schema.Types["ExistingMessageUnionTrigger"] = &ir.TypeDef{Name: "ExistingMessageUnionTrigger", Role: ir.RoleAPIInput, Fields: []*ir.FieldDef{
		{Name: "kind", TypeRef: ir.TypeRef{Name: "TriggerKind"}, Required: true, Default: &existingMessage},
		{Name: "messageId", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
	}}
	schema.Unions["UnionTrigger"] = &ir.UnionDef{Name: "UnionTrigger", Types: []string{"NewMessageUnionTrigger", "ExistingMessageUnionTrigger"}}
	// Identical shapes told apart only by a defaulted kind, plus one member
	// that declares no kind at all.
	for name, kind := range map[string]*string{"PageUnionRef": &page, "PaneUnionRef": &pane} {
		schema.Types[name] = &ir.TypeDef{Name: name, Role: ir.RoleEmbeddedStruct, Fields: []*ir.FieldDef{
			{Name: "kind", TypeRef: ir.TypeRef{Name: "PieceKind"}, Required: true, Default: kind},
			{Name: "pieceId", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
		}}
	}
	schema.Types["SlotUnionRef"] = &ir.TypeDef{Name: "SlotUnionRef", Role: ir.RoleEmbeddedStruct, Fields: []*ir.FieldDef{
		{Name: "slotKey", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
	}}
	schema.Unions["UnionRef"] = &ir.UnionDef{Name: "UnionRef", Types: []string{"PageUnionRef", "PaneUnionRef", "SlotUnionRef"}}
	schema.Types["StartUnionInput"] = &ir.TypeDef{Name: "StartUnionInput", Role: ir.RoleAPIInput, Fields: []*ir.FieldDef{
		{Name: "trigger", TypeRef: ir.TypeRef{Name: "UnionTrigger"}, Required: true},
		{Name: "triggers", TypeRef: ir.TypeRef{Name: "UnionTrigger", IsArray: true}, Required: true},
		{Name: "byName", TypeRef: ir.TypeRef{Name: "UnionTrigger", IsMap: true}, Required: true},
		{Name: "optional", TypeRef: ir.TypeRef{Name: "UnionTrigger"}},
	}}
	schema.Types["UnionRefView"] = &ir.TypeDef{Name: "UnionRefView", Role: ir.RoleEmbeddedStruct, Fields: []*ir.FieldDef{
		{Name: "ref", TypeRef: ir.TypeRef{Name: "UnionRef"}, Required: true},
		{Name: "optional", TypeRef: ir.TypeRef{Name: "UnionRef"}},
		{Name: "grid", TypeRef: ir.TypeRef{Name: "UnionRef", IsArray: true, IsArrayOfArrays: true}},
	}}
	output, err := Generate(schema, Options{SchemaName: "fixture-db", ModulePath: "example.com/schemas/types/go/union-validate"})
	if err != nil {
		t.Fatal(err)
	}
	if !output.HasShapeDispatchedUnions {
		t.Fatal("shape-dispatched unions not reported")
	}
	dir := filepath.Join(t.TempDir(), "union-validate")
	if err := SetReplacePaths(output, paths, dir); err != nil {
		t.Fatal(err)
	}
	if err := WriteTypes(output, dir); err != nil {
		t.Fatal(err)
	}
	const code = `package types
import("encoding/json";"fmt";"strings";"testing")
const key = "60000000-0000-4000-8000-000000000001"
const valid = "{\"kind\":\"new_message\",\"message\":{\"idempotencyKey\":\"" + key + "\",\"mentions\":[]}}"
const existing = "{\"kind\":\"existing_message\",\"messageId\":\"" + key + "\"}"
func decode(t *testing.T, payload string) StartUnionInput {
 t.Helper()
 var input StartUnionInput
 if err := json.Unmarshal([]byte(payload), &input); err != nil { t.Fatalf("decode %s: %v", payload, err) }
 return input
}
func member(t *testing.T, payload string) UnionTrigger {
 t.Helper()
 var w UnionTriggerWrapper
 if err := w.UnmarshalJSON([]byte(payload)); err != nil { t.Fatalf("decode %s: %v", payload, err) }
 return w.Value
}
func ref(t *testing.T, payload string) UnionRef {
 t.Helper()
 var w UnionRefWrapper
 if err := w.UnmarshalJSON([]byte(payload)); err != nil { t.Fatalf("decode %s: %v", payload, err) }
 return w.Value
}
func expect(t *testing.T, errors ValidationErrors, paths ...string) {
 t.Helper()
 text := fmt.Sprint(errors)
 for _, path := range paths { if !strings.Contains(text, path) { t.Fatalf("missing %q in %s", path, text) } }
}
func TestDispatch(t *testing.T) {
 if _, ok := member(t, valid).(NewMessageUnionTrigger); !ok { t.Fatal("new-message trigger misrouted") }
 if _, ok := member(t, existing).(ExistingMessageUnionTrigger); !ok { t.Fatal("existing-message trigger decoded as the first member") }
 if _, ok := member(t, "{\"messageId\":\"" + key + "\"}").(ExistingMessageUnionTrigger); !ok { t.Fatal("untagged existing-message trigger misrouted") }
 // A stated tag must match: a new-message shape tagged existing is not a new message.
 if got, ok := member(t, "{\"kind\":\"existing_message\",\"message\":{}}").(ExistingMessageUnionTrigger); !ok || got.Validate().HasErrors() == false { t.Fatalf("contradicted tag accepted as %T", got) }
 // Unknown keys are tolerated once no member declares them all.
 if _, ok := member(t, "{\"kind\":\"existing_message\",\"messageId\":\"" + key + "\",\"legacy\":true}").(ExistingMessageUnionTrigger); !ok { t.Fatal("unknown key broke tagged dispatch") }
 if got, ok := ref(t, "{\"kind\":\"pane\",\"pieceId\":\"" + key + "\"}").(PaneUnionRef); !ok || got.Kind != "pane" { t.Fatalf("pane ref decoded as %T", got) }
 if _, ok := ref(t, "{\"kind\":\"page\",\"pieceId\":\"" + key + "\"}").(PageUnionRef); !ok { t.Fatal("page ref misrouted") }
 if _, ok := ref(t, "{\"slotKey\":\"" + key + "\"}").(SlotUnionRef); !ok { t.Fatal("untagged slot ref decoded as the first member") }
 if _, ok := ref(t, "{\"kind\":\"pane\",\"pieceId\":\"" + key + "\",\"legacy\":1}").(PaneUnionRef); !ok { t.Fatal("unknown key broke tag dispatch") }
 var w UnionRefWrapper
 if err := w.UnmarshalJSON([]byte("{\"kind\":7,\"pieceId\":\"" + key + "\"}")); err == nil { if _, ok := w.Value.(SlotUnionRef); !ok { t.Fatalf("non-string tag accepted as %T", w.Value) } }
}
func TestValidation(t *testing.T) {
 input := decode(t, "{\"trigger\":" + valid + ",\"triggers\":[" + existing + "],\"byName\":{\"a\":" + valid + "},\"optional\":null}")
 if errors := input.Validate(); errors.HasErrors() { t.Fatalf("complete input refused: %v", errors) }

 // The reported payload: the new-message member decodes, but its message
 // omits the idempotency key and the required mentions array.
 input = decode(t, "{\"trigger\":{\"kind\":\"new_message\",\"message\":{}},\"triggers\":[],\"byName\":{}}")
 errors := input.Validate()
 nested := errors.GetNestedErrors("trigger").GetNestedErrors("message")
 if len(nested.GetFieldErrors("idempotencyKey")) == 0 || len(nested.GetFieldErrors("mentions")) == 0 { t.Fatalf("incomplete member accepted: %v", errors) }

 input = decode(t, "{}")
 errors = input.Validate()
 for _, field := range []string{"trigger", "triggers", "byName"} {
  if len(errors.GetFieldErrors(field)) == 0 { t.Fatalf("absent required union %s accepted: %v", field, errors) }
 }

 input = decode(t, "{\"trigger\":" + valid + ",\"triggers\":[" + valid + ",{\"message\":{}}],\"byName\":{\"bad\":{\"message\":{}}},\"optional\":{\"message\":{}}}")
 errors = input.Validate()
 expect(t, errors, "triggers[1]", "byName[bad]", "optional")
 if len(errors.GetNestedErrors("triggers[0]")) != 0 { t.Fatalf("valid element reported: %v", errors) }

 // Members held by pointer validate too; a nil element is not a member.
 input.Trigger = &NewMessageUnionTrigger{}
 input.Triggers = []UnionTrigger{nil}
 errors = input.Validate()
 expect(t, errors, "trigger", "triggers[0]")
 if len(errors.GetNestedErrors("trigger").GetNestedErrors("message")) == 0 { t.Fatalf("pointer member skipped: %v", errors) }

 view := UnionRefView{}
 if errors := view.Validate(); len(errors.GetFieldErrors("ref")) == 0 || len(errors.GetFieldErrors("optional")) != 0 { t.Fatalf("view presence: %v", errors) }
 view.Ref = SlotUnionRef{}
 view.Optional = PaneUnionRef{}
 errors = view.Validate()
 if len(errors.GetNestedErrors("ref").GetFieldErrors("slotKey")) == 0 || len(errors.GetNestedErrors("optional").GetFieldErrors("pieceId")) == 0 { t.Fatalf("view members skipped: %v", errors) }

 // A list of lists of a union validates each innermost member; a nil
 // element or inner list is required.
 view = UnionRefView{}
 if err := json.Unmarshal([]byte("{\"ref\":{\"slotKey\":\"" + key + "\"},\"grid\":[[{\"kind\":\"pane\",\"pieceId\":\"" + key + "\"},{\"slotKey\":\"" + key + "\"}]]}"), &view); err != nil { t.Fatal(err) }
 if _, ok := view.Grid[0][0].(PaneUnionRef); !ok { t.Fatalf("grid[0][0] decoded as %T", view.Grid[0][0]) }
 if errors := view.Validate(); errors.HasErrors() { t.Fatalf("complete grid refused: %v", errors) }
 view.Grid = [][]UnionRef{{SlotUnionRef{}, nil}, nil}
 errors = view.Validate()
 if len(errors.GetNestedErrors("grid[0][0]").GetFieldErrors("slotKey")) == 0 || len(errors.GetFieldErrors("grid[0][1]")) == 0 || len(errors.GetFieldErrors("grid[1]")) == 0 { t.Fatalf("grid members skipped: %v", errors) }
}`
	if err := os.WriteFile(filepath.Join(dir, "union_validate_test.go"), []byte(code), 0o600); err != nil {
		t.Fatal(err)
	}
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = dir
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Skipf("go mod tidy failed (likely offline): %v\n%s", err, out)
	}
	command := exec.Command("go", "test", "-count=1", ".")
	command.Dir = dir
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated union dispatch and validation failed: %v\n%s", err, out)
	}
}
