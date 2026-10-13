package apigen_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
)

// generatePagedAPI is the API output of fixture-paged-api, whose
// listItems and listTags page (D60).
func generatePagedAPI(t *testing.T) *apigen.APIOutput {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-paged-api"))
	if err != nil {
		t.Fatalf("load fixture-paged-api: %v", err)
	}
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

// TestGoServerRefusesPages: until the Go server renders @paginated, its
// writers refuse a paged operation, naming it (D60).
func TestGoServerRefusesPages(t *testing.T) {
	output := generatePagedAPI(t)
	const want = "the Go server (apigen) does not render @paginated yet (D60): Catalog.listItems"
	if err := apigen.WriteAPI(output, t.TempDir()); err == nil || err.Error() != want {
		t.Fatalf("WriteAPI = %v, want %s", err, want)
	}
	if _, err := apigen.WriteScaffolds(output, t.TempDir()); err == nil || err.Error() != want {
		t.Fatalf("WriteScaffolds = %v, want %s", err, want)
	}
	if _, err := apigen.WriteImplementationScaffold(output, t.TempDir()); err == nil || err.Error() != want {
		t.Fatalf("WriteImplementationScaffold = %v, want %s", err, want)
	}
	if err := apigen.RefusePages("a generator", generateFixtureAPI(t)); err != nil {
		t.Fatalf("RefusePages of an API without a page = %v", err)
	}
}
