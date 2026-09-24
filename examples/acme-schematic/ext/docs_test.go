package ext_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/loader"
	"github.com/parable-work/superschematic/registry"

	"example.com/acme/schematic/ext"
)

// TestShopAPIDocsLandUnderTheAcmeKey: the core @docs decorator fills the
// OpenAPI summary and description, and acme's OpenAPI hook moves the record
// from the core's vendor key to x-acme-docs.
func TestShopAPIDocsLandUnderTheAcmeKey(t *testing.T) {
	reg, names := assemble(t)
	load := func(name string) (*ir.Schema, error) {
		return loader.LoadService(filepath.Join(schemasRoot, "services", name), loader.WithRegistry(reg), loader.WithNaming(names))
	}
	servicePath := filepath.Join(schemasRoot, "services", "shop-api")
	schema, cfg, err := loader.LoadServiceWithConfig(servicePath, loader.WithRegistry(reg), loader.WithNaming(names))
	if err != nil {
		t.Fatalf("LoadServiceWithConfig: %v", err)
	}
	out := t.TempDir()
	if _, err := registry.Generate(schema, cfg, registry.Options{
		OutputRoot:     out,
		ServicePath:    servicePath,
		Naming:         names,
		Registry:       reg,
		LoadDependency: load,
		SkipFormat:     true,
	}); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	var spec struct {
		Paths map[string]map[string]map[string]any `json:"paths"`
	}
	readJSON(t, filepath.Join(out, "api", "shop-api", "openapi.json"), &spec)
	get := spec.Paths["/api/products/{id}"]["get"]
	if get["summary"] != "Get a product" || get["description"] != "Returns one product from the catalog." {
		t.Fatalf("getProduct summary/description = %q / %q", get["summary"], get["description"])
	}
	if _, ok := get[registry.OpenAPIDocsKey]; ok {
		t.Fatalf("the core key %s survived the acme hook", registry.OpenAPIDocsKey)
	}
	docs, ok := get[ext.DocsKey].(map[string]any)
	if !ok || docs["audience"] != "shoppers" || docs["capability"] != "catalog.products.get" {
		t.Fatalf("getProduct %s = %#v", ext.DocsKey, get[ext.DocsKey])
	}
	if _, ok := spec.Paths["/api/products"]["get"][ext.DocsKey]; ok {
		t.Fatal("listProducts has no @docs but carries a docs record")
	}
}

// writeDocsService writes a one-operation API service in the JSON data form
// whose @docs record has the given audience.
func writeDocsService(t *testing.T, audience string) string {
	t.Helper()
	service := filepath.Join(t.TempDir(), "services", "notes-api")
	if err := os.MkdirAll(filepath.Join(service, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"schema.config.json": `{"name": "notes-api", "kind": "API", "outputs": {}}`,
		"src/notes.schema.json": `{
			"name": "notes-api",
			"kind": "API",
			"types": {
				"Note": {"name": "Note", "role": "APIView", "fields": [{"name": "body", "typeRef": {"name": "string"}, "required": true}]}
			},
			"operationSets": [{
				"name": "NoteQueries",
				"operations": [{
					"name": "getNote", "httpMethod": "GET", "restPath": "note",
					"typeRef": {"name": "Note"}, "required": true,
					"docs": {
						"title": "Get a note", "description": "Returns the note.",
						"capability": "notes.get", "lifecycle": "active", "visibility": "internal",
						"audience": "` + audience + `", "mappingStatus": "mapped"
					}
				}]
			}]
		}`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(service, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return service
}

// TestDocsAudienceOutsideTheAcmeSetFailsTheLoad: the audience rule is acme's
// check, registered without a core edit. The same schema loads with the
// core registry, which has no audience vocabulary, and with an acme audience.
func TestDocsAudienceOutsideTheAcmeSetFailsTheLoad(t *testing.T) {
	reg, names := assemble(t)

	_, err := loader.LoadService(writeDocsService(t, "robots"), loader.WithRegistry(reg), loader.WithNaming(names))
	if err == nil || !strings.Contains(err.Error(), `NoteQueries.getNote: @docs audience "robots" is not an acme audience (shoppers, staff)`) {
		t.Fatalf("err = %v, want the acme audience check", err)
	}

	if _, err := loader.LoadService(writeDocsService(t, "staff"), loader.WithRegistry(reg), loader.WithNaming(names)); err != nil {
		t.Fatalf("an acme audience failed to load: %v", err)
	}

	core, err := registry.Assemble(registry.DefaultNaming())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loader.LoadService(writeDocsService(t, "robots"), loader.WithRegistry(core)); err != nil {
		t.Fatalf("the core registry applied an audience rule: %v", err)
	}
}
