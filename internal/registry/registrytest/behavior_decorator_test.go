package registrytest_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
	"github.com/parable-work/superschematic/internal/writer"
	ir "github.com/parable-work/superschematic/ir"
)

// tsProject writes the tsconfig.json and package.json a TypeScript service
// in dir needs: the core packages, the fixture's authoring package and its
// BehaviorConfigs augmentation resolve by absolute path.
func tsProject(t *testing.T, dir string) {
	t.Helper()
	root := testpaths.RepoRoot(t)
	abs := func(parts ...string) string {
		return filepath.ToSlash(filepath.Join(append([]string{root}, parts...)...))
	}
	fixturePackage := []string{"internal", "registry", "registrytest", "testdata", "packages", "schematic", "src"}
	tsconfig := map[string]any{
		"compilerOptions": map[string]any{
			"target": "ES2022", "module": "ESNext", "moduleResolution": "Bundler", "lib": []string{"ES2022"},
			"strict": true, "strictPropertyInitialization": false, "experimentalDecorators": true,
			"noEmit": true, "skipLibCheck": true, "baseUrl": ".",
			"paths": map[string]any{
				"superscalar":                   []string{filepath.ToSlash(filepath.Join(testpaths.Local(t).ScalarTypeScript, "src", "index.ts"))},
				"@superschematic/schema":        []string{abs("packages", "schema", "src", "index.ts")},
				"@superschematic/schema-config": []string{abs("packages", "schema-config", "src", "index.ts")},
				"@acme/schematic":               []string{abs(append(fixturePackage, "index.ts")...)},
			},
		},
		"include": []string{"schema.config.ts", "src/**/*.ts", abs(append(fixturePackage, "behaviors.ts")...)},
	}
	data, err := json.MarshalIndent(tsconfig, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeFiles(t, dir, map[string][]byte{
		"tsconfig.json": data,
		"package.json":  []byte(`{"name": "@acme/stock", "private": true}`),
	})
}

func writeFiles(t *testing.T, dir string, files map[string][]byte) {
	t.Helper()
	for path, content := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, path)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, path), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// tsStockService writes a General service like testdata/stock with the
// given item.schema.ts.
func tsStockService(t *testing.T, source string) string {
	t.Helper()
	dir := t.TempDir()
	config, err := os.ReadFile("testdata/stock/schema.config.ts")
	if err != nil {
		t.Fatal(err)
	}
	tsProject(t, dir)
	writeFiles(t, dir, map[string][]byte{"schema.config.ts": config, "src/item.schema.ts": []byte(source)})
	return dir
}

// normalized is the persisted IR with the owners' format extensions
// dropped, so the same schema compares equal from every form.
func normalized(t *testing.T, schema *ir.Schema) string {
	t.Helper()
	out := persisted(t, schema)
	for _, ext := range []string{".schema.ts", ".schema.json", ".schema.yaml"} {
		out = strings.ReplaceAll(out, ext, ".schema")
	}
	return out
}

// The TypeScript twin's behaviors survive a write to each form and a
// reload through the full reader pipeline: TS to JSON, YAML and TS, and
// the data forms back to TS, with the same IR every time.
func TestBehaviorsRoundTripThroughEveryForm(t *testing.T) {
	reg := acmeRegistry(t)
	schema, err := loader.LoadService("testdata/stock", loader.WithRegistry(reg))
	if err != nil {
		t.Fatal(err)
	}
	want := normalized(t, schema)
	reload := func(t *testing.T, from *ir.Schema, target writer.Format) *ir.Schema {
		t.Helper()
		dir := t.TempDir()
		if _, err := writer.WriteService(from, target, dir); err != nil {
			t.Fatalf("WriteService(%s): %v", target, err)
		}
		writeFiles(t, dir, map[string][]byte{"schema.config.json": []byte(`{"name": "stock", "kind": "General", "outputs": {}}`)})
		if target == writer.FormatTS {
			tsProject(t, dir)
		}
		got, err := loader.LoadService(dir, loader.WithRegistry(reg))
		if err != nil {
			t.Fatalf("reloading the %s form: %v", target, err)
		}
		return got
	}
	for _, target := range []writer.Format{writer.FormatJSON, writer.FormatYAML, writer.FormatTS} {
		t.Run("ts_to_"+string(target), func(t *testing.T) {
			once := reload(t, schema, target)
			if got := normalized(t, once); got != want {
				t.Fatalf("IR changed across TS -> %s\nwant:\n%s\ngot:\n%s", target, want, got)
			}
			if target == writer.FormatTS {
				return
			}
			if got := normalized(t, reload(t, once, writer.FormatTS)); got != want {
				t.Fatalf("IR changed across %s -> TS\nwant:\n%s\ngot:\n%s", target, want, got)
			}
		})
	}
}

// lifecycleBehavior is a core behavior these tests declare, whose config
// holds lists a type may leave empty.
var lifecycleBehavior = json.RawMessage(`{
  "name": "Lifecycle",
  "description": "Moves an item through named states.",
  "configSchema": {
    "type": "object",
    "required": ["states", "transitions"],
    "additionalProperties": false,
    "properties": {
      "states": { "type": "array", "minItems": 1, "items": { "type": "string" } },
      "transitions": {
        "type": "array",
        "items": {
          "type": "object",
          "required": ["from", "to"],
          "additionalProperties": false,
          "properties": { "from": { "type": "string" }, "to": { "type": "string" } }
        }
      },
      "guards": { "type": "object" }
    }
  }
}`)

// lifecycleConfigs types Lifecycle's config for tsc, as an authoring
// package's BehaviorConfigs augmentation does.
const lifecycleConfigs = `import "@superschematic/schema";

declare module "@superschematic/schema" {
  interface BehaviorConfigs {
    Lifecycle: {
      readonly states: readonly string[];
      readonly transitions: readonly { readonly from: string; readonly to: string }[];
      readonly guards?: Readonly<Record<string, unknown>>;
    };
  }
}
`

// An empty list or object in a @behavior config is the value the data
// forms write for it (extension-model.md, sections 2 and 4.2): the
// TypeScript form loads to its JSON twin's IR, where [] used to evaluate
// to null and fail the config schema. The JSON twin written to TypeScript
// spells the empty list [] and reloads to the same IR.
func TestBehaviorConfigWithEmptyListsMatchesTheDataForm(t *testing.T) {
	reg := acmeRegistry(t, lifecycleBehavior)
	jsonDir := t.TempDir()
	writeFiles(t, jsonDir, map[string][]byte{
		"schema.config.json": []byte(`{"name": "stock", "kind": "General", "outputs": {}}`),
		"src/item.schema.json": []byte(`{
  "types": {
    "Item": {
      "name": "Item",
      "role": "EmbeddedStruct",
      "behaviors": [
        { "name": "Lifecycle", "config": { "states": ["open"], "transitions": [], "guards": {} } }
      ],
      "fields": [{ "name": "name", "typeRef": { "name": "string" }, "required": true }]
    }
  }
}`),
	})
	jsonSchema, err := loader.LoadService(jsonDir, loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("loading the JSON form: %v", err)
	}
	want := normalized(t, jsonSchema)
	if config := string(jsonSchema.Types["Item"].Behaviors[0].Config); config != `{"guards":{},"states":["open"],"transitions":[]}` {
		t.Fatalf("JSON form config = %s", config)
	}

	tsDir := tsStockService(t, `import { behavior } from "@superschematic/schema";

@behavior("Lifecycle", { states: ["open"], transitions: [], guards: {} })
export abstract class Item {
  name: string;
}
`)
	writeFiles(t, tsDir, map[string][]byte{"src/lifecycle.ts": []byte(lifecycleConfigs)})
	tsSchema, err := loader.LoadService(tsDir, loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("loading the TypeScript form: %v", err)
	}
	if got := normalized(t, tsSchema); got != want {
		t.Fatalf("TypeScript and JSON forms produced different IR\njson:\n%s\nts:\n%s", want, got)
	}

	writtenDir := t.TempDir()
	written, err := writer.WriteService(jsonSchema, writer.FormatTS, writtenDir)
	if err != nil {
		t.Fatalf("WriteService(ts): %v", err)
	}
	source, err := os.ReadFile(filepath.Join(writtenDir, written[0]))
	if err != nil {
		t.Fatal(err)
	}
	if wantTS := `@behavior("Lifecycle", { guards: {}, states: ["open"], transitions: [] })`; !strings.Contains(string(source), wantTS) {
		t.Errorf("TypeScript form lacks %s:\n%s", wantTS, source)
	}
	writeFiles(t, writtenDir, map[string][]byte{
		"schema.config.json": []byte(`{"name": "stock", "kind": "General", "outputs": {}}`),
		"src/lifecycle.ts":   []byte(lifecycleConfigs),
	})
	tsProject(t, writtenDir)
	reloaded, err := loader.LoadService(writtenDir, loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("reloading the written TypeScript form: %v\n%s", err, source)
	}
	if got := normalized(t, reloaded); got != want {
		t.Fatalf("IR changed across JSON -> TS\nwant:\n%s\ngot:\n%s", want, got)
	}
}

// @behavior fails the load at the decorator, naming the type and the
// behavior, when the registry refuses it; tsc refuses what the
// augmentation's types rule out; verify refuses what only the whole list
// shows.
func TestBehaviorDecoratorRefuses(t *testing.T) {
	reg := acmeRegistry(t)
	const header = "import { behavior } from \"@superschematic/schema\";\n\n"
	for _, test := range []struct {
		name       string
		decorators string
		want       string
	}{
		{"config its schema rejects", `@behavior("acme.Stock", { aisles: 0 })`,
			"src/item.schema.ts:3:25: type Item: behavior acme.Stock config: "},
		{"listed twice", "@behavior(\"acme.Stock\", { aisles: 1 })\n@behavior(\"acme.Stock\", { aisles: 2 })",
			"src/item.schema.ts:4:1: type Item lists behavior acme.Stock twice"},
		{"missing requirement", `@behavior("acme.Audited")`,
			"type Item: behavior acme.Audited requires behavior acme.Stock, which the type does not list"},
		{"config of the wrong type", `@behavior("acme.Stock", { aisles: "three" })`,
			"src/item.schema.ts:3:27: Type 'string' is not assignable to type 'number'."},
		{"missing config", `@behavior("acme.Stock")`,
			"src/item.schema.ts:3:2: Expected 2 arguments, but got 1."},
		{"config on a behavior without one", `@behavior("acme.Audited", {})`,
			"src/item.schema.ts:3:27: Argument of type '{}' is not assignable to parameter of type 'undefined'."},
		{"name no augmentation declares", `@behavior("acme.Ghost")`,
			"src/item.schema.ts:3:11: Argument of type '\"acme.Ghost\"' is not assignable to parameter of type 'keyof BehaviorConfigs'."},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := tsStockService(t, header+test.decorators+"\nexport abstract class Item {\n  name: string;\n}\n")
			_, err := loader.LoadService(dir, loader.WithRegistry(reg))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err = %v, want %q", err, test.want)
			}
		})
	}
}
