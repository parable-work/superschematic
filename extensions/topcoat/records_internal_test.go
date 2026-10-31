package topcoat

import (
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/rustrestgen"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// TestRecordsLeaveOutWhatThePageMustNotHold builds the records of a schema
// whose authored operation returns a type with a secret field beside the
// user model's login, whose result carries the session token (D50): the
// secret field is left out as a @uiHidden one is, and the user model's
// result gets no record at all, since its operation has no in-process call
// (noCall), so newCrate does not hand it to addResults.
func TestRecordsLeaveOutWhatThePageMustNotHold(t *testing.T) {
	schema := ir.NewSchema("fixture", ir.SchemaKindAPI)
	schema.Types["Account"] = &ir.TypeDef{Name: "Account", Fields: []*ir.FieldDef{
		{Name: "name", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		{Name: "apiKey", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Secret: true},
		{Name: "notes", TypeRef: ir.TypeRef{Name: "string"}, Required: true, UIHidden: true},
	}}
	schema.Types["LoginResult"] = &ir.TypeDef{Name: "LoginResult", Origin: ir.OriginIdentity, Fields: []*ir.FieldDef{
		{Name: "expiresAt", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		{Name: "token", TypeRef: ir.TypeRef{Name: "string"}, Secret: true},
	}}
	schema.OperationSets = []*ir.OperationSet{
		{Name: "AccountQueries", Operations: []*ir.FieldDef{{Name: "getAccount", TypeRef: ir.TypeRef{Name: "Account"}}}},
		{Name: "Session", Operations: []*ir.FieldDef{
			{Name: "login", TypeRef: ir.TypeRef{Name: "LoginResult"}, IdentityOperation: ir.IdentityOpLogin},
		}},
	}

	// newCrate hands addResults the operations with an in-process call, and
	// login, which the identity runtime serves, has none.
	account, login := schema.OperationSets[0].Operations[0], schema.OperationSets[1].Operations[0]
	if reason := noCall(registry.RustEndpoint{IdentityOperation: login.IdentityOperation}, login); reason == "" {
		t.Fatal("login has an in-process call")
	}
	if reason := noCall(registry.RustEndpoint{}, account); reason != "" {
		t.Fatalf("getAccount has no in-process call: %s", reason)
	}
	// A behavior operation's handler runs it through the behaviors' core,
	// and no namespace trait has a method a call could reach (D103).
	transition := &ir.FieldDef{Name: "statusTransition", TypeRef: ir.TypeRef{Name: "OrderStatusTransitionResult"}}
	if reason := noCall(registry.RustEndpoint{Behavior: &rustrestgen.BehaviorInfo{}}, transition); !strings.Contains(reason, "behaviors' core") {
		t.Fatalf("statusTransition's reason for no in-process call: %q, want the behaviors' core's", reason)
	}
	b := newRecordBuilder(schemaSet{schema})
	if err := b.addResults([]declared{{op: account}}); err != nil {
		t.Fatal(err)
	}
	records := b.sorted()
	if len(records) != 1 || records[0].Name != "AccountRecord" {
		t.Fatalf("records = %+v, want AccountRecord alone", records)
	}
	if fields := records[0].Fields; len(fields) != 1 || fields[0].JSONName != "name" {
		t.Errorf("AccountRecord's fields = %+v, want name alone", fields)
	}
}

// TestRecordsWithoutLeavesImportNoWire builds the records of a type whose
// fields are all left out, and of one whose only field holds it: neither
// reads a value with a wire helper, so records.rs does not import the
// module, which clippy would deny as unused. A field of a string does.
func TestRecordsWithoutLeavesImportNoWire(t *testing.T) {
	schema := ir.NewSchema("fixture", ir.SchemaKindAPI)
	schema.Types["Key"] = &ir.TypeDef{Name: "Key", Fields: []*ir.FieldDef{
		{Name: "secret", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Secret: true},
		{Name: "hint", TypeRef: ir.TypeRef{Name: "string"}, UIHidden: true},
	}}
	schema.Types["Rotation"] = &ir.TypeDef{Name: "Rotation", Fields: []*ir.FieldDef{
		{Name: "key", TypeRef: ir.TypeRef{Name: "Key"}, Required: true},
	}}
	schema.Types["Account"] = &ir.TypeDef{Name: "Account", Fields: []*ir.FieldDef{
		{Name: "name", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
	}}
	recordsOf := func(results ...string) []record {
		t.Helper()
		var ops []declared
		for _, name := range results {
			ops = append(ops, declared{op: &ir.FieldDef{Name: "get" + name, TypeRef: ir.TypeRef{Name: name}}})
		}
		b := newRecordBuilder(schemaSet{schema})
		if err := b.addResults(ops); err != nil {
			t.Fatal(err)
		}
		return b.sorted()
	}

	records := recordsOf("Rotation")
	if len(records) != 2 || len(records[0].Fields) != 0 || records[0].Name != "KeyRecord" {
		t.Fatalf("records = %+v, want an empty KeyRecord and RotationRecord", records)
	}
	if (&crate{Records: records}).RecordsUseWire() {
		t.Error("records whose fields read no leaf import wire")
	}
	if !(&crate{Records: recordsOf("Rotation", "Account")}).RecordsUseWire() {
		t.Error("records with a string field do not import wire")
	}
}
