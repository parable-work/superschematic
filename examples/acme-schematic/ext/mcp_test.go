package ext_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/loader"
	"github.com/parable-work/superschematic/registry"
)

// writeToolsService writes a one-operation API service named name in the
// JSON data form. mcp is the operation's "mcp" member, or "" for none;
// icon its @icon, or "" for none.
func writeToolsService(t *testing.T, name, mcp, icon string) string {
	t.Helper()
	service := filepath.Join(t.TempDir(), "services", name)
	if err := os.MkdirAll(filepath.Join(service, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	operation := `"name": "getNote", "httpMethod": "GET", "restPath": "note",
		"typeRef": {"name": "Note"}, "required": true,
		"docs": {"title": "Get a note", "description": "Returns the note.", "capability": "notes.get",
			"lifecycle": "active", "visibility": "internal", "audience": "staff", "mappingStatus": "mapped"}`
	if mcp != "" {
		operation += `, "mcp": ` + mcp
	}
	if icon != "" {
		operation += `, "icon": "` + icon + `"`
	}
	files := map[string]string{
		"schema.config.json": `{"name": "` + name + `", "kind": "API", "outputs": {}}`,
		"src/notes.schema.json": `{
			"name": "` + name + `",
			"kind": "API",
			"types": {
				"Note": {"name": "Note", "role": "APIView", "fields": [{"name": "body", "typeRef": {"name": "string"}, "required": true}]}
			},
			"operationSets": [{"name": "NoteQueries", "operations": [{` + operation + `}]}]
		}`,
	}
	for file, body := range files {
		if err := os.WriteFile(filepath.Join(service, file), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return service
}

// TestShopAPIOperationsAreClassified: every operation of acme's shop API
// declares @mcp; a visible tool carries its handle and icon.
func TestShopAPIOperationsAreClassified(t *testing.T) {
	reg, names := assemble(t)
	schema, err := loader.LoadService(filepath.Join(schemasRoot, "services", "shop-api"), loader.WithRegistry(reg), loader.WithNaming(names))
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	handles := map[string]string{}
	for _, set := range schema.OperationSets {
		for _, op := range set.Operations {
			if op.MCP == nil {
				t.Fatalf("%s.%s has no @mcp", set.Name, op.Name)
			}
			if !op.MCP.Hidden {
				handles[op.Name] = op.MCP.Handle + "/" + op.Icon
			}
		}
	}
	if handles["getProduct"] != "get_product/tag" || handles["createProduct"] != "create_product/box" || len(handles) != 2 {
		t.Fatalf("visible tools = %v", handles)
	}
}

// TestUnclassifiedShopOperationFailsTheLoad: the classification rule is
// acme's check on the core @mcp decorator, for the shop API only. Another
// API, and the core registry, accept an operation without @mcp.
func TestUnclassifiedShopOperationFailsTheLoad(t *testing.T) {
	reg, names := assemble(t)
	_, err := loader.LoadService(writeToolsService(t, "shop-api", "", ""), loader.WithRegistry(reg), loader.WithNaming(names))
	if err == nil || !strings.Contains(err.Error(), "NoteQueries.getNote: every operation of shop-api declares @mcp, visible or hidden") {
		t.Fatalf("err = %v, want the acme classification check", err)
	}
	if _, err := loader.LoadService(writeToolsService(t, "shop-api", `{"handle": "get_note", "hidden": false}`, "key"), loader.WithRegistry(reg), loader.WithNaming(names)); err != nil {
		t.Fatalf("a classified operation failed to load: %v", err)
	}
	if _, err := loader.LoadService(writeToolsService(t, "notes-api", "", ""), loader.WithRegistry(reg), loader.WithNaming(names)); err != nil {
		t.Fatalf("the check applied outside the shop API: %v", err)
	}
	core, err := registry.Assemble(registry.DefaultNaming())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loader.LoadService(writeToolsService(t, "shop-api", "", ""), loader.WithRegistry(core)); err != nil {
		t.Fatalf("the core registry required @mcp: %v", err)
	}
}

// TestOperationIconOutsideTheAcmeSetFailsTheLoad: acme's icon set covers
// the operation @icon too.
func TestOperationIconOutsideTheAcmeSetFailsTheLoad(t *testing.T) {
	reg, names := assemble(t)
	_, err := loader.LoadService(writeToolsService(t, "notes-api", `{"handle": "get_note", "hidden": false}`, "rocket"), loader.WithRegistry(reg), loader.WithNaming(names))
	if err == nil || !strings.Contains(err.Error(), `NoteQueries.getNote: @icon "rocket" is not in the acme icon set`) {
		t.Fatalf("err = %v, want the acme icon check", err)
	}
}
