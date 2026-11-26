package providerhooks_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
)

// writeLocalAPI writes one of apigen's local test schemas with the test
// provider and returns routes.go.
func writeLocalAPI(t *testing.T, service string) string {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join("..", "testdata", "services", service))
	if err != nil {
		t.Fatalf("load %s: %v", service, err)
	}
	output, err := apigen.Generate(schema, apigen.Options{
		Provider:    provider{},
		SchemaName:  service,
		ModulePath:  "example.com/schemas/api/" + service,
		TypesModule: "example.com/schemas/types/go/" + service,
		Clock:       codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("generate %s: %v", service, err)
	}
	dir := t.TempDir()
	if err := apigen.WriteAPI(output, dir); err != nil {
		t.Fatalf("write %s: %v", service, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "routes.go"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// imports lists the import paths of routes.go.
func imports(t *testing.T, routes string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "routes.go", routes, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse routes.go: %v", err)
	}
	var paths []string
	for _, spec := range file.Imports {
		path, _ := strconv.Unquote(spec.Path.Value)
		paths = append(paths, path)
	}
	return paths
}

// TestRouteUploadBodyReplacesTheCoresReading: an upload route reads its
// body with the provider's snippet, not the core's block, and routes.go
// imports none of the packages only the core's block uses.
func TestRouteUploadBodyReplacesTheCoresReading(t *testing.T) {
	routes := writeLocalAPI(t, "raw-body-check-api")
	if !strings.Contains(routes, "// providerUploadBody reads NoteAttachPhotoHandler's upload its own way.") ||
		!strings.Contains(routes, "if !providerUpload(w, r, &input, &files) {") {
		t.Error("the upload route does not read its body with the provider's snippet")
	}
	for _, core := range []string{"CheckFileUploads(form", "RequestMediaType(r)", "ParseMultipartForm"} {
		if strings.Contains(routes, core) {
			t.Errorf("routes.go still holds the core's %s", core)
		}
	}
	for _, path := range imports(t, routes) {
		if path == "bytes" || path == "io" || path == "mime/multipart" {
			t.Errorf("routes.go imports %s, which only the core's block uses", path)
		}
	}
	// A JSON input route keeps the core's reading.
	if !strings.Contains(routes, "bodyargs.ReadInput(r.Body, input.JSONFieldNames())") {
		t.Error("the JSON input route lost the core's reading")
	}
}

// TestRouteQueryListReplacesTheCoresReading: each list in the query string,
// a @query parameter or an argument of a GET operation, is read by the
// provider's snippet, and the route builds no bodyargs.Arg for it.
func TestRouteQueryListReplacesTheCoresReading(t *testing.T) {
	routes := writeLocalAPI(t, "body-args-api")
	for _, name := range []string{"tags", "scores", "codes"} {
		if !strings.Contains(routes, "// providerQueryList reads "+name+" its own way.") {
			t.Errorf("the %s list is not read by the provider's snippet", name)
		}
	}
	for _, core := range []string{"bodyargs.QueryList[", "queryListTagsArg :=", "queryListCodesArg :="} {
		if strings.Contains(routes, core) {
			t.Errorf("routes.go still holds the core's %s", core)
		}
	}
	// Body arguments keep the core's reading.
	if !strings.Contains(routes, "bodyargs.ReadObject(r.Body)") {
		t.Error("the body argument routes lost the core's reading")
	}
}
