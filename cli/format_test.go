package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const tsTestdata = "../internal/loader/tsreader/testdata/services"

// runFormatCommand executes the format command and returns its stdout.
func runFormatCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	buf := new(bytes.Buffer)
	root := New(Config{})
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
	assert.Contains(t, out, `import { Generic, Identity, Temporal } from "@psgen/scalar-lib";`)
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
