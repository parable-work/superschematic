package apigen_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/loader"
)

const filesAPI = "fixture-files-api"

// TestFileTransfersInTheAPIOutput: apigen reads a FileUpload field's
// constraints, extensions included, as it reads an upload scalar's, and a
// FileDownload result into the endpoint, with no output type; the OpenAPI
// document describes the multipart part's media types and extensions, and
// the download's binary body of its media type with Content-Disposition;
// and the tool documents list the downloads hidden (D81).
func TestFileTransfersInTheAPIOutput(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, filesAPI))
	if err != nil {
		t.Fatalf("load %s: %v", filesAPI, err)
	}
	output, err := apigen.Generate(schema, apigen.Options{Provider: sessionauth.Provider{}, SchemaName: filesAPI, Clock: goModuleClock})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	byName := map[string]apigen.EndpointInfo{}
	for _, endpoint := range output.Endpoints {
		byName[endpoint.Name] = endpoint
	}
	imports := byName["importStatement"]
	if !imports.HasFileUpload || len(imports.FileUploadFields) != 2 {
		t.Fatalf("importStatement uploads = %+v", imports.FileUploadFields)
	}
	statement := imports.FileUploadFields[0]
	if statement.Name != "statement" || statement.MaxSize != 1024 || !statement.Required ||
		strings.Join(statement.AllowedTypes, ",") != "text/csv,application/x-ofx" || strings.Join(statement.AllowedExtensions, ",") != ".csv,.ofx,.qfx" {
		t.Errorf("statement upload = %+v", statement)
	}
	export := byName["exportStatement"]
	if export.FileDownload == nil || export.FileDownload.ContentType != "text/csv; charset=utf-8" || export.OutputType != "" {
		t.Errorf("exportStatement download = %+v, output %q", export.FileDownload, export.OutputType)
	}
	preview := byName["previewStatement"].FileDownload
	if preview == nil || preview.Disposition != "inline" || strings.Join(preview.ContentTypes, ",") != "text/html; charset=utf-8" ||
		preview.CacheControl != "private, no-cache" || preview.SandboxPolicy() != "sandbox allow-popups" || preview.Filename != "statement.txt" {
		t.Errorf("previewStatement download = %+v", preview)
	}
	if got := byName["previewStatement"].FileSpecLiteral(); got != `FileSpec{ContentType: "text/plain; charset=utf-8", ContentTypes: []string{"text/html; charset=utf-8"}, Filename: "statement.txt", Disposition: "inline", CacheControl: "private, no-cache", Sandbox: true, SandboxAllow: []string{"allow-popups"}}` {
		t.Errorf("previewStatement spec = %s", got)
	}
	if got := export.FileSpecLiteral(); got != `FileSpec{ContentType: "text/csv; charset=utf-8", Filename: "statement.csv"}` {
		t.Errorf("exportStatement spec = %s", got)
	}
	if export.MCP == nil || !export.MCP.Hidden || export.MCP.HiddenReason == "" {
		t.Errorf("exportStatement's tool = %+v, want it hidden with a reason", export.MCP)
	}
	for _, want := range []string{
		`"encoding": {`, `"contentType": "text/csv, application/x-ofx"`,
		"(allowed extensions: .csv, .ofx, .qfx), max size: 1024 bytes",
		`"text/csv; charset=utf-8": {`, `"format": "binary"`, `"Content-Disposition": {`,
		`\"statement.csv\" unless the operation names another`,
		// D141: each media type, the disposition, the headers and the 304.
		`"text/html; charset=utf-8": {`, `inline, with the file's name in filename and filename* (RFC 6266)`,
		`"description": "sandbox allow-popups"`, `"Cross-Origin-Resource-Policy": {`, `"X-Content-Type-Options": {`,
		`\"private, no-cache\", unless the operation sets another`, `"ETag": {`, `"304": {`,
	} {
		if !strings.Contains(output.OpenAPISpecRaw, want) {
			t.Errorf("the OpenAPI document lacks %s", want)
		}
	}
}

// TestFileTransferRoutes generates the Go types and API modules of
// fixture-files-api, copies testdata/files_routes_test.go into the API
// module and runs it: the Go router reads the multipart input, refuses each
// file its field does not admit with the same field errors as the
// TypeScript router (internal/generator/tsrestgen, TestGeneratedFilesEndToEnd),
// and answers the downloads with their Content-Type and Content-Disposition
// (D81).
func TestFileTransferRoutes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, filesAPI))
	if err != nil {
		t.Fatalf("load %s: %v", filesAPI, err)
	}
	apiDir := writeGoAPIModule(t, schema, filesAPI)
	source, err := os.ReadFile(filepath.Join("testdata", "files_routes_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(apiDir, "files_routes_test.go"), source, 0o644); err != nil {
		t.Fatal(err)
	}
	runGoAPIModule(t, apiDir)
}
