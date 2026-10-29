package verify

import (
	"slices"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestFileTransfersAuthoredAsIR: a FileUpload field, authored as IR, is a
// single string with its limit in its config, of a type that is only the
// single input of a POST, PUT or PATCH; a FileDownload sits on a result
// that is never null, encrypted or paged (D81).
func TestFileTransfersAuthoredAsIR(t *testing.T) {
	str := ir.TypeRef{Name: "string"}
	upload := func() *ir.FileUploadConfig { return &ir.FileUploadConfig{MaxSize: 1024} }
	download := func() *ir.FileDownloadDef { return &ir.FileDownloadDef{ContentType: "text/csv"} }
	limit := int64(10)
	schema := ir.NewSchema("statements", ir.SchemaKindAPI)
	schema.Types["ImportInput"] = &ir.TypeDef{Name: "ImportInput", Role: ir.RoleAPIInput, Fields: []*ir.FieldDef{
		{Name: "account", TypeRef: str, Required: true},
		{Name: "statement", TypeRef: str, Required: true, FileUpload: upload()},
	}}
	schema.Types["BadInput"] = &ir.TypeDef{Name: "BadInput", Role: ir.RoleAPIInput, Fields: []*ir.FieldDef{
		{Name: "files", TypeRef: ir.TypeRef{Name: "string", IsArray: true}, FileUpload: upload()},
		{Name: "bounded", TypeRef: str, FileUpload: upload(), ValidateUploadMaxBytes: &limit},
		{Name: "typed", TypeRef: str, FileUpload: &ir.FileUploadConfig{AllowedTypes: []string{"text/CSV"}, AllowedExtensions: []string{"csv"}}},
		{Name: "export", TypeRef: str, FileDownload: download()},
	}}
	schema.Types["Holder"] = &ir.TypeDef{Name: "Holder", Role: ir.RoleAPIView, Fields: []*ir.FieldDef{
		{Name: "input", TypeRef: ir.TypeRef{Name: "ImportInput"}, Required: true},
	}}
	input := func() []*ir.ArgumentDef {
		return []*ir.ArgumentDef{{Name: "input", TypeRef: ir.TypeRef{Name: "ImportInput"}, Required: true}}
	}
	schema.OperationSets = []*ir.OperationSet{
		{Name: "Statements", Operations: []*ir.FieldDef{
			{Name: "importStatement", HTTPMethod: "POST", TypeRef: str, Required: true, Arguments: input()},
			{Name: "replaceStatement", HTTPMethod: "PUT", TypeRef: str, Required: true, Arguments: input()},
			{Name: "readStatement", HTTPMethod: "GET", TypeRef: str, Required: true, Arguments: input()},
			{Name: "importMany", HTTPMethod: "POST", TypeRef: str, Required: true, Arguments: []*ir.ArgumentDef{
				{Name: "inputs", TypeRef: ir.TypeRef{Name: "ImportInput", IsArray: true}, Required: true},
			}},
			{Name: "echoInput", HTTPMethod: "GET", TypeRef: ir.TypeRef{Name: "ImportInput"}, Required: true},
			{Name: "uploadOp", HTTPMethod: "POST", TypeRef: str, Required: true, FileUpload: upload()},
			{Name: "exportStatement", HTTPMethod: "GET", Required: true, FileDownload: download()},
			{Name: "exportMaybe", HTTPMethod: "GET", FileDownload: download()},
			{Name: "exportPages", HTTPMethod: "GET", Required: true, FileDownload: download(), Page: &ir.OperationPage{DefaultLimit: 1, MaxLimit: 1}},
			{Name: "exportTool", HTTPMethod: "GET", Required: true, FileDownload: download(), MCP: &ir.OperationMCP{Handle: "export"}},
			{Name: "exportHidden", HTTPMethod: "GET", Required: true, FileDownload: download(), MCP: &ir.OperationMCP{Hidden: true, HiddenReason: "Staff export it."}},
		}},
		{Name: "Vault", Encrypted: true, Operations: []*ir.FieldDef{
			{Name: "exportSecrets", HTTPMethod: "GET", Required: true, FileDownload: download()},
		}},
	}
	r := &Result{}
	checkFileTransfers(schema, r)
	var got []string
	for _, d := range r.Errors {
		got = append(got, d.Msg)
	}
	want := []string{
		"BadInput.files: a FileUpload is one file, a string field, not string[]",
		"BadInput.bounded: a FileUpload takes its limit in its maxBytes, and no default or other rule",
		`BadInput.typed: FileUpload contentTypes holds "text/CSV", which is not a lowercase media type without parameters, such as "text/csv"`,
		`BadInput.typed: FileUpload extensions holds "csv", which is not a filename extension with its dot, such as ".csv"`,
		"BadInput.export: FileDownload on a field; it marks an operation's result",
		"Holder.input: holds ImportInput, whose statement is a FileUpload; a type with a file is only an operation's input",
		"Statements.readStatement: its input ImportInput holds a file (statement), which travels as a multipart body: the operation is a POST, PUT or PATCH, not GET",
		"Statements.importMany(inputs): ImportInput holds a file (statement), so it is the operation's single input, the multipart body",
		"Statements.echoInput: answers ImportInput, whose statement is a FileUpload; a type with a file is only an operation's input",
		"Statements.uploadOp: FileUpload on an operation; it marks a field of the operation's input type",
		"Statements.exportMaybe: a FileDownload result is never null; answer 404 for a file there is not",
		"Statements.exportPages: @paginated on a FileDownload result, which answers no JSON list",
		"Statements.exportTool: a visible @mcp on a FileDownload result, which is no tool result; declare @mcp({ hidden: true, reason }) or no @mcp, and the tool documents list it hidden",
		"Vault.exportSecrets: a FileDownload result is the file's bytes, which no encrypted envelope carries",
	}
	if !slices.Equal(got, want) {
		t.Errorf("errors =\n%q\nwant\n%q", got, want)
	}
}

// TestFileUploadsAreAPIFields: a FileUpload field outside an API schema,
// in a table, say, is refused (D81).
func TestFileUploadsAreAPIFields(t *testing.T) {
	schema := ir.NewSchema("statements-db", ir.SchemaKindDB)
	schema.Types["Statement"] = &ir.TypeDef{Name: "Statement", Fields: []*ir.FieldDef{
		{Name: "file", TypeRef: ir.TypeRef{Name: "string"}, Required: true, FileUpload: &ir.FileUploadConfig{}},
	}}
	r := &Result{}
	checkFileTransfers(schema, r)
	if len(r.Errors) != 1 || r.Errors[0].Msg != "Statement.file: FileUpload in a DB schema; a file is a field of an API operation's input" {
		t.Errorf("errors = %+v", r.Errors)
	}
}
