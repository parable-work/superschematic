package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/registry/registrytest"
)

const notesSchemaFile = "../examples/engine-notes/schemas/notes.schema.json"

// engine-client writes the module, and --check passes on it and fails,
// saying how to rewrite it, on a file that differs or is missing.
func TestEngineClientWritesAndChecks(t *testing.T) {
	out := filepath.Join(t.TempDir(), "src", "notes.client.ts")

	stdout, err := runCommandWith(t, nil, "engine-client", "--out", out, notesSchemaFile)
	require.NoError(t, err)
	assert.Contains(t, stdout, "engine-client: wrote "+out+" (1 schema(s))")
	module, err := os.ReadFile(out)
	require.NoError(t, err)
	committed, err := os.ReadFile("../examples/engine-notes/src/notes.client.ts")
	require.NoError(t, err)
	assert.Equal(t, string(committed), string(module), "the command writes what the example commits")

	stdout, err = runCommandWith(t, nil, "engine-client", "--out", out, "--check", notesSchemaFile)
	require.NoError(t, err)
	assert.Contains(t, stdout, "is current")

	require.NoError(t, os.WriteFile(out, append(module, '\n'), 0o644))
	_, err = runCommandWith(t, nil, "engine-client", "--out", out, "--check", notesSchemaFile)
	require.ErrorContains(t, err, "is not what the schemas generate; run: superschematic engine-client --out "+out+" "+notesSchemaFile)

	_, err = runCommandWith(t, nil, "engine-client", "--out", filepath.Join(t.TempDir(), "absent.ts"), "--check", notesSchemaFile)
	require.ErrorContains(t, err, "is missing")
}

// An extension's behavior is typed by its declaration in the binary that
// registers it, and refused by one that does not.
func TestEngineClientTypesAnExtensionsBehavior(t *testing.T) {
	dir := t.TempDir()
	schema := filepath.Join(dir, "items.schema.json")
	require.NoError(t, os.WriteFile(schema, []byte(`{
  "kind": "General",
  "name": "items",
  "types": {
    "Item": {
      "name": "Item",
      "role": "EmbeddedStruct",
      "behaviors": [{ "name": "acme.Stock", "config": { "aisles": 3 } }, { "name": "Workflow", "config": { "states": ["new", "old"], "transitions": [{ "from": "new", "to": "old" }] } }],
      "fields": [{ "name": "sku", "typeRef": { "name": "string" }, "required": true }]
    }
  }
}`), 0o644))
	out := filepath.Join(dir, "items.client.ts")

	_, err := runCommandWith(t, []registry.Extension{registrytest.Acme{}}, "engine-client", "--out", out, schema)
	require.NoError(t, err)
	module, err := os.ReadFile(out)
	require.NoError(t, err)
	for _, want := range []string{
		"  /** acme.Stock: Units on hand. */\n  readonly onHand?: unknown;\n",
		"export type ItemRestockParams = { quantity: number };\n",
		"/** An Item as a read returns it: its own fields, then its behaviors'. */\n",
		"  'acme.Stock': never;\n",
		"  restock(id: string, params: ItemRestockParams, options?: ItemWriteOptions): Promise<ItemRestockResult>;\n",
		"  countStock(id: string, params?: ItemCountStockParams, options?: ItemWriteOptions): Promise<ItemCountStockResult>;\n",
		"export type ItemState = 'new' | 'old';\n",
		"export function itemsClient(client: EngineClient): ItemsClient {\n",
	} {
		assert.Contains(t, string(module), want)
	}

	_, err = runCommandWith(t, nil, "engine-client", "--out", out, schema)
	require.ErrorContains(t, err, `behavior "acme.Stock" on type "Item" is not a registered behavior`)
}

// What the engine refuses, the command refuses: a schema of another kind,
// a map field, and a document with no instance type.
func TestEngineClientRefusesWhatTheEngineRefuses(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
		return path
	}
	out := filepath.Join(dir, "out.ts")

	mapField := write("maps.schema.json", `{"kind": "General", "name": "maps", "types": {"Map": {"name": "Map", "role": "EmbeddedStruct",
  "fields": [{"name": "tags", "typeRef": {"name": "string", "isMap": true}}]}}}`)
	_, err := runCommandWith(t, nil, "engine-client", "--out", out, mapField)
	require.ErrorContains(t, err, "engine-client: schema maps: field Map.tags is a map, which the engine refuses")

	twoTypes := write("pairs.schema.json", `{"kind": "General", "name": "pairs", "types": {
  "Left": {"name": "Left", "role": "EmbeddedStruct", "fields": [{"name": "a", "typeRef": {"name": "string"}}]},
  "Right": {"name": "Right", "role": "EmbeddedStruct", "fields": [{"name": "b", "typeRef": {"name": "string"}}]}}}`)
	_, err = runCommandWith(t, nil, "engine-client", "--out", out, twoTypes)
	require.ErrorContains(t, err, "engine-client: schema pairs has no instance type: declare a type named pairs or exactly one type (found: Left, Right)")

	_, err = runCommandWith(t, nil, "engine-client", "--out", out, notesSchemaFile, notesSchemaFile)
	require.ErrorContains(t, err, "engine-client: schema notes is given twice")

	_, err = runCommandWith(t, nil, "engine-client", "--out", out, filepath.Join(dir, "absent.schema.json"))
	require.ErrorContains(t, err, "schema file not found")
	_, statErr := os.Stat(out)
	assert.True(t, os.IsNotExist(statErr), "a refused run writes nothing")
}

// A .schema.ts file is read in the context of its service, as format
// reads it: here a step whose Variants pick its result's type by its kind.
func TestEngineClientReadsATypeScriptSchema(t *testing.T) {
	out := filepath.Join(t.TempDir(), "step.client.ts")
	_, err := runCommandWith(t, nil, "engine-client", "--out", out, "../internal/loader/tsreader/testdata/services/fixture-variants/src/step.schema.ts")
	require.NoError(t, err)
	module, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Contains(t, string(module), `export type StepFields = {
  title: string;
} & (
  | { kind: 'review'; result?: ReviewResult | null }
  | { kind: 'verify'; result?: VerifyResult | null }
  | { kind: 'note'; result?: null }
);
`)
	assert.Contains(t, string(module), "export type VerifyResult = {\n  passed: boolean;\n  checks?: Check[] | null;\n};\n")
}
