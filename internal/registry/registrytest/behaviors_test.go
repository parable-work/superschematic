package registrytest_test

import (
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/jsonreader"
	"github.com/parable-work/superschematic/internal/loader/schemafile"
	"github.com/parable-work/superschematic/internal/loader/yamlreader"
	"github.com/parable-work/superschematic/internal/writer"
	ir "github.com/parable-work/superschematic/ir"
)

// wantItemBehaviors is Item's behavior list in the persisted IR: the
// config is canonical whatever key order the source used, and acme.Audited
// has none.
const wantItemBehaviors = `"behaviors": [
        {
          "name": "acme.Stock",
          "config": {
            "aisles": 3,
            "unit": "box"
          }
        },
        {
          "name": "acme.Audited"
        }
      ],`

// The fixture extension's behaviors reach the IR from both data forms, byte
// for byte the same though the YAML twin writes acme.Stock's config keys in
// another order; the generators refuse the schema, naming themselves; a
// registry without the extension refuses the behaviors by name.
func TestAcmeBehaviorsInTheDataForms(t *testing.T) {
	reg := acmeRegistry(t)
	jsonSchema, cfg, err := loader.LoadServiceWithConfig("testdata/stock-json", loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("LoadServiceWithConfig(stock-json): %v", err)
	}
	out := persisted(t, jsonSchema)
	if !strings.Contains(out, wantItemBehaviors) {
		t.Fatalf("persisted IR lacks\n%s\n%s", wantItemBehaviors, out)
	}
	if !strings.Contains(out, "\"role\": \"EmbeddedStruct\",\n      \"behaviors\": [") {
		t.Errorf("behaviors do not follow the type's role and heritage:\n%s", out)
	}
	yamlSchema, err := loader.LoadService("testdata/stock-yaml", loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("LoadService(stock-yaml): %v", err)
	}
	if yamlOut := strings.ReplaceAll(persisted(t, yamlSchema), ".schema.yaml", ".schema.json"); yamlOut != out {
		t.Errorf("JSON and YAML forms produced different IR\njson:\n%s\nyaml:\n%s", out, yamlOut)
	}

	_, err = generator.Run(jsonSchema, cfg, generator.Options{OutputRoot: t.TempDir(), ServicePath: "testdata/stock-json", Registry: reg})
	if want := "generator: types does not render behaviors yet: type Item composes behavior acme.Stock"; err == nil || err.Error() != want {
		t.Fatalf("generator.Run: err = %v, want %q", err, want)
	}

	n := naming.Default()
	n.AuthProvider = sessionauth.Name
	core := generator.CoreRegistry(n)
	for _, dir := range []string{"testdata/stock-json", "testdata/stock-yaml"} {
		_, err := loader.LoadService(dir, loader.WithRegistry(core))
		if err == nil || !strings.Contains(err.Error(), `behavior "acme.Stock" on type "Item" is not a registered behavior (none are registered)`) {
			t.Errorf("%s with the core registry: err = %v", dir, err)
		}
	}
}

// format converts a data-form file with behaviors between JSON and YAML,
// and each form reads back to the same document.
func TestAcmeBehaviorsConvertBetweenTheDataForms(t *testing.T) {
	reg := acmeRegistry(t)
	fromJSON, err := jsonreader.ReadFileWith("testdata/stock-json/src/item.schema.json", "item.schema.json", reg)
	if err != nil {
		t.Fatal(err)
	}
	asYAML, err := writer.Write(fromJSON, writer.FormatYAML)
	if err != nil {
		t.Fatal(err)
	}
	fromYAML, err := yamlreader.ReadWith(asYAML, "item.schema.yaml", reg)
	if err != nil {
		t.Fatalf("converted YAML does not read back: %v\n%s", err, asYAML)
	}
	asJSON, err := writer.Write(fromYAML, writer.FormatJSON)
	if err != nil {
		t.Fatal(err)
	}
	again, err := jsonreader.ReadWith(asJSON, "item.schema.json", reg)
	if err != nil {
		t.Fatalf("converted JSON does not read back: %v\n%s", err, asJSON)
	}
	want := persisted(t, &ir.Schema{Types: fromJSON.Types})
	for _, doc := range []*schemafile.Document{fromYAML, again} {
		if got := persisted(t, &ir.Schema{Types: doc.Types}); got != want {
			t.Errorf("document changed across the conversion\nwant:\n%s\ngot:\n%s", want, got)
		}
	}
	if _, err := writer.Write(fromJSON, writer.FormatTS); err == nil ||
		!strings.Contains(err.Error(), "type Item: behaviors (acme.Stock, acme.Audited) have no TypeScript authoring form in this writer yet") {
		t.Errorf("TypeScript writer: err = %v", err)
	}
}
