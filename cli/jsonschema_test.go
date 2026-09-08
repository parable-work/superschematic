package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	validator "github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const loaderTestdata = "../internal/loader/testdata/services"

// compileSchema asserts the emitted bytes are a JSON Schema the validator
// can compile.
func compileSchema(t *testing.T, data []byte, name string) {
	t.Helper()
	resource, err := validator.UnmarshalJSON(bytes.NewReader(data))
	require.NoError(t, err)
	compiler := validator.NewCompiler()
	require.NoError(t, compiler.AddResource(name, resource))
	_, err = compiler.Compile(name)
	require.NoError(t, err)
}

func TestJSONSchemaCommand(t *testing.T) {
	buf := new(bytes.Buffer)
	root := New(Config{})
	root.SetOut(buf)
	root.SetArgs([]string{"json-schema"})

	require.NoError(t, root.Execute())

	var doc map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &doc))
	assert.Contains(t, doc, "oneOf")
	assert.Contains(t, doc, "$defs")
	compileSchema(t, buf.Bytes(), "psgen://schema-file.json")
}

func TestJSONSchemaCommand_Config(t *testing.T) {
	buf := new(bytes.Buffer)
	root := New(Config{})
	root.SetOut(buf)
	root.SetArgs([]string{"json-schema", "--config"})

	require.NoError(t, root.Execute())

	var doc map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &doc))
	assert.Equal(t, "psgen://schema-config.schema.json", doc["$id"])
	compileSchema(t, buf.Bytes(), "psgen://schema-config.schema.json")
}

func TestBuildCommand_JSONService(t *testing.T) {
	buf := new(bytes.Buffer)
	root := New(Config{})
	root.SetOut(buf)
	root.SetArgs([]string{"build", filepath.Join(loaderTestdata, "fixture-db-json"), "--out", t.TempDir()})

	require.NoError(t, root.Execute())
	assert.Contains(t, buf.String(), "Loaded schema fixture-db (kind DB)")
}

func TestBuildCommand_YAMLService(t *testing.T) {
	buf := new(bytes.Buffer)
	root := New(Config{})
	root.SetOut(buf)
	root.SetArgs([]string{"build", filepath.Join(loaderTestdata, "fixture-general-yaml"), "--out", t.TempDir()})

	require.NoError(t, root.Execute())
	assert.Contains(t, buf.String(), "Loaded schema fixture-general (kind General)")
}
