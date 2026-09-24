package registrytest_test

import (
	"encoding/json"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemafile"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/registry/registrytest"
	ir "github.com/parable-work/superschematic/ir"
)

// acmeRegistry is what an extension binary's cli.New would assemble: the
// core kinds and decorators (registry.New), the core generators
// (generator.RegisterCore), the extension (Use), then Finalize.
func acmeRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	// The default naming with the core's own auth provider selected: the
	// in-tree default names the Acme provider, which only the Acme
	// extension registers, and Acme registers none.
	n := naming.Default()
	n.AuthProvider = sessionauth.Name
	reg := registry.New(n)
	if err := generator.RegisterCore(reg); err != nil {
		t.Fatal(err)
	}
	if err := reg.Use(registrytest.Acme{}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Finalize(); err != nil {
		t.Fatal(err)
	}
	return reg
}

func persisted(t *testing.T, schema *ir.Schema) string {
	t.Helper()
	b, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The in-tree acceptance test (extension-model.md, section 10): an extension
// registers a kind, decorators, a document and a generator through Use and
// nothing in the engine is edited. The TS fixture loads through the real
// loader, the decorators' Apply lands values in the IR extension slots, the
// persisted IR carries them canonically, the data-form twin produces the
// same IR, and generator.Run emits the extension's file.
func TestAcmeExtensionEndToEnd(t *testing.T) {
	reg := acmeRegistry(t)

	schema, cfg, err := loader.LoadServiceWithConfig("testdata/shop", loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("LoadServiceWithConfig(shop): %v", err)
	}
	if schema.Kind != "Catalog" || cfg.Kind != "Catalog" {
		t.Fatalf("kind = %q / %q, want Catalog", schema.Kind, cfg.Kind)
	}

	product := schema.Types["Product"]
	if product == nil || product.Role != ir.RoleEmbeddedStruct {
		t.Fatalf("Product = %+v, want an EmbeddedStruct (KindSpec.StructRole)", product)
	}
	sku := product.Fields[0]
	if got := string(sku.Extensions["acme"]); got != `{"shelf":{"aisle":3}}` {
		t.Errorf("FieldDef.Extensions[acme] = %s", got)
	}
	shelf, ok, err := ir.GetExtension[registrytest.FieldExt](sku, "acme")
	if err != nil || !ok || shelf.Shelf == nil || shelf.Shelf.Aisle != 3 {
		t.Errorf("GetExtension[FieldExt] = %+v ok=%v err=%v", shelf, ok, err)
	}
	if got := string(product.Extensions["acme"]); got != `{"meta":{"region":"eu","tiers":[1,2]},"tagged":true}` {
		t.Errorf("TypeDef.Extensions[acme] = %s", got)
	}
	if len(product.Fields[1].Extensions) != 0 {
		t.Errorf("undecorated field carries extensions: %v", product.Fields[1].Extensions)
	}

	// The persisted IR carries the slots in canonical form and round-trips.
	out := persisted(t, schema)
	for _, want := range []string{
		"\"extensions\": {\n            \"acme\": {\n              \"shelf\": {\n                \"aisle\": 3\n              }\n            }\n          }",
		`"kind": "Catalog"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("persisted IR lacks %q:\n%s", want, out)
		}
	}
	var back ir.Schema
	if err := json.Unmarshal([]byte(out), &back); err != nil {
		t.Fatal(err)
	}
	if persisted(t, &back) != out {
		t.Error("persisted IR does not round-trip byte for byte")
	}

	// The data form: the same schema as YAML, extension decorators under the
	// extensions slots, through the same loader and registry. Owners name the
	// source file, so they are normalized the way the loader's own format
	// parity tests do; every other byte is identical.
	yamlSchema, yamlCfg, err := loader.LoadServiceWithConfig("testdata/shop-yaml", loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("LoadServiceWithConfig(shop-yaml): %v", err)
	}
	if yamlCfg.Kind != cfg.Kind || yamlCfg.Name != cfg.Name {
		t.Errorf("yaml config = %+v, ts config = %+v", yamlCfg, cfg)
	}
	yamlOut := strings.ReplaceAll(persisted(t, yamlSchema), ".schema.yaml", ".schema.ts")
	if yamlOut != out {
		t.Errorf("YAML and TS forms produced different IR\nts:\n%s\nyaml:\n%s", out, yamlOut)
	}

	// generator.Run dispatches the Catalog pipeline to the extension's
	// generator, which reads the extension slots back out of the IR.
	outputRoot := t.TempDir()
	result, err := generator.Run(schema, cfg, generator.Options{
		OutputRoot:  outputRoot,
		ServicePath: "testdata/shop",
		Registry:    reg,
	})
	if err != nil {
		t.Fatalf("generator.Run: %v", err)
	}
	if got := result.Outputs["catalog"]; got != registrytest.OutDir(outputRoot, "shop") {
		t.Errorf("Result.Outputs[catalog] = %q", got)
	}
	catalog, err := os.ReadFile(filepath.Join(registrytest.OutDir(outputRoot, "shop"), "catalog.json"))
	if err != nil {
		t.Fatalf("catalog.json: %v", err)
	}
	wantCatalog := "{\n  \"service\": \"shop\",\n  \"shelves\": {\n    \"Product.sku\": {\n      \"aisle\": 3\n    }\n  },\n  \"tagged\": [\n    \"Product\"\n  ]\n}\n"
	if string(catalog) != wantCatalog {
		t.Errorf("catalog.json:\n%s\nwant:\n%s", catalog, wantCatalog)
	}

	// The YAML twin's config is a data form too, and it switches the
	// extension generator on with the extension's own output key.
	yamlRoot := t.TempDir()
	if _, err := generator.Run(yamlSchema, yamlCfg, generator.Options{
		OutputRoot:  yamlRoot,
		ServicePath: "testdata/shop-yaml",
		Registry:    reg,
	}); err != nil {
		t.Fatalf("generator.Run(shop-yaml): %v", err)
	}
	yamlCatalog, err := os.ReadFile(filepath.Join(registrytest.OutDir(yamlRoot, "shop"), "catalog.json"))
	if err != nil {
		t.Fatalf("catalog.json from the YAML twin: %v", err)
	}
	if string(yamlCatalog) != wantCatalog {
		t.Errorf("catalog.json from the YAML twin:\n%s\nwant:\n%s", yamlCatalog, wantCatalog)
	}

	// The emitted JSON Schema composes the extension in.
	def, err := schemafile.DefinitionFor(reg)
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		Defs map[string]struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(def, &root); err != nil {
		t.Fatal(err)
	}
	var fieldExt struct {
		Properties map[string]struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(root.Defs["FieldDef"].Properties["extensions"], &fieldExt); err != nil {
		t.Fatal(err)
	}
	var gotShelf, wantShelf any
	_ = json.Unmarshal(fieldExt.Properties["acme"].Properties["shelf"], &gotShelf)
	_ = json.Unmarshal(registrytest.ShelfArgs, &wantShelf)
	gotJSON, _ := json.Marshal(gotShelf)
	wantJSON, _ := json.Marshal(wantShelf)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("$defs.FieldDef.properties.extensions.properties.acme.properties.shelf = %s, want %s", gotJSON, wantJSON)
	}
	if !strings.Contains(string(root.Defs["Document"].Properties["kind"]), `"Catalog"`) {
		t.Errorf("Document.kind enum lacks Catalog: %s", root.Defs["Document"].Properties["kind"])
	}
}

// outputs.catalog is checked against the catalog generator's OutputSchema
// before any generator runs, from either config form.
func TestAcmeOutputSectionFailsItsOutputSchema(t *testing.T) {
	reg := acmeRegistry(t)
	schema, cfg, err := loader.LoadServiceWithConfig("testdata/shop-yaml", loader.WithRegistry(reg))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Outputs = map[string]any{"catalog": map[string]any{"enabled": true, "shelves": 3}}
	_, err = generator.Run(schema, cfg, generator.Options{OutputRoot: t.TempDir(), ServicePath: "testdata/shop-yaml", Registry: reg})
	if err == nil || !strings.Contains(err.Error(), "outputs.catalog") || !strings.Contains(err.Error(), "shelves") {
		t.Fatalf("a catalog section with an undeclared key: %v", err)
	}
}

// A decorator restricted to the extension's kind is rejected elsewhere with
// the registry's KindError, located at the decorator.
func TestAcmeDecoratorOutsideItsKind(t *testing.T) {
	reg := acmeRegistry(t)
	_, _, err := loader.LoadServiceWithConfig("testdata/broken-shop-db", loader.WithRegistry(reg))
	if err == nil {
		t.Fatal("DB schema using @shelf loaded")
	}
	want := "src/product.schema.ts:6:3: @shelf is only allowed in Catalog schemas (this service is kind DB)"
	if !strings.HasSuffix(strings.TrimSpace(err.Error()), want) {
		t.Errorf("error = %q\nwant suffix %q", err.Error(), want)
	}
}

// Without the extension the core registry knows none of this: the TS form
// fails on the kind and the data form on the extension name.
func TestAcmeFixturesNeedTheExtension(t *testing.T) {
	_, _, err := loader.LoadServiceWithConfig("testdata/shop")
	if err == nil || !strings.Contains(err.Error(), `schema config for shop has unknown kind "Catalog"`) {
		t.Errorf("TS fixture against core: %v", err)
	}
	_, _, err = loader.LoadServiceWithConfig("testdata/shop-yaml")
	if err == nil || !strings.Contains(err.Error(), `unknown kind "Catalog"`) {
		t.Errorf("YAML fixture against core: %v", err)
	}
}
