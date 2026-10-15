package apigen_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// generatePagedAPI is the API output of fixture-paged-api, whose
// listItems and listTags page (D60).
func generatePagedAPI(t *testing.T) *apigen.APIOutput {
	t.Helper()
	return generatePagedAPIOf(t, loadPagedAPI(t))
}

func loadPagedAPI(t *testing.T) *ir.Schema {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-paged-api"))
	if err != nil {
		t.Fatalf("load fixture-paged-api: %v", err)
	}
	return schema
}

// generatePagedAPIOf is generatePagedAPI of schema, fixture-paged-api as
// the caller loaded and edited it.
func generatePagedAPIOf(t *testing.T, schema *ir.Schema) *apigen.APIOutput {
	t.Helper()
	output, err := apigen.Generate(schema, apigen.Options{
		Provider:    sessionauth.Provider{},
		SchemaName:  "fixture-paged-api",
		ModulePath:  "example.com/schemas/api/fixture-paged-api",
		TypesModule: "example.com/schemas/types/go/fixture-paged-api",
		Clock:       codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	return output
}

// TestPagedEndpoints: a paged operation's endpoint carries its page, whose
// cursor and limit are not among its query parameters, and its result
// stays the list; every other endpoint has no page.
func TestPagedEndpoints(t *testing.T) {
	output := generatePagedAPI(t)
	pages := map[string]apigen.PageInfo{}
	for _, endpoint := range output.Endpoints {
		if endpoint.Page == nil {
			continue
		}
		pages[endpoint.Name] = *endpoint.Page
		if !endpoint.OutputIsArray {
			t.Errorf("%s: OutputIsArray = false", endpoint.Name)
		}
		for _, param := range append(endpoint.QueryParams, endpoint.ScalarArgs...) {
			if param.Name == "cursor" || param.Name == "limit" {
				t.Errorf("%s: the page's %s is an argument", endpoint.Name, param.Name)
			}
		}
	}
	want := map[string]apigen.PageInfo{
		"listItems": {DefaultLimit: 2, MaxLimit: 5, Operation: "Catalog.listItems"},
		"listTags":  {DefaultLimit: 20, MaxLimit: 100, Operation: "Catalog.listTags"},
	}
	if len(pages) != len(want) || pages["listItems"] != want["listItems"] || pages["listTags"] != want["listTags"] {
		t.Fatalf("pages = %+v, want %+v", pages, want)
	}
	if !output.HasPagedEndpoints() {
		t.Fatal("HasPagedEndpoints = false")
	}
}

// TestPagedOpenAPIGolden pins the OpenAPI document of fixture-paged-api: a
// paged operation's cursor and limit follow its own query parameters, with
// their bounds, and its data is the page, {items, next}, written inline.
// Regenerate with:
// go test ./internal/generator/apigen -run TestPagedOpenAPIGolden -update
func TestPagedOpenAPIGolden(t *testing.T) {
	got := []byte(generatePagedAPI(t).OpenAPISpecRaw)
	goldenPath := filepath.Join("testdata", "golden", "fixture-paged-api", "openapi.json")
	if *update {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("missing golden (run with -update): %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("openapi.json differs from %s; run with -update and review the diff\ngot:\n%s", goldenPath, got)
	}
}

// TestWriteAPIGoldenPaged pins the Go server of fixture-paged-api (D60): a
// paged operation's method takes page paging.Request last and returns
// paging.Page of its list's element, its route reads the page with
// paging.Read after its own query parameters and answers the page as the
// envelope's data, and its scaffolds return the empty page. Regenerate
// with:
// go test ./internal/generator/apigen -run TestWriteAPIGoldenPaged -update
func TestWriteAPIGoldenPaged(t *testing.T) {
	output := generatePagedAPI(t)
	outDir := t.TempDir()
	paths := naming.LocalPaths{
		ScalarGo:        "/repo/third_party/superscalar/go",
		SchemaIR:        "/repo/ir",
		SchemaRuntimeGo: "/repo/runtime/schema/go",
		HTTPRuntimeGo:   "/repo/runtime/http/go",
	}
	if err := apigen.SetReplacePaths(output, paths, "/repo/schemas/dist/api/fixture-paged-api"); err != nil {
		t.Fatalf("set replace paths: %v", err)
	}
	if err := apigen.WriteAPI(output, outDir); err != nil {
		t.Fatalf("write api: %v", err)
	}
	if _, err := apigen.WriteScaffolds(output, filepath.Join(outDir, "scaffolds")); err != nil {
		t.Fatalf("write scaffolds: %v", err)
	}
	if _, err := apigen.WriteImplementationScaffold(output, filepath.Join(outDir, "impl")); err != nil {
		t.Fatalf("write implementation scaffold: %v", err)
	}
	checkGoldenFiles(t, outDir, filepath.Join("testdata", "golden", "fixture-paged-api"), []string{
		"interfaces.go",
		"routes.go",
		"scaffolds/catalog/list_items.go",
		"scaffolds/catalog/list_tags.go",
		"impl/implementation.go",
	})
}

// TestPagedRoutesServePages generates the Go types and API modules of
// fixture-paged-api, with its scaffolds and its implementation scaffold
// inside the API module, copies testdata/paged_routes_test.go and the HTTP
// runtimes' page vectors into it, and runs go build, go vet and go test
// there: the routes page an in-memory catalog over httptest, first page,
// next, last page with a null next, filters on each page, the limit's
// bounds, and each refusal of a limit or cursor as page_parity.json words
// it; the scaffolds implement the interface and answer 501.
func TestPagedRoutesServePages(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	apiDir := writePagedAPIModule(t, loadPagedAPI(t))
	for _, file := range []struct{ from, to string }{
		{filepath.Join("testdata", "paged_routes_test.go"), "paged_routes_test.go"},
		{filepath.Join(testpaths.Local(t).HTTPRuntimeGo, "..", "testdata", "page_parity.json"), filepath.Join("testdata", "page_parity.json")},
	} {
		data, err := os.ReadFile(file.from)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(apiDir, file.to)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(apiDir, file.to), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGoAPIModule(t, apiDir)
}

// TestPagedFilterableRoutesCompile builds and vets fixture-paged-api with
// listItems also @filterable, its scaffolds and its implementation
// scaffold: the method takes the filters, then the page, and the route
// parses the filters before it reads the page, so the filters apply to
// each page.
func TestPagedFilterableRoutesCompile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	schema := loadPagedAPI(t)
	found := false
	for _, set := range schema.OperationSets {
		for _, op := range set.Operations {
			if op.Name == "listItems" && op.Page != nil {
				op.Filterable, found = true, true
			}
		}
	}
	if !found {
		t.Fatal("fixture-paged-api has no paged listItems")
	}
	apiDir := writePagedAPIModule(t, schema)
	routes, err := os.ReadFile(filepath.Join(apiDir, "routes.go"))
	if err != nil {
		t.Fatal(err)
	}
	filters := strings.Index(string(routes), "filters, filterErrors := filterparse.ParseFilters(r.URL.Query())")
	page := strings.Index(string(routes), "page, ok := paging.Read(w, r, paging.Spec{DefaultLimit: 2, MaxLimit: 5})")
	if filters < 0 || page < filters || !strings.Contains(string(routes), "impl.ListItems(r.Context(), Ids, InStock, filters, page)") {
		t.Errorf("routes.go does not parse the filters, then read the page, then pass both:\n%s", routes)
	}
	runGoAPIModule(t, apiDir)
}

// writePagedAPIModule writes the Go types and API modules of schema, a
// fixture-paged-api, with its scaffolds under scaffolds/ and its
// implementation scaffold under impl/ inside the API module, and returns
// the API module's directory.
func writePagedAPIModule(t *testing.T, schema *ir.Schema) string {
	t.Helper()
	apiDir := writeGoAPIModule(t, schema, "fixture-paged-api")
	output := generatePagedAPIOf(t, schema)
	if _, err := apigen.WriteScaffolds(output, filepath.Join(apiDir, "scaffolds")); err != nil {
		t.Fatal(err)
	}
	if _, err := apigen.WriteImplementationScaffold(output, filepath.Join(apiDir, "impl")); err != nil {
		t.Fatal(err)
	}
	return apiDir
}
