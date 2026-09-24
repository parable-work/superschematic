package apigen_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
)

func generateDocsFixtureAPI(t *testing.T) *apigen.APIOutput {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-docs"))
	if err != nil {
		t.Fatalf("load fixture-docs: %v", err)
	}
	output, err := apigen.Generate(schema, apigen.Options{
		Provider:    sessionauth.Provider{},
		SchemaName:  "fixture-docs",
		ModulePath:  "example.com/schemas/api/fixture-docs",
		TypesModule: "example.com/schemas/types/go/fixture-docs",
		Clock:       codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	return output
}

func openAPIOperation(t *testing.T, spec map[string]any, path, method string) map[string]any {
	t.Helper()
	item, ok := spec["paths"].(map[string]any)[path].(map[string]any)
	if !ok {
		t.Fatalf("no path %s", path)
	}
	op, ok := item[method].(map[string]any)
	if !ok {
		t.Fatalf("no %s %s", method, path)
	}
	return op
}

// TestOpenAPIUsesOperationDocs: an operation's @docs gives the summary and
// the description (over its comment), marks a deprecated or retired
// operation deprecated, and appears whole under the vendor key. An
// operation without @docs keeps its name and comment.
func TestOpenAPIUsesOperationDocs(t *testing.T) {
	output := generateDocsFixtureAPI(t)
	var spec map[string]any
	if err := json.Unmarshal([]byte(output.OpenAPISpecRaw), &spec); err != nil {
		t.Fatal(err)
	}

	get := openAPIOperation(t, spec, "/api/orders/{id}", "get")
	if get["summary"] != "Get an order" || get["description"] != "Returns one order by its identifier." {
		t.Errorf("getOrder summary/description = %q / %q", get["summary"], get["description"])
	}
	if _, ok := get["deprecated"]; ok {
		t.Error("an active operation is marked deprecated")
	}
	docs, ok := get[apigen.OpenAPIDocsKey].(map[string]any)
	if !ok {
		t.Fatalf("getOrder has no %s", apigen.OpenAPIDocsKey)
	}
	want := map[string]any{
		"title": "Get an order", "description": "Returns one order by its identifier.",
		"capability": "orders.get", "lifecycle": "active", "visibility": "public",
		"audience": "shoppers", "mappingStatus": "mapped",
	}
	if len(docs) != len(want) {
		t.Errorf("getOrder docs = %v, want %v", docs, want)
	}
	for key, value := range want {
		if docs[key] != value {
			t.Errorf("getOrder docs %s = %v, want %v", key, docs[key], value)
		}
	}

	list := openAPIOperation(t, spec, "/api/orders", "get")
	if list["summary"] != "listOrders" || list["description"] != "Lists every order. Without @docs the summary is the operation name." {
		t.Errorf("listOrders summary/description = %q / %q", list["summary"], list["description"])
	}
	if _, ok := list[apigen.OpenAPIDocsKey]; ok {
		t.Error("an operation without @docs carries the docs key")
	}

	open := openAPIOperation(t, spec, "/api/returns", "post")
	if open["deprecated"] != true {
		t.Error("a deprecated operation is not marked deprecated")
	}
	openDocs := open[apigen.OpenAPIDocsKey].(map[string]any)
	if openDocs["replacement"] != "orders.returns.create" || openDocs["sunset"] != "2027-01-31" || openDocs["mappingStatus"] != "uncertain" {
		t.Errorf("openReturn docs = %v", openDocs)
	}
	if _, ok := openDocs["audience"]; ok {
		t.Error("an undeclared audience is written")
	}
}

// TestOpenAPIDocsGolden pins the whole document for the docs fixture.
// Regenerate with: go test ./internal/generator/apigen -run TestOpenAPIDocsGolden -update
func TestOpenAPIDocsGolden(t *testing.T) {
	output := generateDocsFixtureAPI(t)
	golden := filepath.Join("testdata", "golden", "fixture-docs", "openapi.json")
	if *update {
		if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(output.OpenAPISpecRaw), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("missing golden (run with -update): %v", err)
	}
	if string(want) != output.OpenAPISpecRaw {
		t.Fatalf("OpenAPI document changed; run with -update and review the diff\ngot:\n%s", output.OpenAPISpecRaw)
	}
}
