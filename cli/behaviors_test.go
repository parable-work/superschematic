package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/registry/registrytest"
)

func runCommandWith(t *testing.T, exts []registry.Extension, args ...string) (string, error) {
	t.Helper()
	buf := new(bytes.Buffer)
	root := New(Config{}, exts...)
	root.SetOut(buf)
	root.SetArgs(args)
	err := root.Execute()
	return buf.String(), err
}

// A service whose type composes a behavior loads, prints its IR and
// converts between the data forms; build refuses it, naming the generator
// that cannot render the behavior. The core binary refuses the behavior.
func TestBehaviorsThroughTheCommands(t *testing.T) {
	acme := []registry.Extension{registrytest.Acme{}}
	service := filepath.Join(acmeTestdata, "stock-json")

	out, err := runCommandWith(t, acme, "build", service, "--emit-ir", "--out", t.TempDir())
	require.NoError(t, err)
	var schema struct {
		Types map[string]struct {
			Behaviors []struct {
				Name   string          `json:"name"`
				Config json.RawMessage `json:"config"`
			} `json:"behaviors"`
		} `json:"types"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &schema))
	behaviors := schema.Types["Item"].Behaviors
	require.Len(t, behaviors, 2)
	assert.Equal(t, "acme.Stock", behaviors[0].Name)
	assert.JSONEq(t, `{"aisles": 3, "unit": "box"}`, string(behaviors[0].Config))
	assert.Equal(t, "acme.Audited", behaviors[1].Name)
	assert.Empty(t, behaviors[1].Config)

	_, err = runCommandWith(t, acme, "build", service, "--out", t.TempDir())
	require.ErrorContains(t, err, "generator: types does not render behaviors yet: type Item composes behavior acme.Stock")

	_, err = runCommandWith(t, nil, "build", service, "--emit-ir", "--out", t.TempDir())
	require.ErrorContains(t, err, `behavior "acme.Stock" on type "Item" is not a registered behavior (none are registered)`)

	yamlOut, err := runCommandWith(t, acme, "format", "--to=yaml", "--stdout", filepath.Join(service, "src/item.schema.json"))
	require.NoError(t, err)
	assert.Contains(t, yamlOut, "behaviors:\n      - name: acme.Stock\n        config:\n          aisles: 3\n          unit: box\n      - name: acme.Audited\n")

	schemaOut, err := runCommandWith(t, acme, "json-schema")
	require.NoError(t, err)
	var def struct {
		Defs struct {
			BehaviorRef struct {
				Properties struct {
					Name struct {
						Enum []string `json:"enum"`
					} `json:"name"`
				} `json:"properties"`
			} `json:"BehaviorRef"`
		} `json:"$defs"`
	}
	require.NoError(t, json.Unmarshal([]byte(schemaOut), &def))
	assert.Equal(t, []string{"acme.Audited", "acme.Stock"}, def.Defs.BehaviorRef.Properties.Name.Enum)
	compileSchema(t, []byte(schemaOut), "superschematic://schema-file.json")
}

// The behaviors command writes a canonical copy of each registered
// declaration, one the registry takes back unchanged; --check fails on a
// copy that differs, is missing or has no behavior, and a write repairs
// all three.
func TestBehaviorsCommand(t *testing.T) {
	acme := []registry.Extension{registrytest.Acme{}}
	out := filepath.Join(t.TempDir(), "declarations")

	log, err := runCommandWith(t, acme, "behaviors", "--out", out)
	require.NoError(t, err)
	assert.Equal(t, "behaviors: 2 declaration(s) in "+out+"\n", log)
	files, err := behaviorFiles(out)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"acme.Audited.behavior.json", "acme.Stock.behavior.json"}, sortedKeys(files))

	// The copy is canonical: the declaration's keys in order, schema keys
	// sorted, whatever the embedded file's layout.
	stock := string(files["acme.Stock.behavior.json"])
	assert.True(t, strings.HasPrefix(stock, "{\n  \"name\": \"acme.Stock\",\n  \"description\": \"Counts the units of an item on hand.\",\n  \"configSchema\": {\n    \"additionalProperties\": false,\n"), stock)
	assert.True(t, strings.HasSuffix(stock, "}\n"))
	assert.JSONEq(t, string(registrytest.StockBehavior), stock)
	assert.JSONEq(t, string(registrytest.AuditedBehavior), string(files["acme.Audited.behavior.json"]))

	// The registry takes the copy as the declaration it was made from.
	reg := registry.New(naming.Default())
	require.NoError(t, reg.RegisterBehavior(registry.BehaviorSpec{Extension: "acme", Declaration: files["acme.Stock.behavior.json"]}))
	copied, ok := reg.Behavior("acme.Stock")
	require.True(t, ok)
	written, err := behaviorDeclarationFile(copied.BehaviorDeclaration)
	require.NoError(t, err)
	assert.Equal(t, stock, string(written))

	log, err = runCommandWith(t, acme, "behaviors", "--out", out, "--check")
	require.NoError(t, err)
	assert.Equal(t, "behaviors: 2 declaration(s) in "+out+" are current\n", log)

	require.NoError(t, os.WriteFile(filepath.Join(out, "acme.Stock.behavior.json"), registrytest.StockBehavior, 0o644))
	require.NoError(t, os.Remove(filepath.Join(out, "acme.Audited.behavior.json")))
	require.NoError(t, os.WriteFile(filepath.Join(out, "acme.Gone.behavior.json"), []byte("{}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(out, "README.md"), []byte("not a declaration\n"), 0o644))
	_, err = runCommandWith(t, acme, "behaviors", "--out", out, "--check")
	require.EqualError(t, err, "behaviors: "+out+": acme.Audited.behavior.json is missing; acme.Stock.behavior.json differs from the registered declaration; "+
		"acme.Gone.behavior.json is no registered behavior's declaration; run: superschematic behaviors --out "+out)

	_, err = runCommandWith(t, acme, "behaviors", "--out", out)
	require.NoError(t, err)
	repaired, err := behaviorFiles(out)
	require.NoError(t, err)
	assert.Equal(t, files, repaired)
	_, err = os.Stat(filepath.Join(out, "README.md"))
	require.NoError(t, err, "only *.behavior.json files are the command's")
	_, err = runCommandWith(t, acme, "behaviors", "--out", out, "--check")
	require.NoError(t, err)
}

// --extension keeps one extension's behaviors, and names what the binary
// registers when that extension registers none; the core binary registers
// no behavior and writes nothing.
func TestBehaviorsCommand_Extension(t *testing.T) {
	acme := []registry.Extension{registrytest.Acme{}}
	out := t.TempDir()
	_, err := runCommandWith(t, acme, "behaviors", "--out", out, "--extension", "acme")
	require.NoError(t, err)
	files, err := behaviorFiles(out)
	require.NoError(t, err)
	assert.Len(t, files, 2)

	_, err = runCommandWith(t, acme, "behaviors", "--out", out, "--extension", "shop", "--check")
	require.EqualError(t, err, `behaviors: extension "shop" registers no behavior in this binary (registered: acme.Audited (extension acme), acme.Stock (extension acme))`)

	_, err = runCommandWith(t, acme, "behaviors", "--out", out, "--extension", "acme", "--check")
	require.NoError(t, err)

	core := filepath.Join(t.TempDir(), "core")
	log, err := runCommandWith(t, nil, "behaviors", "--out", core)
	require.NoError(t, err)
	assert.Equal(t, "behaviors: 0 declaration(s) in "+core+"\n", log)
	_, err = runCommandWith(t, nil, "behaviors", "--out", out, "--check")
	require.ErrorContains(t, err, "acme.Audited.behavior.json is no registered behavior's declaration")

	_, err = runCommandWith(t, nil, "behaviors")
	require.ErrorContains(t, err, `required flag(s) "out" not set`)
}
