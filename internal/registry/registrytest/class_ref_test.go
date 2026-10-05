package registrytest_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/jsonreader"
	"github.com/parable-work/superschematic/internal/writer"
	ir "github.com/parable-work/superschematic/ir"
)

// classConfig declares the depot service, whose package @schemas/depot the
// TypeScript fixtures import a class from.
const classConfig = `{"name": "shop", "kind": "General", "dependencies": [{"name": "depot", "kind": "General"}], "outputs": {}}`

// classSource names a class of its own schema as @pairsWith's whole argument
// and one from another service's package inside @meta's object: any
// decorator's argument can hold a class.
const classSource = `import { meta, pairsWith } from "@acme/schematic";
import { Warehouse } from "@schemas/depot";

export abstract class Accessory {
  name: string;
}

@pairsWith(Accessory)
@meta({ region: "eu", storedIn: [Warehouse] })
export abstract class Product {
  name: string;
}
`

// classDocuments are the data forms of classSource: each class is written
// {"class": name}, and the imported one is listed under imports.
var classDocuments = map[string]string{
	"src/product.schema.json": `{
  "imports": [{ "package": "@schemas/depot", "types": ["Warehouse"] }],
  "types": {
    "Accessory": {
      "name": "Accessory",
      "role": "EmbeddedStruct",
      "fields": [{ "name": "name", "typeRef": { "name": "string" }, "required": true }]
    },
    "Product": {
      "name": "Product",
      "role": "EmbeddedStruct",
      "extensions": {
        "acme": {
          "pairsWith": { "class": "Accessory" },
          "meta": { "region": "eu", "storedIn": [{ "class": "Warehouse" }] }
        }
      },
      "fields": [{ "name": "name", "typeRef": { "name": "string" }, "required": true }]
    }
  }
}`,
	"src/product.schema.yaml": `imports:
  - package: "@schemas/depot"
    types: [Warehouse]
types:
  Accessory:
    name: Accessory
    role: EmbeddedStruct
    fields:
      - name: name
        typeRef: { name: string }
        required: true
  Product:
    name: Product
    role: EmbeddedStruct
    extensions:
      acme:
        pairsWith: { class: Accessory }
        meta: { region: eu, storedIn: [{ class: Warehouse }] }
    fields:
      - name: name
        typeRef: { name: string }
        required: true
`,
}

// classService writes a TypeScript General service whose config declares
// the depot service, with the @schemas/depot package (one class, Warehouse)
// beside it, and returns the service directory.
func classService(t *testing.T, source string) string {
	t.Helper()
	root := t.TempDir()
	depot := filepath.Join(root, "depot")
	writeFiles(t, depot, map[string][]byte{
		"package.json": []byte(`{"name": "@schemas/depot", "private": true}`),
		"src/index.ts": []byte("export abstract class Warehouse {\n  name: string;\n}\n"),
	})
	dir := filepath.Join(root, "shop")
	tsProject(t, dir)
	addTSPath(t, dir, "@schemas/depot", filepath.Join(depot, "src", "index.ts"))
	writeFiles(t, dir, map[string][]byte{
		"schema.config.json":    []byte(classConfig),
		"src/product.schema.ts": []byte(source),
	})
	return dir
}

// addTSPath maps a module specifier to a file in the service's tsconfig.
func addTSPath(t *testing.T, dir, specifier, file string) {
	t.Helper()
	path := filepath.Join(dir, "tsconfig.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var tsconfig map[string]any
	if err := json.Unmarshal(data, &tsconfig); err != nil {
		t.Fatal(err)
	}
	paths := tsconfig["compilerOptions"].(map[string]any)["paths"].(map[string]any)
	paths[specifier] = []string{filepath.ToSlash(file)}
	data, err = json.MarshalIndent(tsconfig, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// dataService writes a data-form service with classConfig and the given
// schema files.
func dataService(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	contents := map[string][]byte{"schema.config.json": []byte(classConfig)}
	for name, source := range files {
		contents[name] = []byte(source)
	}
	writeFiles(t, dir, contents)
	return dir
}

// A class named as a value in an extension decorator's argument reaches
// Apply and the extension slot as {"class": name} from the TypeScript form
// and from both data forms. The class from another service's package is
// recorded under that package in Imports, as a field type from it is, so
// every form gives the same IR.
func TestClassArgumentsLoadTheSameFromEveryForm(t *testing.T) {
	reg := acmeRegistry(t)
	tsSchema, err := loader.LoadService(classService(t, classSource), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("loading the TypeScript form: %v", err)
	}
	const wantSlot = `{"meta":{"region":"eu","storedIn":[{"class":"Warehouse"}]},"pairsWith":{"class":"Accessory"}}`
	if got := string(tsSchema.Types["Product"].Extensions["acme"]); got != wantSlot {
		t.Errorf("TypeDef.Extensions[acme] = %s, want %s", got, wantSlot)
	}
	wantImports := []ir.Import{{Package: "@schemas/depot", Types: []string{"Warehouse"}}}
	if !slices.EqualFunc(tsSchema.Imports, wantImports, func(a, b ir.Import) bool {
		return a.Package == b.Package && slices.Equal(a.Types, b.Types)
	}) {
		t.Errorf("Imports = %+v, want %+v", tsSchema.Imports, wantImports)
	}

	want := normalized(t, tsSchema)
	for file, source := range classDocuments {
		schema, err := loader.LoadService(dataService(t, map[string]string{file: source}), loader.WithRegistry(reg))
		if err != nil {
			t.Fatalf("loading %s: %v", file, err)
		}
		if got := normalized(t, schema); got != want {
			t.Errorf("%s and the TypeScript form produced different IR\n%s:\n%s\nts:\n%s", file, file, got, want)
		}
	}
}

// format carries class references from TypeScript to JSON and YAML, and from
// one data form to the other, and each loads back to the same IR. The
// TypeScript writer cannot render extension data (extension-model.md,
// section 11).
func TestClassArgumentsRoundTripThroughFormat(t *testing.T) {
	reg := acmeRegistry(t)
	schema, err := loader.LoadService(classService(t, classSource), loader.WithRegistry(reg))
	if err != nil {
		t.Fatal(err)
	}
	want := normalized(t, schema)
	reload := func(t *testing.T, dir string) *ir.Schema {
		t.Helper()
		writeFiles(t, dir, map[string][]byte{"schema.config.json": []byte(classConfig)})
		got, err := loader.LoadService(dir, loader.WithRegistry(reg))
		if err != nil {
			t.Fatalf("reloading %s: %v", dir, err)
		}
		return got
	}

	jsonDir := t.TempDir()
	for _, target := range []writer.Format{writer.FormatJSON, writer.FormatYAML} {
		dir := t.TempDir()
		if target == writer.FormatJSON {
			dir = jsonDir
		}
		if _, err := writer.WriteService(schema, target, dir); err != nil {
			t.Fatalf("WriteService(%s): %v", target, err)
		}
		if got := normalized(t, reload(t, dir)); got != want {
			t.Fatalf("IR changed across TS -> %s\nwant:\n%s\ngot:\n%s", target, want, got)
		}
	}

	// JSON to YAML the way format converts a data-form file.
	doc, err := jsonreader.ReadFileWith(filepath.Join(jsonDir, "src", "product.schema.json"), "src/product.schema.json", reg)
	if err != nil {
		t.Fatal(err)
	}
	out, err := writer.Write(doc, writer.FormatYAML)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "pairsWith:\n") || !strings.Contains(string(out), "class: Accessory") {
		t.Errorf("the YAML form does not name the class:\n%s", out)
	}
	yamlDir := t.TempDir()
	writeFiles(t, yamlDir, map[string][]byte{"src/product.schema.yaml": out})
	if got := normalized(t, reload(t, yamlDir)); got != want {
		t.Fatalf("IR changed across JSON -> YAML\nwant:\n%s\ngot:\n%s", want, got)
	}
}

// A class the schema neither declares nor imports fails the load and is
// named, in every form; so does a class written as a string.
func TestUnknownClassArgumentFailsTheLoad(t *testing.T) {
	reg := acmeRegistry(t)
	yamlProduct := func(acme string) string {
		return `types:
  Product:
    name: Product
    role: EmbeddedStruct
    extensions:
      acme: ` + acme + `
    fields:
      - name: name
        typeRef: { name: string }
        required: true
`
	}
	for _, tc := range []struct {
		name string
		dir  func(t *testing.T) string
		want string
	}{
		{
			name: "yaml_unknown_class",
			dir: func(t *testing.T) string {
				return dataService(t, map[string]string{"src/product.schema.yaml": yamlProduct("{ pairsWith: { class: Missing } }")})
			},
			want: `src/product.schema.yaml: type "Product": @pairsWith names class "Missing", which this schema neither declares nor imports`,
		},
		{
			// Warehouse is depot's, but this file does not import it.
			name: "json_class_not_imported",
			dir: func(t *testing.T) string {
				return dataService(t, map[string]string{"src/product.schema.json": `{"types": {"Product": {
  "name": "Product", "role": "EmbeddedStruct",
  "extensions": {"acme": {"meta": {"storedIn": [{"class": "Warehouse"}]}}},
  "fields": [{"name": "name", "typeRef": {"name": "string"}, "required": true}]}}}`})
			},
			want: `src/product.schema.json: type "Product": @meta names class "Warehouse", which this schema neither declares nor imports`,
		},
		{
			name: "yaml_string_for_a_class",
			dir: func(t *testing.T) string {
				return dataService(t, map[string]string{"src/product.schema.yaml": yamlProduct("{ pairsWith: Accessory }")})
			},
			// The composed schema-file JSON Schema holds the slot to Args.
			want: `at '/types/Product/extensions/acme/pairsWith': got string, want object`,
		},
		{
			name: "ts_unknown_class",
			dir: func(t *testing.T) string {
				return classService(t, strings.Replace(classSource, "@pairsWith(Accessory)", "@pairsWith(Missing)", 1))
			},
			want: `Cannot find name 'Missing'`,
		},
		{
			// A class the walker does not read is not a schema class.
			name: "ts_unexported_class",
			dir: func(t *testing.T) string {
				return classService(t, strings.Replace(classSource, "export abstract class Accessory", "abstract class Accessory", 1))
			},
			want: `src/product.schema.ts: type "Product": @pairsWith names class "Accessory", which this schema neither declares nor imports`,
		},
		{
			name: "ts_class_spelled_as_data",
			dir: func(t *testing.T) string {
				return classService(t, strings.Replace(classSource, "storedIn: [Warehouse]", `storedIn: [{ class: "Warehouse" }]`, 1))
			},
			want: `src/product.schema.ts:9:34: write the class itself, not { class: "Warehouse" }: an object whose only key is class is a class reference`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loader.LoadService(tc.dir(t), loader.WithRegistry(reg))
			if err == nil {
				t.Fatal("the service loaded")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v\nwant it to contain %q", err, tc.want)
			}
		})
	}
}
