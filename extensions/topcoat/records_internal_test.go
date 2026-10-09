package topcoat

import (
	"testing"

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
