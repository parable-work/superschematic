package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// prepareDependencyTypesRoot copies the generator's dependency-types
// fixtures into a fresh schemas/services layout: shop-orders enables types
// in every language and imports types from shop-common and shop-db, which
// generate Go and TypeScript types only.
func prepareDependencyTypesRoot(t *testing.T) string {
	t.Helper()
	servicesRoot := filepath.Join(t.TempDir(), "schemas", "services")
	copyDir(t, "../internal/generator/testdata/dependency-types/services", servicesRoot)
	return servicesRoot
}

const shopOrdersDependencyTypesError = "shop-orders: shop-orders generates Python and Rust types, which use shop-common's Python and Rust types; enable outputs.types.python and outputs.types.rust in shop-common\n" +
	"shop-orders generates Python and Rust types, which use shop-db's Python and Rust types; enable outputs.types.python and outputs.types.rust in shop-db"

func TestBuildAllCommand_RefusesTypesWhoseDependencyTypesAreOff(t *testing.T) {
	servicesRoot := prepareDependencyTypesRoot(t)
	outDir := t.TempDir()
	root := New(Config{})
	root.SetOut(new(bytes.Buffer))
	root.SetErr(new(bytes.Buffer))
	root.SetArgs([]string{"build-all", servicesRoot, "--out", outDir})

	err := root.Execute()
	require.Error(t, err)
	assert.Equal(t, shopOrdersDependencyTypesError, err.Error())
	assert.DirExists(t, filepath.Join(outDir, "types", "go", "shop-common"))
	assert.NoDirExists(t, filepath.Join(outDir, "types", "rust", "shop-orders"))
	assert.NoDirExists(t, filepath.Join(outDir, "types", "go", "shop-orders"))

	// Enabling the languages in the dependencies is the fix the error names.
	for _, dep := range []string{"shop-common", "shop-db"} {
		path := filepath.Join(servicesRoot, dep, "schema.config.yaml")
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		data = append(data, "    python: { enabled: true }\n    rust: { enabled: true }\n"...)
		require.NoError(t, os.WriteFile(path, data, 0o644))
	}
	root.SetArgs([]string{"build-all", servicesRoot, "--out", outDir})
	require.NoError(t, root.Execute())
	assert.DirExists(t, filepath.Join(outDir, "types", "rust", "shop-orders"))
	assert.DirExists(t, filepath.Join(outDir, "types", "rust", "shop-common"))
	assert.NoDirExists(t, filepath.Join(outDir, "types", "rust", "shop-ids"), "shop-ids contributes only a scalar")
}

func TestBuildCommand_WithDepsRefusesTypesWhoseDependencyTypesAreOff(t *testing.T) {
	servicesRoot := prepareDependencyTypesRoot(t)
	root := New(Config{})
	root.SetOut(new(bytes.Buffer))
	root.SetErr(new(bytes.Buffer))
	root.SetArgs([]string{"build", "--with-deps", filepath.Join(servicesRoot, "shop-orders"), "--out", t.TempDir()})

	err := root.Execute()
	require.Error(t, err)
	assert.Equal(t, shopOrdersDependencyTypesError, err.Error())
}

// A single build does not read its dependencies' configs: it builds, and
// says which check it left to build --with-deps and build-all.
func TestBuildCommand_SaysItDoesNotCheckDependencyTypes(t *testing.T) {
	servicesRoot := prepareDependencyTypesRoot(t)
	outDir := t.TempDir()
	buf := new(bytes.Buffer)
	root := New(Config{})
	root.SetOut(buf)
	root.SetErr(new(bytes.Buffer))
	root.SetArgs([]string{"build", filepath.Join(servicesRoot, "shop-orders"), "--out", outDir})

	require.NoError(t, root.Execute())
	assert.Contains(t, buf.String(), "  - not checked: shop-common and shop-db must enable outputs.types.go, outputs.types.python, outputs.types.rust and outputs.types.typescript, since shop-orders's types import theirs; build --with-deps and build-all check it\n")
	assert.DirExists(t, filepath.Join(outDir, "types", "rust", "shop-orders"))
}

// An SDK whose language has no types output fails discovery, before any
// service is built.
func TestBuildAllCommand_RefusesAnSDKWithoutItsTypes(t *testing.T) {
	servicesRoot := prepareDependencyTypesRoot(t)
	config := `name: shop-orders
kind: API
dependencies:
  - name: shop-common
    kind: General
  - name: shop-db
    kind: DB
  - name: shop-ids
    kind: General
outputs:
  types:
    go: { enabled: true }
    typescript: { enabled: true }
  sdk:
    typescript: { enabled: true }
    rust: { enabled: true }
`
	require.NoError(t, os.WriteFile(filepath.Join(servicesRoot, "shop-orders", "schema.config.yaml"), []byte(config), 0o644))
	outDir := t.TempDir()
	root := New(Config{})
	root.SetOut(new(bytes.Buffer))
	root.SetErr(new(bytes.Buffer))
	root.SetArgs([]string{"build-all", servicesRoot, "--out", outDir})

	err := root.Execute()
	require.Error(t, err)
	assert.Equal(t, "schema config for shop-orders: outputs.sdk.rust needs outputs.types.rust: the Rust SDK depends on the Rust types crate and its methods take and return its types", err.Error())
	assert.NoDirExists(t, filepath.Join(outDir, "types", "go", "shop-common"), "nothing is built")
}
