package cli

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	scalars "github.com/parable-work/superscalar/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/internal/registry/registrytest"
	"github.com/parable-work/superschematic/registry"
)

var updateFormat = flag.Bool("update", false, "rewrite testdata/format")

const tsTestdata = "../internal/loader/tsreader/testdata/services"

const acmeTestdata = "../internal/registry/registrytest/testdata"

// runFormatCommand executes the format command of a binary that links exts
// and returns its stdout.
func runFormatCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return runFormatCommandWith(t, nil, args...)
}

func runFormatCommandWith(t *testing.T, exts []registry.Extension, args ...string) (string, error) {
	t.Helper()
	buf := new(bytes.Buffer)
	root := New(Config{}, exts...)
	root.SetOut(buf)
	root.SetArgs(append([]string{"format"}, args...))
	err := root.Execute()
	return buf.String(), err
}

func TestFormatCommand_JSONToYAMLStdout(t *testing.T) {
	out, err := runFormatCommand(t, "--to=yaml", "--stdout",
		filepath.Join(loaderTestdata, "fixture-db-json/src/tenant.schema.json"))

	require.NoError(t, err)
	assert.Contains(t, out, "name: fixture-db")
	assert.Contains(t, out, "# A tenant of the platform.")
}

func TestFormatCommand_YAMLToTSStdout(t *testing.T) {
	out, err := runFormatCommand(t, "--to=ts", "--stdout",
		filepath.Join(loaderTestdata, "fixture-db-yaml/src/tenant.schema.yaml"))

	require.NoError(t, err)
	assert.Contains(t, out, `import { Generic, Identity, Temporal } from "superscalar";`)
	assert.Contains(t, out, "export abstract class Tenant extends Auditable {")
}

func TestFormatCommand_TSToJSONStdout(t *testing.T) {
	out, err := runFormatCommand(t, "--to=json", "--stdout",
		filepath.Join(tsTestdata, "fixture-db/src/tenant.schema.ts"))

	require.NoError(t, err)
	assert.Contains(t, out, `"name": "fixture-db"`)
	assert.Contains(t, out, `"comment": "A tenant of the platform."`)
}

func TestFormatCommand_SiblingFileAndForce(t *testing.T) {
	dir := t.TempDir()
	src, err := os.ReadFile(filepath.Join(loaderTestdata, "fixture-db-json/src/tenant.schema.json"))
	require.NoError(t, err)
	input := filepath.Join(dir, "tenant.schema.json")
	require.NoError(t, os.WriteFile(input, src, 0o644))

	out, err := runFormatCommand(t, "--to=yaml", input)
	require.NoError(t, err)
	sibling := filepath.Join(dir, "tenant.schema.yaml")
	assert.Contains(t, out, "Wrote "+sibling)
	assert.FileExists(t, sibling)

	_, err = runFormatCommand(t, "--to=yaml", input)
	require.ErrorContains(t, err, "pass --force to overwrite")

	_, err = runFormatCommand(t, "--to=yaml", "--force", input)
	require.NoError(t, err)
}

func TestFormatCommand_SameFormatRejected(t *testing.T) {
	_, err := runFormatCommand(t, "--to=json",
		filepath.Join(loaderTestdata, "fixture-db-json/src/tenant.schema.json"))

	require.ErrorContains(t, err, "already in the json format")
}

// A binary that links an extension converts a file that uses it: format reads
// with the registry the binary assembles. The core binary rejects the same
// file, and the TypeScript writer refuses extension data it cannot write
// instead of dropping it.
func TestFormatCommand_ReadsWithTheLinkedExtensions(t *testing.T) {
	yamlInput := filepath.Join(acmeTestdata, "shop-yaml/src/product.schema.yaml")
	acme := []registry.Extension{registrytest.Acme{}}

	_, err := runFormatCommand(t, "--to=json", "--stdout", yamlInput)
	require.ErrorContains(t, err, `unknown kind "Catalog"`)

	out, err := runFormatCommandWith(t, acme, "--to=json", "--stdout", yamlInput)
	require.NoError(t, err)
	assert.Contains(t, out, `"kind": "Catalog"`)
	assert.Contains(t, out, `"aisle": 3`)
	assert.Contains(t, out, `"tagged": true`)

	out, err = runFormatCommandWith(t, acme, "--to=yaml", "--stdout", filepath.Join(acmeTestdata, "shop/src/product.schema.ts"))
	require.NoError(t, err)
	assert.Contains(t, out, "kind: Catalog")
	assert.Contains(t, out, "aisle: 3")

	_, err = runFormatCommandWith(t, acme, "--to=ts", "--stdout", yamlInput)
	require.ErrorContains(t, err, "extension data (acme) has no TypeScript authoring form")
}

// extJSONScalars is an extension whose scalar catalog adds Ext.Doc to the
// core rows: a JSON object scalar with the Object primitive and the
// json_schema mapping object.
type extJSONScalars struct{}

func (extJSONScalars) Name() string { return "extjson" }

func (extJSONScalars) Register(r *registry.Registry) error {
	core := registry.CoreScalars()
	rows := make(map[string]*scalars.ScalarMetadata, len(core.Names())+1)
	for _, name := range core.Names() {
		row, _ := core.Scalar(name)
		rows[name] = row
	}
	rows["Ext.Doc"] = &scalars.ScalarMetadata{
		CanonicalName:  "Ext.Doc",
		Symbol:         "ExtDoc",
		Primitive:      "Object",
		Description:    "A JSON object an extension defines",
		TypeScriptType: "Readonly<Record<string, unknown>>",
		JSONSchemaType: "object",
	}
	return r.RegisterScalars("extjson", registry.ScalarCatalogOf(rows))
}

// TestFormatCommand_TSToJSONWritesAnExtensionJSONScalarsMapping: the
// TypeScript form records the json_schema mapping of a scalar the
// registry's catalog declares as JSON, so format --to=json writes it beside
// the name and primitive. The engine, which knows only the builtin catalog,
// then holds Ext.Doc to a JSON object instead of refusing it: its tests
// define and publish testdata/format/ext-json-scalar.schema.json, which this
// test keeps equal to what format writes (-update rewrites it). The JSON
// converts back to TypeScript with the extension's catalog, which owns the
// mapping; the core binary, which does not know the row, refuses it.
func TestFormatCommand_TSToJSONWritesAnExtensionJSONScalarsMapping(t *testing.T) {
	exts := []registry.Extension{extJSONScalars{}}
	out, err := runFormatCommandWith(t, exts, "--to=json", "--stdout",
		filepath.Join(tsTestdata, "fixture-ext-json-scalar/src/reading.schema.ts"))
	require.NoError(t, err)

	var doc struct {
		Scalars map[string]struct {
			LanguagePrimitive string            `json:"languagePrimitive"`
			TypeMappings      map[string]string `json:"typeMappings"`
		} `json:"scalars"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &doc))
	assert.Equal(t, "object", doc.Scalars["Ext.Doc"].LanguagePrimitive)
	assert.Equal(t, "object", doc.Scalars["Ext.Doc"].TypeMappings["json_schema"])
	assert.Equal(t, "any", doc.Scalars["Generic.JSON"].TypeMappings["json_schema"])

	golden := filepath.Join("testdata", "format", "ext-json-scalar.schema.json")
	if *updateFormat {
		require.NoError(t, os.MkdirAll(filepath.Dir(golden), 0o755))
		require.NoError(t, os.WriteFile(golden, []byte(out), 0o644))
	}
	want, err := os.ReadFile(golden)
	require.NoError(t, err, "run with -update")
	assert.Equal(t, string(want), out, "testdata/format/ext-json-scalar.schema.json is stale; run with -update")

	input := filepath.Join(t.TempDir(), "reading.schema.json")
	require.NoError(t, os.WriteFile(input, []byte(out), 0o644))
	ts, err := runFormatCommandWith(t, exts, "--to=ts", "--stdout", input)
	require.NoError(t, err)
	assert.Contains(t, ts, "payload: Ext.Doc;")

	_, err = runFormatCommand(t, "--to=ts", "--stdout", input)
	require.ErrorContains(t, err, "scalar Ext.Doc: declares metadata beyond its language primitive")
}
