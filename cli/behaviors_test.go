package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
