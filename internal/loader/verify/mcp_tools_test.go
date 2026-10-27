package verify

import (
	"slices"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestMCPToolsLeftOutAreWarnings: a visible @mcp tool that an API's MCP
// endpoint leaves out of tools/list, because its input carries a file or
// its payload is encrypted, is a warning that names the operation, the
// tool and the reason; a hidden @mcp, an operation with no @mcp, and a
// tool the endpoint serves are not (D93).
func TestMCPToolsLeftOutAreWarnings(t *testing.T) {
	str := ir.TypeRef{Name: "string"}
	schema := ir.NewSchema("finances-api", ir.SchemaKindAPI)
	schema.Types["ImportStatementInput"] = &ir.TypeDef{Name: "ImportStatementInput", Role: ir.RoleAPIInput, Fields: []*ir.FieldDef{
		{Name: "accountId", TypeRef: str, Required: true},
		{Name: "statement", TypeRef: str, Required: true, FileUpload: &ir.FileUploadConfig{MaxSize: 1024}},
	}}
	input := []*ir.ArgumentDef{{Name: "input", TypeRef: ir.TypeRef{Name: "ImportStatementInput"}, Required: true}}
	visible := func(handle string) *ir.OperationMCP { return &ir.OperationMCP{Handle: handle} }
	schema.OperationSets = []*ir.OperationSet{
		{Name: "LineItems", Operations: []*ir.FieldDef{
			{Name: "importStatement", HTTPMethod: "POST", TypeRef: str, Required: true, Arguments: input, MCP: visible("import_statement")},
			{Name: "importQuietly", HTTPMethod: "POST", TypeRef: str, Required: true, Arguments: input, MCP: &ir.OperationMCP{Hidden: true, HiddenReason: "A file is no tool argument."}},
			{Name: "importUnclassified", HTTPMethod: "POST", TypeRef: str, Required: true, Arguments: input},
			{Name: "listLineItems", HTTPMethod: "GET", TypeRef: str, Required: true, MCP: visible("list_line_items")},
			{Name: "sealNote", HTTPMethod: "POST", TypeRef: str, Required: true, Encrypted: true, MCP: visible("seal_note")},
			{Name: "sealArgument", HTTPMethod: "POST", TypeRef: str, Required: true, MCP: visible("seal_argument"), Arguments: []*ir.ArgumentDef{
				{Name: "note", TypeRef: str, Required: true, Encrypted: true},
			}},
		}},
		{Name: "Vault", Encrypted: true, Operations: []*ir.FieldDef{
			{Name: "rotate", HTTPMethod: "POST", TypeRef: str, Required: true, MCP: visible("rotate")},
		}},
	}
	r := &Result{}
	checkMCPToolsLeftOut(schema, r)
	if len(r.Errors) > 0 {
		t.Fatalf("errors = %+v", r.Errors)
	}
	var got []string
	for _, d := range r.Warnings {
		got = append(got, d.Msg)
	}
	want := []string{
		"LineItems.importStatement: the visible @mcp tool import_statement uploads a file (ImportStatementInput.statement), which tool arguments cannot carry, so an API's MCP endpoint leaves it out of tools/list and invokeTool refuses it; declare @mcp({ hidden: true, reason }) to leave it out on purpose",
		"LineItems.sealNote: the visible @mcp tool seal_note has an encrypted payload, which only the SDK seals, so an API's MCP endpoint leaves it out of tools/list; declare @mcp({ hidden: true, reason }) to leave it out on purpose",
		"LineItems.sealArgument: the visible @mcp tool seal_argument has an encrypted payload, which only the SDK seals, so an API's MCP endpoint leaves it out of tools/list; declare @mcp({ hidden: true, reason }) to leave it out on purpose",
		"Vault.rotate: the visible @mcp tool rotate has an encrypted payload, which only the SDK seals, so an API's MCP endpoint leaves it out of tools/list; declare @mcp({ hidden: true, reason }) to leave it out on purpose",
	}
	if !slices.Equal(got, want) {
		t.Errorf("warnings =\n%q\nwant\n%q", got, want)
	}

	// The whole pass reports it, so check and build print it.
	if res := Run(schema, Input{}); !slices.ContainsFunc(res.Warnings, func(d *Diagnostic) bool { return d.Msg == want[0] }) {
		t.Errorf("Run's warnings = %+v, want the upload's", res.Warnings)
	}
}
