package loader

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

const streamFixture = "tsreader/testdata/services/fixture-stream-api"

// streamDecorator is the fixture's @httpBinaryStream line.
const streamDecorator = `@httpBinaryStream({ contentType: "application/vnd.apache.arrow.stream", responseHeaders: ["X-Export-Id"] })`

// streamService copies fixture-stream-api to a temp directory with its
// @httpBinaryStream line replaced by decorator, and returns the copy.
func streamService(t *testing.T, decorator string) string {
	t.Helper()
	source, err := os.ReadFile(filepath.FromSlash(streamFixture + "/src/reports.schema.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), streamDecorator) {
		t.Fatalf("the fixture's reports.schema.ts no longer reads %q", streamDecorator)
	}
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(filepath.FromSlash(streamFixture))); err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(source), streamDecorator, decorator, 1)
	if err := os.WriteFile(filepath.Join(dir, "src", "reports.schema.ts"), []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tsconfig.json"), []byte(viewTSConfig(t)), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestHTTPBinaryStreamLoads: @httpBinaryStream fills the operation's
// HTTPBinaryStream with its media type and its headers (D167), and leaves
// the declared result, which the tool documents keep. The operation beside
// it has none.
func TestHTTPBinaryStreamLoads(t *testing.T) {
	schema, err := LoadService(filepath.FromSlash(streamFixture))
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	ops := map[string]*ir.FieldDef{}
	for _, op := range schema.OperationSets[0].Operations {
		ops[op.Name] = op
	}
	want := &ir.HTTPBinaryStreamDef{ContentType: "application/vnd.apache.arrow.stream", ResponseHeaders: []string{"X-Export-Id"}}
	export := ops["exportReport"]
	if export == nil || !reflect.DeepEqual(export.HTTPBinaryStream, want) {
		t.Fatalf("exportReport's stream = %+v, want %+v", export, want)
	}
	if export.TypeRef.Name != "ReportSummary" || !export.ManualRouteRegistration {
		t.Errorf("exportReport = %+v, want a manual route that declares ReportSummary", export)
	}
	if ops["describeReport"].HTTPBinaryStream != nil {
		t.Errorf("describeReport streams: %+v", ops["describeReport"].HTTPBinaryStream)
	}
}

// TestHTTPBinaryStreamArgumentsAreRefused: the decorator's contentType is
// a lowercase media type without parameters that is not JSON, and its
// responseHeaders, when present, a list of header names, each once (D167).
// The type checker refuses a config of another shape before the decorator
// reads it.
func TestHTTPBinaryStreamArgumentsAreRefused(t *testing.T) {
	for _, tc := range []struct{ name, decorator, want string }{
		{"parameters", `@httpBinaryStream({ contentType: "text/csv; charset=utf-8" })`, `httpBinaryStream contentType "text/csv; charset=utf-8" is not a lowercase media type without parameters`},
		{"upper case", `@httpBinaryStream({ contentType: "Text/CSV" })`, `httpBinaryStream contentType "Text/CSV" is not a lowercase media type without parameters`},
		{"no subtype", `@httpBinaryStream({ contentType: "arrow" })`, `httpBinaryStream contentType "arrow" is not a lowercase media type without parameters`},
		{"JSON", `@httpBinaryStream({ contentType: "application/json" })`, `httpBinaryStream contentType "application/json" is JSON`},
		{"a JSON suffix", `@httpBinaryStream({ contentType: "application/problem+json" })`, `httpBinaryStream contentType "application/problem+json" is JSON`},
		{"empty headers", `@httpBinaryStream({ contentType: "text/csv", responseHeaders: [] })`, "@httpBinaryStream responseHeaders must be a non-empty list of header names"},
		{"an empty header", `@httpBinaryStream({ contentType: "text/csv", responseHeaders: [""] })`, "@httpBinaryStream responseHeaders must be a non-empty list of header names"},
		{"a header that is no name", `@httpBinaryStream({ contentType: "text/csv", responseHeaders: ["X Export"] })`, `httpBinaryStream responseHeaders holds "X Export", which is not a header name`},
		{"a header twice", `@httpBinaryStream({ contentType: "text/csv", responseHeaders: ["X-Export-Id", "x-export-id"] })`, `httpBinaryStream responseHeaders holds "x-export-id" twice`},
		{"twice", `@httpBinaryStream({ contentType: "text/csv" })` + "\n  " + `@httpBinaryStream({ contentType: "text/csv" })`, "operation exportReport has more than one @httpBinaryStream decorator"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := LoadService(streamService(t, tc.decorator)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("LoadService = %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

// streamDataService writes an API service in the data form whose one
// operation is operation, beside an input type that holds a file, and
// returns its directory.
func streamDataService(t *testing.T, operation string) string {
	t.Helper()
	return writeService(t, map[string]string{
		"schema.config.json": `{"name": "stream-api", "kind": "API", "outputs": {}}`,
		"src/reports.schema.json": `{"name": "stream-api", "kind": "API", "types": {
  "ReportSummary": {"name": "ReportSummary", "role": "EmbeddedStruct", "fields": [{"name": "rows", "typeRef": {"name": "number"}, "required": true}]},
  "UploadInput": {"name": "UploadInput", "role": "APIInput", "fields": [{"name": "file", "typeRef": {"name": "string"}, "required": true, "fileUpload": {}}]}
}, "operationSets": [{"name": "Reports", "operations": [` + operation + `]}]}`,
	})
}

func streamOperation(more string) string {
	return `{"name": "exportReport", "typeRef": {"name": "ReportSummary"}, "required": true, "httpMethod": "POST", "restPath": "reports/export", "httpBinaryStream": {"contentType": "application/vnd.apache.arrow.stream"}` + more + `}`
}

// TestHTTPBinaryStreamRoutesAreRefused: in every form, an @httpBinaryStream
// operation is a @manualRouteRegistration one, since the generated route
// answers the envelope, and one each SDK sends a plain JSON body: no
// webhook, no encrypted operation, no upload and no filter (D167). The
// data form's media type is held to the decorator's rule.
func TestHTTPBinaryStreamRoutesAreRefused(t *testing.T) {
	manual := `, "manualRouteRegistration": true`
	if _, err := LoadService(streamDataService(t, streamOperation(manual))); err != nil {
		t.Fatalf("a manual stream route: %v", err)
	}
	for _, tc := range []struct{ name, operation, want string }{
		{"a generated route", streamOperation(""), "Reports.exportReport: @httpBinaryStream on an operation without @manualRouteRegistration"},
		{"a webhook", streamOperation(manual + `, "webhook": true`), "Reports.exportReport: @httpBinaryStream on a @webhook, which no SDK calls"},
		{"an encrypted operation", streamOperation(manual + `, "encrypted": true`), "Reports.exportReport: @httpBinaryStream on an encrypted operation"},
		{"an upload", streamOperation(manual + `, "arguments": [{"name": "input", "typeRef": {"name": "UploadInput"}, "required": true}]`), "Reports.exportReport: @httpBinaryStream on an operation whose input UploadInput holds a file"},
		{"JSON", strings.Replace(streamOperation(manual), "application/vnd.apache.arrow.stream", "application/json", 1), `Reports.exportReport: httpBinaryStream contentType "application/json" is JSON`},
		{"a header that is no name", strings.Replace(streamOperation(manual), `stream"}`, `stream", "responseHeaders": ["X Export"]}`, 1), `Reports.exportReport: httpBinaryStream responseHeaders holds "X Export", which is not a header name`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := LoadService(streamDataService(t, tc.operation)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("LoadService = %v, want an error containing %q", err, tc.want)
			}
		})
	}
}
