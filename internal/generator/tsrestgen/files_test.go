package tsrestgen

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/sdkgen"
	"github.com/parable-work/superschematic/internal/generator/tsgen"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

const filesAPI = "fixture-files-api"

// filesFixture is fixture-files-api with apigen's endpoints for it (D81):
// statements.importStatement takes a statement file and an optional
// receipt beside its JSON fields, under @bodyLimit; exportStatement answers
// a CSV file under the schema's filename, and originalStatement a file
// under the implementation's.
func filesFixture(t *testing.T) apiFixture {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, filesAPI))
	if err != nil {
		t.Fatalf("load %s: %v", filesAPI, err)
	}
	endpoints, err := apigen.Generate(schema, apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: filesAPI,
		Clock:      fixedClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return apiFixture{name: filesAPI, schema: schema, endpoints: endpoints}
}

// TestWriteAPIGoldenFiles pins the package for fixture-files-api (D81).
// Regenerate with
// go test ./internal/generator/tsrestgen -run TestWriteAPIGoldenFiles -update
func TestWriteAPIGoldenFiles(t *testing.T) {
	checkGolden(t, generateFixture(t, filesFixture(t)), filesAPI)
}

// TestGenerateFilesShape: an input with file parts is no manual route: the
// operation table carries each part's limits in field order, args.input
// omits the file fields and args.files holds the files, optional where the
// field is. A FileDownload result is the runtime's FileDownload, and the
// table carries its media type and filename.
func TestGenerateFilesShape(t *testing.T) {
	output := generateFixture(t, filesFixture(t))
	if len(output.ManualEndpoints) != 0 {
		t.Fatalf("manual endpoints = %+v, want none", output.ManualEndpoints)
	}
	if !output.HasDownloads || !output.HasUploads() {
		t.Errorf("HasDownloads = %t, HasUploads = %t", output.HasDownloads, output.HasUploads())
	}
	byName := endpointsByName(output)
	imports := byName["importStatement"]
	wantUploads := []UploadInfo{
		{Name: "statement", TSName: "statement", Required: true, MaxBytes: 1024, ContentTypes: []string{"text/csv", "application/x-ofx"}, Extensions: []string{".csv", ".ofx", ".qfx"}},
		{Name: "receipt", TSName: "receipt", MaxBytes: ir.DefaultUploadMaxBytes, ContentTypes: []string{}, Extensions: []string{}},
	}
	if !reflect.DeepEqual(imports.Uploads, wantUploads) {
		t.Errorf("uploads = %+v, want %+v", imports.Uploads, wantUploads)
	}
	for got, want := range map[string]string{
		imports.InputArgType():   "Omit<ImportStatementInput, 'statement' | 'receipt'>",
		imports.FilesArgType():   "{ statement: File; receipt?: File }",
		imports.UploadsLiteral(): "[{ name: 'statement', required: true, maxBytes: 1024, contentTypes: ['text/csv', 'application/x-ofx'], extensions: ['.csv', '.ofx', '.qfx'] }, { name: 'receipt', required: false, maxBytes: 104857600, contentTypes: [], extensions: [] }]",
		imports.OutputType:       "ImportResult",
	} {
		if got != want {
			t.Errorf("importStatement: got %s, want %s", got, want)
		}
	}
	if imports.BodyLimitBytes == nil || *imports.BodyLimitBytes != 1<<20 || imports.Download != nil {
		t.Errorf("importStatement: body limit %v, download %+v", imports.BodyLimitBytes, imports.Download)
	}
	for name, want := range map[string]string{
		"exportStatement":   "{ contentType: 'text/csv; charset=utf-8', filename: 'statement.csv' }",
		"originalStatement": "{ contentType: 'application/octet-stream' }",
	} {
		ep := byName[name]
		if ep.Download == nil || ep.DownloadLiteral() != want || ep.OutputType != "FileDownload" || len(ep.Uploads) != 0 {
			t.Errorf("%s: download %+v, output %s, uploads %+v", name, ep.Download, ep.OutputType, ep.Uploads)
		}
	}
	if fixture := generateFixtureAPI(t); fixture.HasDownloads || fixture.HasUploads() {
		t.Error("fixture-api, which has no file, imports the file helpers")
	}
}

// TestGeneratedFilesEndToEnd type-checks the generated package for
// fixture-files-api with a typed implementation staged beside it, so tsc
// holds the methods to args.files and FileDownload, then serves it through
// the generated router under bun and calls it through the generated
// TypeScript SDK (testdata/files_runtime.test.ts): a statement imported as
// a multipart body and exported as CSV, with its filename, the refusals of
// each file its field does not admit as the Go router answers them, the
// @bodyLimit cap, and a download under the implementation's filename.
func TestGeneratedFilesEndToEnd(t *testing.T) {
	fixture := filesFixture(t)
	tree := materializeAPI(t, fixture)
	tree.stage(t, "files_implementation.ts")
	tree.typeCheck(t)

	// The SDK, beside the package, resolving its types package by name.
	tsOutput, err := tsgen.Generate(fixture.schema, tsgen.Options{SchemaName: filesAPI, Clock: fixedClock})
	if err != nil {
		t.Fatalf("tsgen.Generate: %v", err)
	}
	sdkOutput, err := sdkgen.Generate(fixture.endpoints, tsgen.ParseableTypeNames(tsOutput), fixedClock)
	if err != nil {
		t.Fatalf("sdkgen.Generate: %v", err)
	}
	sdkDir := filepath.Join(tree.root, "sdk", "typescript", filesAPI)
	if err := sdkgen.WriteSDKWithTools(sdkOutput, fixture.endpoints, sdkDir, fixedClock); err != nil {
		t.Fatalf("write the SDK: %v", err)
	}
	typesPackage := naming.Default().NpmTypesPackage(filesAPI)
	link(t, tree.typesDirs[typesPackage], filepath.Join(sdkDir, "node_modules", filepath.FromSlash(typesPackage)))

	tree.env = []string{"SDK_DIR=" + sdkDir}
	tree.runTest(t, "files_runtime.test.ts")
}
