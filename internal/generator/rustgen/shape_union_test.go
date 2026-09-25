package rustgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// shapeUnionSchema declares two unions without a discriminator: triggers
// whose members differ by fields and by a defaulted kind, and refs where two
// members have identical fields, told apart only by a defaulted kind, next
// to a member that declares no kind.
func shapeUnionSchema() *ir.Schema {
	schema := ir.NewSchema("shape-unions", ir.SchemaKindGeneral)
	newMessage, existingMessage, page, pane := "new_message", "existing_message", "page", "pane"
	schema.Enums["TriggerKind"] = &ir.EnumDef{Name: "TriggerKind", Values: []ir.EnumValueDef{{Name: "NEW_MESSAGE", SerializedAs: newMessage}, {Name: "EXISTING_MESSAGE", SerializedAs: existingMessage}}}
	schema.Enums["PieceKind"] = &ir.EnumDef{Name: "PieceKind", Values: []ir.EnumValueDef{{Name: "PAGE", SerializedAs: page}, {Name: "PANE", SerializedAs: pane}}}
	schema.Types["UnionMessage"] = &ir.TypeDef{Name: "UnionMessage", Role: ir.RoleEmbeddedStruct, Fields: []*ir.FieldDef{
		{Name: "text", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
	}}
	schema.Types["NewMessageTrigger"] = &ir.TypeDef{Name: "NewMessageTrigger", Role: ir.RoleEmbeddedStruct, Fields: []*ir.FieldDef{
		{Name: "kind", TypeRef: ir.TypeRef{Name: "TriggerKind"}, Required: true, Default: &newMessage},
		{Name: "message", TypeRef: ir.TypeRef{Name: "UnionMessage"}, Required: true},
	}}
	schema.Types["ExistingMessageTrigger"] = &ir.TypeDef{Name: "ExistingMessageTrigger", Role: ir.RoleEmbeddedStruct, Fields: []*ir.FieldDef{
		{Name: "kind", TypeRef: ir.TypeRef{Name: "TriggerKind"}, Required: true, Default: &existingMessage},
		{Name: "messageId", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
	}}
	schema.Unions["Trigger"] = &ir.UnionDef{Name: "Trigger", Types: []string{"NewMessageTrigger", "ExistingMessageTrigger"}}
	for name, kind := range map[string]*string{"PageRef": &page, "PaneRef": &pane} {
		schema.Types[name] = &ir.TypeDef{Name: name, Role: ir.RoleEmbeddedStruct, Fields: []*ir.FieldDef{
			{Name: "kind", TypeRef: ir.TypeRef{Name: "PieceKind"}, Required: true, Default: kind},
			{Name: "pieceId", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		}}
	}
	schema.Types["SlotRef"] = &ir.TypeDef{Name: "SlotRef", Role: ir.RoleEmbeddedStruct, Fields: []*ir.FieldDef{
		{Name: "slotKey", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
	}}
	schema.Unions["PieceRef"] = &ir.UnionDef{Name: "PieceRef", Types: []string{"PageRef", "PaneRef", "SlotRef"}}
	schema.Types["Attachment"] = &ir.TypeDef{Name: "Attachment", Role: ir.RoleEmbeddedStruct, Fields: []*ir.FieldDef{
		{Name: "primary", TypeRef: ir.TypeRef{Name: "PieceRef"}, Required: true},
		{Name: "others", TypeRef: ir.TypeRef{Name: "PieceRef", IsArray: true}, Required: true},
	}}
	return schema
}

// TestUntaggedUnionShapes pins the member shapes rustgen hands the template,
// for local members and for a member imported from a dependency, whose kind
// enum lives in that dependency.
func TestUntaggedUnionShapes(t *testing.T) {
	output, err := Generate(shapeUnionSchema(), Options{SchemaName: "shape-unions"})
	if err != nil {
		t.Fatal(err)
	}
	if !output.HasUntaggedUnions() {
		t.Fatal("untagged unions not reported")
	}
	want := map[string][]UnionMemberInfo{
		"PieceRef": {
			{Name: "PageRef", Fields: []string{"kind", "pieceId"}, Tags: []codegen.UnionTag{{Field: "kind", Value: "page"}}},
			{Name: "PaneRef", Fields: []string{"kind", "pieceId"}, Tags: []codegen.UnionTag{{Field: "kind", Value: "pane"}}},
			{Name: "SlotRef", Fields: []string{"slotKey"}},
		},
		"Trigger": {
			{Name: "NewMessageTrigger", Fields: []string{"kind", "message"}, Tags: []codegen.UnionTag{{Field: "kind", Value: "new_message"}}},
			{Name: "ExistingMessageTrigger", Fields: []string{"kind", "messageId"}, Tags: []codegen.UnionTag{{Field: "kind", Value: "existing_message"}}},
		},
	}
	for _, union := range output.Unions {
		if !reflect.DeepEqual(union.Members, want[union.Name]) {
			t.Errorf("%s members = %+v, want %+v", union.Name, union.Members, want[union.Name])
		}
	}

	dependency := shapeUnionSchema()
	dependency.Name = "shape-dep"
	consumer := ir.NewSchema("shape-consumer", ir.SchemaKindGeneral)
	consumer.Imports = []ir.Import{{Package: "@schemas/shape-dep", Types: []string{"PaneRef"}}}
	sheet := "sheet"
	consumer.Enums["SheetKind"] = &ir.EnumDef{Name: "SheetKind", Values: []ir.EnumValueDef{{Name: "SHEET", SerializedAs: sheet}}}
	consumer.Types["SheetRef"] = &ir.TypeDef{Name: "SheetRef", Role: ir.RoleEmbeddedStruct, Fields: []*ir.FieldDef{
		{Name: "kind", TypeRef: ir.TypeRef{Name: "SheetKind"}, Required: true, Default: &sheet},
		{Name: "pieceId", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
	}}
	consumer.Unions["LocalOrImportedRef"] = &ir.UnionDef{Name: "LocalOrImportedRef", Types: []string{"SheetRef", "PaneRef"}}
	output, err = Generate(consumer, Options{SchemaName: consumer.Name, Dependencies: map[string]*ir.Schema{dependency.Name: dependency}})
	if err != nil {
		t.Fatal(err)
	}
	wantImported := []UnionMemberInfo{
		{Name: "SheetRef", Fields: []string{"kind", "pieceId"}, Tags: []codegen.UnionTag{{Field: "kind", Value: "sheet"}}},
		{Name: "PaneRef", Fields: []string{"kind", "pieceId"}, Tags: []codegen.UnionTag{{Field: "kind", Value: "pane"}}},
	}
	if got := output.Unions[0].Members; !reflect.DeepEqual(got, wantImported) {
		t.Fatalf("imported member shapes = %+v, want %+v", got, wantImported)
	}
}

// TestGeneratedUntaggedUnionDecodesTheMember builds the crate and runs Rust
// tests. serde's untagged derive tried each member in turn, and a member's
// derived Deserialize ignores keys it does not declare, so a pane ref
// decoded as a page ref, and any payload with an unknown key decoded as the
// first member whose required fields it had.
func TestGeneratedUntaggedUnionDecodesTheMember(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compiled untagged union check in short mode")
	}
	cargoPath, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping compiled untagged union check")
	}
	output, err := Generate(shapeUnionSchema(), Options{
		SchemaName: "shape-unions",
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatal(err)
	}
	outDir := t.TempDir()
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatal(err)
	}
	cargoToml, err := os.ReadFile(filepath.Join(outDir, "Cargo.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cargoToml), "serde_json") {
		t.Fatalf("a crate with an untagged union must depend on serde_json:\n%s", cargoToml)
	}
	testsDir := filepath.Join(outDir, "tests")
	if err := os.Mkdir(testsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	crate := strings.ReplaceAll(output.CrateName, "-", "_")
	decodeTest := `use ` + crate + `::{Attachment, PieceRef, Trigger};

fn piece(text: &str) -> PieceRef {
    serde_json::from_str(text).unwrap_or_else(|err| panic!("decode {text}: {err}"))
}

fn trigger(text: &str) -> Trigger {
    serde_json::from_str(text).unwrap_or_else(|err| panic!("decode {text}: {err}"))
}

#[test]
fn identical_shapes_are_told_apart_by_their_tag() {
    assert!(matches!(piece(r#"{"kind":"pane","pieceId":"p"}"#), PieceRef::PaneRef(_)), "pane ref misrouted");
    assert!(matches!(piece(r#"{"kind":"page","pieceId":"p"}"#), PieceRef::PageRef(_)), "page ref misrouted");
    assert!(matches!(piece(r#"{"slotKey":"s"}"#), PieceRef::SlotRef(_)), "slot ref misrouted");
    // A payload that states no tag takes the first member whose fields it has.
    assert!(matches!(piece(r#"{"pieceId":"p"}"#), PieceRef::PageRef(_)), "untagged page ref misrouted");
    // Unknown keys are tolerated once no member declares them all; tags still apply.
    assert!(matches!(piece(r#"{"kind":"pane","pieceId":"p","legacy":1}"#), PieceRef::PaneRef(_)), "unknown key broke tag dispatch");
    // A non-string tag contradicts both tagged members, and the slot member needs slotKey.
    assert!(serde_json::from_str::<PieceRef>(r#"{"kind":7,"pieceId":"p"}"#).is_err());
}

#[test]
fn members_with_different_fields_are_told_apart() {
    assert!(matches!(trigger(r#"{"kind":"new_message","message":{"text":"hi"}}"#), Trigger::NewMessageTrigger(_)));
    assert!(matches!(trigger(r#"{"kind":"existing_message","messageId":"m"}"#), Trigger::ExistingMessageTrigger(_)));
    assert!(matches!(trigger(r#"{"messageId":"m"}"#), Trigger::ExistingMessageTrigger(_)));
    assert!(matches!(trigger(r#"{"kind":"existing_message","messageId":"m","legacy":true}"#), Trigger::ExistingMessageTrigger(_)));
    // A new-message shape tagged existing is neither member.
    assert!(serde_json::from_str::<Trigger>(r#"{"kind":"existing_message","message":{"text":"hi"}}"#).is_err());
}

#[test]
fn a_payload_that_is_not_an_object_is_refused() {
    let err = serde_json::from_str::<PieceRef>(r#"["pane","p"]"#).unwrap_err();
    assert!(err.to_string().contains("PieceRef requires a JSON object"), "{err}");
    let err = serde_json::from_str::<PieceRef>(r#"{"other":1}"#).unwrap_err();
    assert!(err.to_string().contains("did not match any variant of untagged enum PieceRef"), "{err}");
}

#[test]
fn union_fields_decode_and_round_trip() {
    let text = r#"{"primary":{"kind":"pane","pieceId":"p"},"others":[{"kind":"page","pieceId":"a"},{"kind":"pane","pieceId":"b"},{"slotKey":"s"}]}"#;
    let decoded: Attachment = serde_json::from_str(text).expect("decode attachment");
    assert!(matches!(decoded.primary, PieceRef::PaneRef(_)), "{decoded:?}");
    assert!(matches!(decoded.others[..], [PieceRef::PageRef(_), PieceRef::PaneRef(_), PieceRef::SlotRef(_)]), "{decoded:?}");
    let encoded = serde_json::to_value(&decoded).unwrap();
    assert_eq!(encoded, serde_json::from_str::<serde_json::Value>(text).unwrap());
    let again: Attachment = serde_json::from_value(encoded).unwrap();
    assert_eq!(again, decoded);
}
`
	if err := os.WriteFile(filepath.Join(testsDir, "untagged_union.rs"), []byte(decodeTest), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(cargoPath, "test", "--quiet")
	cmd.Dir = outDir
	cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+filepath.Join(outDir, "target"))
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cargo test failed: %v\n%s", err, combined)
	}
}
