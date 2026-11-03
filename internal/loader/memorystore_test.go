package loader

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// The MemoryStore fixtures (D75): fixture-cache, a store whose channels
// carry fixture-cache-common's StockLevel and whose parts name its Region,
// and fixture-cache-common, a General service.
var memoryStoreFixture = filepath.Join("tsreader", "testdata", "services", "fixture-cache")

// TestMemoryStoreDeclarationsLoad: each @channel and @key class is a
// declaration and no type, named in kebab case unless the decorator names
// it, with its fields the parts in order, each part's format read from its
// type, a payload imported from the dependency, and the core's reserved
// rate-limit declaration among them, sorted by name.
func TestMemoryStoreDeclarationsLoad(t *testing.T) {
	schema, err := LoadService(memoryStoreFixture)
	if err != nil {
		t.Fatal(err)
	}
	if len(schema.Types) != 0 {
		t.Errorf("Types = %v; a declaration's class is no type", keys(schema.Types))
	}
	var names []string
	for _, decl := range schema.MemoryStoreDeclarations {
		names = append(names, decl.Name)
	}
	if got := strings.Join(names, ","); got != "rate-limit,receipt-sent,stock-changed,stock-samples,views" {
		t.Errorf("declarations = %s", got)
	}
	views := schema.MemoryStoreDeclaration("views")
	want := &ir.MemoryStoreDeclaration{
		Name: "views", Symbol: "ProductViews", Kind: ir.MemoryStoreCounter, MaxTTLSeconds: 86400,
		Doc: "Views of a product a day, in a region.", File: "src/cache.schema.ts",
		Parts: []ir.MemoryStorePart{
			{Name: "productId", Type: ir.TypeRef{Name: "Identity.UUID"}, Format: ir.MemoryStorePartString},
			{Name: "day", Type: ir.TypeRef{Name: "Generic.Int64"}, Format: ir.MemoryStorePartInteger},
			{Name: "region", Type: ir.TypeRef{Name: "Region"}, Format: ir.MemoryStorePartEnum},
			{Name: "mobile", Type: ir.TypeRef{Name: "boolean"}, Format: ir.MemoryStorePartBoolean},
			{Name: "label", Type: ir.TypeRef{Name: "string"}, Format: ir.MemoryStorePartString},
		},
	}
	if !reflect.DeepEqual(views, want) {
		got, _ := json.MarshalIndent(views, "", "  ")
		t.Errorf("views = %s", got)
	}
	if samples := schema.MemoryStoreDeclaration("stock-samples"); samples == nil || samples.Payload == nil || samples.Payload.Name != "StockLevel" || samples.Kind != ir.MemoryStoreChannel {
		t.Errorf("stock-samples = %+v", samples)
	}
	if changed := schema.MemoryStoreDeclaration("stock-changed"); changed == nil || changed.Payload != nil {
		t.Errorf("stock-changed = %+v, want a signal", changed)
	}
	if claim := schema.MemoryStoreDeclaration("receipt-sent"); claim == nil || claim.Kind != ir.MemoryStoreClaim || claim.TTLSeconds != 600 {
		t.Errorf("receipt-sent = %+v", claim)
	}
	if !reflect.DeepEqual(schema.MemoryStoreDeclaration(ir.MemoryStoreRateLimit), ir.MemoryStoreRateLimitDeclaration()) {
		t.Errorf("rate-limit = %+v", schema.MemoryStoreDeclaration(ir.MemoryStoreRateLimit))
	}
}

func keys[V any](m map[string]V) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// memoryStoreService lays out a services root holding
// fixture-cache-common and a MemoryStore service named store whose one
// schema file is schema, beside a tsconfig that resolves the authoring
// packages, superscalar and the services' packages. It returns the store's
// directory.
func memoryStoreService(t *testing.T, schema string) string {
	t.Helper()
	repo := testpaths.RepoRoot(t)
	root := t.TempDir()
	base := map[string]any{
		"compilerOptions": map[string]any{
			"target": "ES2022", "module": "ESNext", "moduleResolution": "Bundler", "lib": []string{"ES2022"},
			"strict": true, "strictPropertyInitialization": false, "experimentalDecorators": true,
			"skipLibCheck": true, "noEmit": true, "baseUrl": ".",
			"paths": map[string][]string{
				"@superschematic/memory-store":  {filepath.Join(repo, "packages/memory-store/src/index.ts")},
				"@superschematic/schema":        {filepath.Join(repo, "packages/schema/src/index.ts")},
				"@superschematic/schema-config": {filepath.Join(repo, "packages/schema-config/src/index.ts")},
				"@superschematic/api":           {filepath.Join(repo, "packages/api/src/index.ts")},
				"superscalar":                   {filepath.Join(repo, "third_party/superscalar/bindings/typescript/src/index.ts")},
				"@schemas/*":                    {"./services/*/src/index.ts"},
			},
		},
	}
	data, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"tsconfig.base.json": string(data)}
	common := filepath.Join("tsreader", "testdata", "services", "fixture-cache-common")
	if err := filepath.Walk(common, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(common, path)
		files[filepath.Join("services", "fixture-cache-common", rel)] = string(contents)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	store := filepath.Join("services", "store")
	files[filepath.Join(store, "package.json")] = `{"name": "@schemas/store", "private": true}`
	files[filepath.Join(store, "tsconfig.json")] = `{"extends": "../../tsconfig.base.json", "include": ["schema.config.ts", "src/**/*.ts"]}`
	files[filepath.Join(store, "schema.config.ts")] = `import { defineConfig, SchemaKind } from "@superschematic/schema-config";
import { FixtureCacheCommon } from "@schemas/fixture-cache-common";

export default defineConfig({
  name: "store",
  kind: SchemaKind.MemoryStore,
  dependencies: [FixtureCacheCommon],
  outputs: {}
});
`
	files[filepath.Join(store, "src", "index.ts")] = "export * from \"./store.schema\";\nexport * from \"./service.generated\";\n"
	files[filepath.Join(store, "src", "service.generated.ts")] = `import { service, SchemaKind } from "@superschematic/schema-config";
export const Store = service({ name: "store", kind: SchemaKind.MemoryStore });
`
	files[filepath.Join(store, "src", "store.schema.ts")] = schema
	for rel, contents := range files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(root, store)
}

const memoryStoreImports = `import { channel, key } from "@superschematic/memory-store";
import { Generic, Identity } from "superscalar";
import { Region, StockLevel, StrictSample } from "@schemas/fixture-cache-common";
`

// TestMemoryStoreVerifyRefusals: each rule of D75's verification refuses
// its declaration, with the file, and a decorator's argument with its
// line.
func TestMemoryStoreVerifyRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       []string
	}{
		{"key without a TTL", `@key({ use: "counter" })
export abstract class Views { productId: Identity.UUID }`, []string{"store.schema.ts:4:6", "is not assignable to parameter of type 'KeyOptions'"}},
		{"key with both TTLs", `@key({ use: "claim", ttlSeconds: 60, maxTtlSeconds: 120 })
export abstract class Sent { orderId: Identity.UUID }`, []string{"is not assignable to parameter of type 'KeyOptions'"}},
		{"key without a TTL past the type", `// @ts-expect-error every key expires
@key({ use: "counter" })
export abstract class Views { productId: Identity.UUID }`, []string{"store.schema.ts:5:", "sets neither ttlSeconds nor maxTtlSeconds"}},
		{"key with both TTLs past the type", `// @ts-expect-error a key sets one
@key({ use: "claim", ttlSeconds: 60, maxTtlSeconds: 120 })
export abstract class Sent { orderId: Identity.UUID }`, []string{"sets both ttlSeconds and maxTtlSeconds"}},
		{"key with a payload past the type", `// @ts-expect-error a key holds no value
@key({ use: "claim", ttlSeconds: 60, payload: StockLevel })
export abstract class Sent { orderId: Identity.UUID }`, []string{"a key holds a count or a claim, never a value"}},
		{"TTL above seven days", `@key({ use: "claim", ttlSeconds: 604801 })
export abstract class Sent { orderId: Identity.UUID }`, []string{"ttlSeconds is 604801; it is from 1 to 604800"}},
		{"TTL below a second", `@key({ use: "counter", maxTtlSeconds: -1 })
export abstract class Views { productId: Identity.UUID }`, []string{"maxTtlSeconds is -1"}},
		{"channel with a TTL", `// @ts-expect-error a channel has no TTL
@channel({ ttlSeconds: 60 })
export abstract class Changed { productId: Identity.UUID }`, []string{"a channel's messages are not kept, so it has no TTL"}},
		{"strictJSON payload", `@channel({ payload: StrictSample })
export abstract class Samples { productId: Identity.UUID }`, []string{"store.schema.ts", "StrictSample of fixture-cache-common is @strictJSON"}},
		{"payload not a dependency's type", `export abstract class Local { id: string }
@channel({ payload: Local })
export abstract class Samples { productId: Identity.UUID }`, []string{"Local is imported from no dependency", "(type Local)"}},
		{"optional part", `@channel()
export abstract class Changed { productId?: Identity.UUID }`, []string{"field Changed.productId is optional"}},
		{"list part", `@channel()
export abstract class Changed { productIds: Identity.UUID[] }`, []string{"field Changed.productIds is a list or a map"}},
		{"object part", `@channel()
export abstract class Changed { level: StockLevel }`, []string{"part level: StockLevel is an object type of fixture-cache-common"}},
		{"float part", `@channel()
export abstract class Changed { ratio: number }`, []string{"part ratio: number is a floating-point number"}},
		{"name not a segment", `@channel({ name: "stock:changed" })
export abstract class Changed { productId: Identity.UUID }`, []string{"a segment holds ':'"}},
		{"name not lowercase", `@channel({ name: "Stock" })
export abstract class Changed { productId: Identity.UUID }`, []string{`name "Stock" is not a declaration's name`}},
		{"two declarations with one name", `@channel()
export abstract class StockChanged { productId: Identity.UUID }
@channel({ name: "stock-changed" })
export abstract class Other { productId: Identity.UUID }`, []string{"is named stock-changed, like class StockChanged"}},
		{"the reserved name", `@key({ use: "counter", maxTtlSeconds: 120, name: "rate-limit" })
export abstract class Limits { client: string }`, []string{"takes the name rate-limit, which the core declares"}},
		{"a type", `export abstract class Loose { id: string }`, []string{"declares only channels and keys", "(type Loose)"}},
		{"an enum", `export enum Mode { A = "a" }`, []string{"(enum Mode)"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadService(memoryStoreService(t, memoryStoreImports+tc.body+"\n"))
			if err == nil {
				t.Fatal("the load succeeded")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q lacks %q", err, want)
				}
			}
		})
	}
}

// TestMemoryStoreDecoratorErrorsAreLocated: an argument the decorator
// refuses is reported at its line and column, as D72's check prints it.
func TestMemoryStoreDecoratorErrorsAreLocated(t *testing.T) {
	_, err := LoadService(memoryStoreService(t, memoryStoreImports+"\n@key({ use: \"counter\", ttlSeconds: 604801 })\nexport abstract class Views { productId: Identity.UUID }\n"))
	if err == nil || !strings.Contains(err.Error(), "store.schema.ts:5:") || !strings.Contains(err.Error(), "604801") {
		t.Errorf("err = %v, want it at store.schema.ts:5", err)
	}
}

// TestMemoryStoreDataForm: a MemoryStore schema in YAML declares the same
// declarations, its formats filled in by the load and the reserved
// declaration added, and the rules hold there too: a channel with a TTL,
// which the TypeScript decorator's type refuses, is refused.
func TestMemoryStoreDataForm(t *testing.T) {
	config := "name: store\nkind: MemoryStore\noutputs: {}\n"
	ok := writeService(t, map[string]string{
		"schema.config.yaml": config,
		"src/store.schema.yaml": `memoryStoreDeclarations:
  - name: stock-changed
    symbol: StockChanged
    kind: channel
    parts:
      - name: productId
        type: { name: string }
  - name: views
    symbol: Views
    kind: counter
    maxTtlSeconds: 3600
    parts:
      - name: day
        type: { name: boolean }
`,
	})
	schema, err := LoadService(ok)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(schema.MemoryStoreDeclarations); got != 3 {
		t.Fatalf("declarations = %d, want 3", got)
	}
	if d := schema.MemoryStoreDeclaration("stock-changed"); d.Parts[0].Format != ir.MemoryStorePartString || d.File != filepath.Join("src", "store.schema.yaml") {
		t.Errorf("stock-changed = %+v", d)
	}
	if d := schema.MemoryStoreDeclaration("views"); d.Parts[0].Format != ir.MemoryStorePartBoolean {
		t.Errorf("views = %+v", d)
	}
	for _, tc := range []struct{ name, decl, want string }{
		{"channel with a TTL", "  - name: c\n    symbol: C\n    kind: channel\n    ttlSeconds: 60\n", "sets neither ttlSeconds nor maxTtlSeconds"},
		{"key without a TTL", "  - name: k\n    symbol: K\n    kind: claim\n", "sets neither ttlSeconds nor maxTtlSeconds"},
		{"unknown kind", "  - name: k\n    symbol: K\n    kind: list\n", "kind"},
		{"key with a payload", "  - name: k\n    symbol: K\n    kind: claim\n    ttlSeconds: 5\n    payload: { name: StockLevel }\n", "a claim has a payload"},
		{"reserved name", "  - name: rate-limit\n    symbol: RateLimit\n    kind: counter\n    maxTtlSeconds: 5\n", "takes the name rate-limit"},
		{"reserved flag", "  - name: mine\n    symbol: Mine\n    kind: counter\n    maxTtlSeconds: 5\n    reserved: true\n", "only the core's rate-limit is"},
		{"payload not imported", "  - name: c\n    symbol: C\n    kind: channel\n    payload: { name: StockLevel }\n", "StockLevel is imported from no dependency"},
		{"unknown part type", "  - name: c\n    symbol: C\n    kind: channel\n    parts:\n      - name: x\n        type: { name: Mystery }\n", "Mystery is not a scalar"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeService(t, map[string]string{
				"schema.config.yaml":    config,
				"src/store.schema.yaml": "memoryStoreDeclarations:\n" + tc.decl,
			})
			_, err := LoadService(dir)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// TestMemoryStoreDeclarationsOnlyInAMemoryStore: an API schema that
// imports @channel is refused, as is a data form API that writes
// declarations.
func TestMemoryStoreDeclarationsOnlyInAMemoryStore(t *testing.T) {
	dir := writeService(t, map[string]string{
		"schema.config.yaml":  "name: api\nkind: API\noutputs: {}\n",
		"src/api.schema.yaml": "memoryStoreDeclarations:\n  - name: c\n    symbol: C\n    kind: channel\n",
	})
	_, err := LoadService(dir)
	if err == nil || !strings.Contains(err.Error(), "only a MemoryStore schema declares channels and keys") {
		t.Errorf("err = %v", err)
	}
}

// TestMemoryStoresInTheTypeScriptConfig: an API's schema.config.ts lists
// a store's handle in memoryStores and names one its rateLimitStore, which
// the IR records (D75); a handle of another kind is refused.
func TestMemoryStoresInTheTypeScriptConfig(t *testing.T) {
	store := memoryStoreService(t, memoryStoreImports+"@channel()\nexport abstract class Changed { productId: Identity.UUID }\n")
	write := func(config string) string {
		dir := filepath.Join(filepath.Dir(store), "api")
		for rel, contents := range map[string]string{
			"package.json":     `{"name": "@schemas/api", "private": true}`,
			"tsconfig.json":    `{"extends": "../../tsconfig.base.json", "include": ["schema.config.ts", "src/**/*.ts"]}`,
			"schema.config.ts": config,
			"src/index.ts":     "export const unused = 1;\n",
		} {
			path := filepath.Join(dir, rel)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	schema, err := LoadService(write(`import { defineConfig, SchemaKind } from "@superschematic/schema-config";
import { Store } from "@schemas/store";

export default defineConfig({
  name: "api",
  kind: SchemaKind.API,
  memoryStores: [Store],
  rateLimitStore: Store,
  outputs: {}
});
`))
	if err != nil {
		t.Fatal(err)
	}
	ref := ir.ServiceRef{Name: "store", Kind: ir.SchemaKindMemoryStore}
	if !reflect.DeepEqual(schema.MemoryStores, []ir.ServiceRef{ref}) || !reflect.DeepEqual(schema.RateLimitStore, &ref) {
		t.Errorf("memory stores = %+v, rate limit store = %+v", schema.MemoryStores, schema.RateLimitStore)
	}
	_, err = LoadService(write(`import { defineConfig, SchemaKind, service } from "@superschematic/schema-config";

export default defineConfig({
  name: "api",
  kind: SchemaKind.API,
  // @ts-expect-error memoryStores takes MemoryStore handles
  memoryStores: [service({ name: "media", kind: SchemaKind.Bucket })],
  outputs: {}
});
`))
	if err == nil || !strings.Contains(err.Error(), `lists media in memoryStores, a handle of kind "Bucket"`) {
		t.Errorf("err = %v", err)
	}
}
