package registrytest_test

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/registry/registrytest"
	"github.com/parable-work/superschematic/internal/writer"
	ir "github.com/parable-work/superschematic/ir"
)

// referencesRegistry is acmeRegistry with the Admits fixture, whose
// @admits takes its `from` handles as identities.
func referencesRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	n := naming.Default()
	n.AuthProvider = sessionauth.Name
	reg := registry.New(n)
	if err := generator.RegisterCore(reg); err != nil {
		t.Fatal(err)
	}
	if err := reg.Use(registrytest.Acme{}, registrytest.Admits{}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Finalize(); err != nil {
		t.Fatal(err)
	}
	return reg
}

const referencesConfig = `{"name": "shop", "kind": "General", "outputs": {}}`

// referencesSource names services by handle in two decorators: @meta's
// argument references depot through its imported sentinel, ledger through a
// service({...}) written in place and the shop itself, which is no edge;
// @admits's `from` only names orders.
const referencesSource = `import { admits, meta } from "@acme/schematic";
import { SchemaKind, service } from "@superschematic/schema-config";
import { Depot } from "@schemas/depot";
import { Orders } from "@schemas/orders";
import { Shop } from "./service.generated";

@meta({ region: "eu", storedIn: [Depot], ledger: service({ name: "ledger", kind: SchemaKind.DB }), self: Shop })
@admits({ from: [Orders] })
export abstract class Product {
  name: string;
}
`

// referencesDocuments are the data forms of referencesSource: each handle is
// {name, kind}, and the references are stated, as imports are.
var referencesDocuments = map[string]string{
	"src/product.schema.json": `{
  "references": [{ "name": "ledger", "kind": "DB" }, { "name": "depot", "kind": "General" }],
  "types": {
    "Product": {
      "name": "Product",
      "role": "EmbeddedStruct",
      "extensions": {
        "acme": {
          "meta": {
            "region": "eu",
            "storedIn": [{ "name": "depot", "kind": "General" }],
            "ledger": { "name": "ledger", "kind": "DB" },
            "self": { "name": "shop", "kind": "General" }
          }
        },
        "admits": { "admits": { "from": [{ "name": "orders", "kind": "API" }] } }
      },
      "fields": [{ "name": "name", "typeRef": { "name": "string" }, "required": true }]
    }
  }
}`,
	"src/product.schema.yaml": `references:
  - { name: depot, kind: General }
  - { name: ledger, kind: DB }
types:
  Product:
    name: Product
    role: EmbeddedStruct
    extensions:
      acme:
        meta:
          region: eu
          storedIn: [{ name: depot, kind: General }]
          ledger: { name: ledger, kind: DB }
          self: { name: shop, kind: General }
      admits:
        admits: { from: [{ name: orders, kind: API }] }
    fields:
      - name: name
        typeRef: { name: string }
        required: true
`,
}

// referencesService writes the shop service with referencesSource, the
// depot and orders packages beside it, each exporting only its sentinel,
// and the shop's own sentinel. It returns the shop's directory and the
// orders sentinel's path.
func referencesService(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "shop")
	tsProject(t, dir)
	sentinel := func(name, kind, constName string) []byte {
		return []byte(`import { SchemaKind, service } from "@superschematic/schema-config";
export const ` + constName + ` = service({ name: "` + name + `", kind: SchemaKind.` + kind + ` });
`)
	}
	for _, sibling := range []struct{ name, kind, constName string }{
		{"depot", "General", "Depot"},
		{"orders", "API", "Orders"},
	} {
		siblingDir := filepath.Join(root, sibling.name)
		writeFiles(t, siblingDir, map[string][]byte{
			"package.json":             []byte(`{"name": "@schemas/` + sibling.name + `", "private": true}`),
			"src/index.ts":             []byte("export * from \"./service.generated\";\n"),
			"src/service.generated.ts": sentinel(sibling.name, sibling.kind, sibling.constName),
		})
		addTSPath(t, dir, "@schemas/"+sibling.name, filepath.Join(siblingDir, "src", "index.ts"))
	}
	writeFiles(t, dir, map[string][]byte{
		"schema.config.json":       []byte(referencesConfig),
		"src/product.schema.ts":    []byte(referencesSource),
		"src/service.generated.ts": sentinel("shop", "General", "Shop"),
	})
	return dir, filepath.Join(root, "orders", "src", "service.generated.ts")
}

// A handle in a decorator argument references its service, and the IR
// records it once, sorted, without the schema itself; a handle under an
// identity path is no reference, and the schema keeps only the sentinel it
// came from. The data forms state the same references, so every form, and a
// write to each data form, loads the same IR (D41).
func TestDecoratorHandlesAreReferencesInEveryForm(t *testing.T) {
	reg := referencesRegistry(t)
	dir, ordersSentinel := referencesService(t)
	tsSchema, err := loader.LoadService(dir, loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("loading the TypeScript form: %v", err)
	}
	wantRefs := []ir.ServiceRef{{Name: "depot", Kind: ir.SchemaKindGeneral}, {Name: "ledger", Kind: ir.SchemaKindDB}}
	if !slices.Equal(tsSchema.References, wantRefs) {
		t.Errorf("References = %+v, want %+v", tsSchema.References, wantRefs)
	}
	ordersReal, err := filepath.EvalSymlinks(ordersSentinel)
	if err != nil {
		t.Fatal(err)
	}
	var identities []string
	for _, file := range tsSchema.IdentitySentinels {
		real, err := filepath.EvalSymlinks(file)
		if err != nil {
			t.Fatal(err)
		}
		identities = append(identities, real)
	}
	if !slices.Equal(identities, []string{ordersReal}) {
		t.Errorf("IdentitySentinels = %v, want only %s", tsSchema.IdentitySentinels, ordersReal)
	}

	want := normalized(t, tsSchema)
	if !strings.Contains(want, `"references"`) {
		t.Fatalf("the IR does not carry its references:\n%s", want)
	}
	for file, source := range referencesDocuments {
		dir := t.TempDir()
		writeFiles(t, dir, map[string][]byte{"schema.config.json": []byte(referencesConfig), file: []byte(source)})
		schema, err := loader.LoadService(dir, loader.WithRegistry(reg))
		if err != nil {
			t.Fatalf("loading %s: %v", file, err)
		}
		if got := normalized(t, schema); got != want {
			t.Errorf("%s and the TypeScript form produced different IR\n%s:\n%s\nts:\n%s", file, file, got, want)
		}
	}
	for _, target := range []writer.Format{writer.FormatJSON, writer.FormatYAML} {
		dir := t.TempDir()
		if _, err := writer.WriteService(tsSchema, target, dir); err != nil {
			t.Fatalf("WriteService(%s): %v", target, err)
		}
		writeFiles(t, dir, map[string][]byte{"schema.config.json": []byte(referencesConfig)})
		schema, err := loader.LoadService(dir, loader.WithRegistry(reg))
		if err != nil {
			t.Fatalf("reloading the %s form: %v", target, err)
		}
		if got := normalized(t, schema); got != want {
			t.Errorf("IR changed across TS -> %s\nwant:\n%s\ngot:\n%s", target, want, got)
		}
	}
}

// A data form's reference names a kind the registry knows, as its kind
// does.
func TestDataFormReferenceKindIsChecked(t *testing.T) {
	reg := referencesRegistry(t)
	dir := t.TempDir()
	writeFiles(t, dir, map[string][]byte{
		"schema.config.json": []byte(referencesConfig),
		"src/product.schema.yaml": []byte(`references:
  - { name: depot, kind: Warehouse }
types:
  Product:
    name: Product
    role: EmbeddedStruct
    fields:
      - name: name
        typeRef: { name: string }
        required: true
`),
	})
	_, err := loader.LoadService(dir, loader.WithRegistry(reg))
	if err == nil || !strings.Contains(err.Error(), "references") {
		t.Fatalf("a reference of an unknown kind loaded: %v", err)
	}
}
