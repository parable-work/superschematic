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
	if handles["getProduct"] != "get_product/tag" || handles["createProduct"] != "create_product/box" ||
		handles["replaceVariants"] != "replace_variants/tag" || len(handles) != 3 {
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

// TestShopAPIToolsCarryAcmeKeys: the acme tool hook writes acme's vendor
// keys and icon variant into shop-api's TypeScript SDK tool documents; the
// core registry writes the core's.
func TestShopAPIToolsCarryAcmeKeys(t *testing.T) {
	reg, names := assemble(t)
	acmeOut := generateService(t, reg, names, "shop-api")
	coreNames := names
	coreNames.AuthProvider = "session"
	core, err := registry.Assemble(coreNames)
	if err != nil {
		t.Fatal(err)
	}
	coreOut := generateService(t, core, coreNames, "shop-api")

	tools := filepath.Join("sdk", "typescript", "shop-api", "tools")
	var manifest ir.ToolManifest
	readJSON(t, filepath.Join(coreOut, tools, "schema.json"), &manifest)
	get := manifestTool(t, manifest, "product.getProduct")
	if get.MCP == nil || get.MCP.Handle != "get_product" || get.MCP.Icon == nil || get.MCP.Icon.Family != "" {
		t.Fatalf("core getProduct mcp = %+v", get.MCP)
	}
	if get.Parameters.Properties["id"].CanonicalScalar != "Identity.UUID" {
		t.Fatalf("core getProduct id = %+v", get.Parameters.Properties["id"])
	}

	if want := (ir.MCPInvocation{Key: registry.DefaultToolInvocationKey, Value: registry.ToolInvocationAuto}); get.MCP.Invocation != want {
		t.Fatalf("core getProduct invocation = %+v, want %+v", get.MCP.Invocation, want)
	}

	schema, err := os.ReadFile(filepath.Join(acmeOut, tools, "schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"x-acme-scalar":"Identity.UUID"`,
		`"x-acme-arguments": 1,`,
		`"acme/operation-guidance":`,
		`"family": "acme"`,
		`"style": "outline"`,
		`"confirm": "never"`,
	} {
		if !strings.Contains(string(schema), want) {
			t.Errorf("acme schema.json lacks %s", want)
		}
	}
	for _, core := range []string{registry.DefaultToolScalarKey, registry.DefaultToolGuidanceKey, registry.DefaultToolInvocationKey} {
		if strings.Contains(string(schema), core) {
			t.Errorf("acme schema.json still carries %s", core)
		}
	}
	var audit struct {
		RegistryDigestInputs []struct {
			Handle string `json:"handle"`
			Hidden bool   `json:"hidden"`
			Icon   *struct {
				Family string `json:"family"`
			} `json:"icon"`
		} `json:"registryDigestInputs"`
	}
	readJSON(t, filepath.Join(acmeOut, tools, "mcp-audit.json"), &audit)
	visible := 0
	for _, entry := range audit.RegistryDigestInputs {
		if !entry.Hidden {
			visible++
			if entry.Icon == nil || entry.Icon.Family != ext.IconFamily {
				t.Errorf("audit entry %s icon = %+v", entry.Handle, entry.Icon)
			}
		}
	}
	if visible != 3 || len(audit.RegistryDigestInputs) != 4 {
		t.Fatalf("audit = %+v", audit)
	}
}

// returnsAPI is a test service that declares acme's confirm key on @mcp.
const returnsAPI = "testdata/services/returns-api"

// TestConfirmReplacesTheCoreInvocationPolicy: acme's confirm key is how a
// visible tool declares its invocation policy under acme. The TypeScript
// schema type-checks through acme's augmentation of the @mcp options, the
// IR carries confirm with its value or acme's default, and the core
// registry rejects the key.
func TestConfirmReplacesTheCoreInvocationPolicy(t *testing.T) {
	reg, names := assemble(t)
	schema, err := loader.LoadService(returnsAPI, loader.WithRegistry(reg), loader.WithNaming(names))
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	policies := map[string]ir.MCPInvocation{}
	for _, set := range schema.OperationSets {
		for _, op := range set.Operations {
			policies[op.Name] = op.MCP.Invocation
		}
	}
	want := map[string]ir.MCPInvocation{
		"openReturn":    {Key: ext.ConfirmKey, Value: ext.ConfirmNever},
		"approveReturn": {Key: ext.ConfirmKey, Value: ext.ConfirmAlways},
	}
	if len(policies) != len(want) || policies["openReturn"] != want["openReturn"] || policies["approveReturn"] != want["approveReturn"] {
		t.Fatalf("policies = %+v, want %+v", policies, want)
	}

	core, err := registry.Assemble(registry.DefaultNaming())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loader.LoadService(returnsAPI, loader.WithRegistry(core)); err == nil ||
		!strings.Contains(err.Error(), `@mcp config has unknown key "confirm"`) {
		t.Fatalf("core registry: err = %v, want the unknown confirm key", err)
	}
}

// TestConfirmInTheDataForms: the data forms take confirm with acme's
// values, and not the core key.
func TestConfirmInTheDataForms(t *testing.T) {
	reg, names := assemble(t)
	schema, err := loader.LoadService(writeToolsService(t, "notes-api", `{"handle": "get_note", "hidden": false, "confirm": "always"}`, "key"),
		loader.WithRegistry(reg), loader.WithNaming(names))
	if err != nil {
		t.Fatal(err)
	}
	if got := schema.OperationSets[0].Operations[0].MCP.Invocation; got != (ir.MCPInvocation{Key: ext.ConfirmKey, Value: ext.ConfirmAlways}) {
		t.Fatalf("invocation = %+v", got)
	}
	for mcp, want := range map[string]string{
		`{"handle": "get_note", "hidden": false, "confirm": "sometimes"}`:      "/confirm",
		`{"handle": "get_note", "hidden": false, "invocationPolicy": "ask"}`:   "additional properties 'invocationPolicy' not allowed",
		`{"hidden": true, "hiddenReason": "Staff only.", "confirm": "always"}`: "a hidden operation must not declare confirm",
	} {
		_, err := loader.LoadService(writeToolsService(t, "notes-api", mcp, ""), loader.WithRegistry(reg), loader.WithNaming(names))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", mcp, err, want)
		}
	}
}

// TestReturnsAPIToolsCarryConfirm: the TypeScript SDK tool documents write
// confirm where the core writes invocationPolicy, and tools/index.ts types
// it with acme's values.
func TestReturnsAPIToolsCarryConfirm(t *testing.T) {
	reg, names := assemble(t)
	schema, cfg, err := loader.LoadServiceWithConfig(returnsAPI, loader.WithRegistry(reg), loader.WithNaming(names))
	if err != nil {
		t.Fatalf("LoadServiceWithConfig: %v", err)
	}
	out := t.TempDir()
	if _, err := registry.Generate(schema, cfg, registry.Options{
		OutputRoot:  out,
		ServicePath: returnsAPI,
		Naming:      names,
		Registry:    reg,
		SkipFormat:  true,
	}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	tools := filepath.Join(out, "sdk", "typescript", "returns-api", "tools")

	var manifest ir.ToolManifest
	readJSON(t, filepath.Join(tools, "schema.json"), &manifest)
	if got := manifestTool(t, manifest, "return.approveReturn").MCP.Invocation; got != (ir.MCPInvocation{Key: ext.ConfirmKey, Value: ext.ConfirmAlways}) {
		t.Fatalf("approveReturn invocation = %+v", got)
	}
	for file, wants := range map[string][]string{
		"schema.json": {
			"\"description\": \"Approves an open return and refunds the order.\",\n        \"confirm\": \"always\",",
			"\"description\": \"Opens a return for one order.\",\n        \"confirm\": \"never\",",
		},
		"mcp-audit.json": {"\"hiddenReason\": \"\",\n      \"confirm\": \"always\","},
		"index.ts": {
			"description?: string;\n    confirm?: 'never' | 'always';",
			"description: `Approves an open return and refunds the order.`,\n      confirm: 'always',",
		},
	} {
		data, err := os.ReadFile(filepath.Join(tools, file))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range wants {
			if !strings.Contains(string(data), want) {
				t.Errorf("%s lacks %q", file, want)
			}
		}
		if strings.Contains(string(data), registry.DefaultToolInvocationKey) {
			t.Errorf("%s carries the core key %s", file, registry.DefaultToolInvocationKey)
		}
	}
}

func generateService(t *testing.T, reg *registry.Registry, names registry.Naming, service string) string {
	t.Helper()
	load := func(name string) (*ir.Schema, error) {
		return loader.LoadService(filepath.Join(schemasRoot, "services", name), loader.WithRegistry(reg), loader.WithNaming(names))
	}
	servicePath := filepath.Join(schemasRoot, "services", service)
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
	return out
}

func manifestTool(t *testing.T, manifest ir.ToolManifest, name string) ir.ToolManifestTool {
	t.Helper()
	for _, tool := range manifest.Tools {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("tool %s not found", name)
	return ir.ToolManifestTool{}
}
