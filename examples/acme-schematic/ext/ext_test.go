package ext_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/loader"
	"github.com/parable-work/superschematic/registry"

	"example.com/acme/schematic/ext"
)

// schemasRoot is the example's schemas root, relative to this package.
const schemasRoot = "../schemas"

func assemble(t *testing.T) (*registry.Registry, registry.Naming) {
	t.Helper()
	names, err := registry.LoadNaming(schemasRoot)
	if err != nil {
		t.Fatalf("LoadNaming: %v", err)
	}
	reg, err := registry.Assemble(names, ext.Extension{})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	return reg, names
}

func TestExtensionRegistersEverySurface(t *testing.T) {
	reg, names := assemble(t)

	// The core is not an extension; only the ones passed to Assemble list.
	if got := reg.Extensions(); strings.Join(got, ",") != "acme" {
		t.Fatalf("Extensions = %v, want acme", got)
	}
	if got := reg.Kinds(); strings.Join(got, ",") != "API,Catalog,DB,General" {
		t.Fatalf("Kinds = %v, want the three core kinds plus Catalog", got)
	}

	var pipeline []string
	for _, gen := range reg.Pipeline(ext.Kind) {
		pipeline = append(pipeline, gen.Name)
	}
	if strings.Join(pipeline, ",") != "types,catalog,acmeManifest" {
		t.Fatalf("Catalog pipeline = %v, want types, catalog, then the manifest appended", pipeline)
	}
	for _, kind := range []string{"DB", "API", "General"} {
		last := reg.Pipeline(kind)
		if len(last) == 0 || last[len(last)-1].Name != "acmeManifest" {
			t.Fatalf("%s pipeline = %v, want acmeManifest appended", kind, last)
		}
	}

	var docs []string
	for _, doc := range reg.Documents() {
		docs = append(docs, doc.Name)
	}
	if strings.Join(docs, ",") != ext.DocumentName {
		t.Fatalf("Documents = %v, want only %s", docs, ext.DocumentName)
	}
	if got := reg.AuthProviders(); strings.Join(got, ",") != "apikey,session" {
		t.Fatalf("AuthProviders = %v, want apikey and session", got)
	}
	if names.AuthProvider != "apikey" {
		t.Fatalf("naming selects %q, want apikey", names.AuthProvider)
	}
	var hooks []string
	for _, hook := range reg.BuildAllHooks() {
		hooks = append(hooks, hook.Name)
	}
	if strings.Join(hooks, ",") != "acmeInventory" {
		t.Fatalf("BuildAllHooks = %v, want acmeInventory", hooks)
	}
}

// TestInventoryHookReadsOutputDirs runs the hook the way build-all does after
// every service came from the cache: no IR for any service, only their
// output directories.
func TestInventoryHookReadsOutputDirs(t *testing.T) {
	reg, names := assemble(t)
	hooks := reg.BuildAllHooks()
	if len(hooks) != 1 {
		t.Fatalf("BuildAllHooks = %v, want one", hooks)
	}
	out := t.TempDir()
	var services []registry.BuildAllService
	for _, m := range []ext.Manifest{
		{Service: "shop-db", Kind: "DB", Region: "eu", Types: []string{"Product"}},
		{Service: "shop-catalog", Kind: ext.Kind, Region: "eu", Types: []string{"Bundle", "Product"}},
	} {
		dir := ext.ManifestDir(out, m.Service)
		writeJSON(t, filepath.Join(dir, "manifest.json"), m)
		services = append(services, registry.BuildAllService{
			Name:       m.Service,
			Kind:       m.Kind,
			OutputDirs: []string{filepath.Join(out, "types", "go", m.Service), dir},
		})
	}
	bc := registry.BuildAllContext{
		Services:   services,
		SchemaFor:  func(string) (*ir.Schema, bool) { return nil, false },
		OutputRoot: out,
		Naming:     names,
		Log:        new(bytes.Buffer),
	}
	if err := hooks[0].Run(context.Background(), bc); err != nil {
		t.Fatalf("acmeInventory: %v", err)
	}
	var inventory ext.Inventory
	readJSON(t, ext.InventoryPath(out), &inventory)
	if len(inventory.Services) != 2 || inventory.Services[0].Service != "shop-db" || inventory.Services[1].Kind != ext.Kind {
		t.Fatalf("inventory = %+v, want shop-db then shop-catalog", inventory)
	}

	// A service whose manifest is missing from its output fails the hook
	// instead of dropping out of the inventory.
	if err := os.Remove(filepath.Join(ext.ManifestDir(out, "shop-catalog"), "manifest.json")); err != nil {
		t.Fatal(err)
	}
	err := hooks[0].Run(context.Background(), bc)
	if err == nil || !strings.Contains(err.Error(), "shop-catalog") {
		t.Fatalf("acmeInventory with a missing manifest: err = %v, want shop-catalog named", err)
	}
}

func TestCoreRegistryHasNoAcmeSurface(t *testing.T) {
	reg, err := registry.Assemble(registry.DefaultNaming())
	if err != nil {
		t.Fatalf("Assemble core: %v", err)
	}
	if got := reg.Kinds(); strings.Join(got, ",") != "API,DB,General" {
		t.Fatalf("core Kinds = %v, want no Catalog", got)
	}
	if len(reg.Documents()) != 0 {
		t.Fatalf("core Documents = %v, want none", reg.Documents())
	}
	if got := reg.AuthProviders(); strings.Join(got, ",") != "session" {
		t.Fatalf("core AuthProviders = %v, want session only", got)
	}
}

func TestUnknownExtensionConfigKeyFailsAssembly(t *testing.T) {
	names, err := registry.ParseNaming([]byte("[extension.acme]\nregion = \"eu\"\nshelves = 3\n"), "superschematic.toml")
	if err != nil {
		t.Fatalf("ParseNaming: %v", err)
	}
	_, err = registry.Assemble(names, ext.Extension{})
	if err == nil || !strings.Contains(err.Error(), `unknown key "shelves"`) {
		t.Fatalf("Assemble err = %v, want the unknown key named", err)
	}
}

func TestCatalogServiceLoadsAndGenerates(t *testing.T) {
	reg, names := assemble(t)
	servicePath := filepath.Join(schemasRoot, "services", "shop-catalog")

	schema, cfg, err := loader.LoadServiceWithConfig(servicePath, loader.WithRegistry(reg), loader.WithNaming(names))
	if err != nil {
		t.Fatalf("LoadServiceWithConfig: %v", err)
	}
	if string(schema.Kind) != ext.Kind {
		t.Fatalf("Kind = %q, want %s", schema.Kind, ext.Kind)
	}

	shelf, ok, err := ext.ShelfOf(field(t, schema, "Product", "sku"))
	if err != nil || !ok {
		t.Fatalf("ShelfOf(Product.sku) = %v, %v, %v; want the @shelf payload", shelf, ok, err)
	}
	if shelf.Aisle != 3 || shelf.Bay != "B" {
		t.Fatalf("Product.sku shelf = %+v, want aisle 3 bay B", shelf)
	}
	if _, ok, _ := ext.ShelfOf(field(t, schema, "Product", "name")); ok {
		t.Fatal("Product.name carries a shelf it never declared")
	}

	raw, ok := schema.Documents[ext.DocumentName]
	if !ok {
		t.Fatalf("Documents = %v, want %s loaded from the sidecar", schema.Documents, ext.DocumentName)
	}
	var doc ext.CatalogConfig
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode %s: %v", ext.DocumentName, err)
	}
	if doc.Region != "eu" || doc.Currency != "EUR" || doc.Aisles != 16 {
		t.Fatalf("catalog.config = %+v, want the sidecar's values", doc)
	}

	out := t.TempDir()
	var log bytes.Buffer
	result, err := registry.Generate(schema, cfg, registry.Options{
		OutputRoot:  out,
		ServicePath: servicePath,
		Naming:      names,
		Registry:    reg,
		Log:         &log,
	})
	if err != nil {
		t.Fatalf("Generate: %v\n%s", err, log.String())
	}
	for _, key := range []string{"catalog", ext.DocumentName, "acmeManifest"} {
		if _, ok := result.Outputs[key]; !ok {
			t.Fatalf("Outputs = %v, want %q", result.Outputs, key)
		}
	}

	var catalog ext.Catalog
	readJSON(t, filepath.Join(ext.CatalogDir(out, "shop-catalog"), "catalog.json"), &catalog)
	if len(catalog.Shelves) != 3 || catalog.Shelves["Bundle.code"].Aisle != 12 {
		t.Fatalf("catalog.json = %+v, want the three shelved fields", catalog)
	}
	var config ext.CatalogConfig
	readJSON(t, filepath.Join(ext.CatalogDir(out, "shop-catalog"), "config.json"), &config)
	if config != doc {
		t.Fatalf("config.json = %+v, want %+v", config, doc)
	}
	var manifest ext.Manifest
	readJSON(t, filepath.Join(ext.ManifestDir(out, "shop-catalog"), "manifest.json"), &manifest)
	if manifest.Kind != ext.Kind || manifest.Region != "eu" || strings.Join(manifest.Types, ",") != "Bundle,Product" {
		t.Fatalf("manifest.json = %+v, want Catalog kind, region eu, both types", manifest)
	}
}

func TestShelfOnNonCatalogSchemaIsRejected(t *testing.T) {
	reg, names := assemble(t)
	root := t.TempDir()
	service := filepath.Join(root, "services", "bad-db")
	if err := os.MkdirAll(filepath.Join(service, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"schema.config.json": `{"name": "bad-db", "kind": "DB", "outputs": {}}`,
		"src/bad.schema.json": `{
			"name": "bad-db",
			"kind": "DB",
			"types": {
				"Thing": {
					"name": "Thing",
					"role": "DBTable",
					"fields": [{"name": "id", "typeRef": {"name": "Identity.UUID"}, "required": true, "extensions": {"acme": {"shelf": {"aisle": 1}}}}]
				}
			}
		}`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(service, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, err := loader.LoadService(service, loader.WithRegistry(reg), loader.WithNaming(names))
	if err == nil {
		t.Fatal("a DB schema carrying @shelf loaded; the decorator is Catalog-only")
	}
	if !strings.Contains(err.Error(), "shelf") {
		t.Fatalf("err = %v, want it to name the decorator", err)
	}
}

func field(t *testing.T, schema *ir.Schema, typeName, fieldName string) *ir.FieldDef {
	t.Helper()
	td, ok := schema.Types[typeName]
	if !ok {
		t.Fatalf("type %s missing from the IR", typeName)
	}
	for _, fd := range td.Fields {
		if fd.Name == fieldName {
			return fd
		}
	}
	t.Fatalf("%s.%s missing from the IR", typeName, fieldName)
	return nil
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}

// TestDeclaredFieldsChecksLabelDeclarations runs the fields command's
// program over the example's labels: a relative import between declaration
// files resolves, and each field carries the compiler's type.
func TestDeclaredFieldsChecksLabelDeclarations(t *testing.T) {
	fields, err := ext.DeclaredFields("../labels", "shelf-label.d.ts", "ShelfLabel")
	if err != nil {
		t.Fatalf("DeclaredFields: %v", err)
	}
	var got []string
	for _, field := range fields {
		got = append(got, field.Name+": "+field.Type)
	}
	want := "sku: string, price: number, currency: Currency, location: Location, promo: string | undefined"
	if strings.Join(got, ", ") != want {
		t.Fatalf("fields = %s, want %s", strings.Join(got, ", "), want)
	}
	if _, err := ext.DeclaredFields("../labels", "shelf-label.d.ts", "Shelf"); err == nil || !strings.Contains(err.Error(), "declares no interface or type alias Shelf") {
		t.Fatalf("unknown type: err = %v", err)
	}
}

func TestDeclaredFieldsReportsLocatedDiagnostics(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.d.ts"), []byte("export interface Bad { price: Money; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ext.DeclaredFields(dir, "bad.d.ts", "Bad")
	if err == nil || !strings.HasPrefix(err.Error(), "bad.d.ts:1:31: ") || !strings.Contains(err.Error(), "Money") {
		t.Fatalf("err = %v, want the unknown name located at bad.d.ts:1:31", err)
	}
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}
