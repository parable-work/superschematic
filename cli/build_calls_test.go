package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// prepareDepsServicesRoot copies the deps fixtures, a database and two
// APIs that call each other, into a fresh repository's schemas/services
// and returns the repository root and the services root.
func prepareDepsServicesRoot(t *testing.T) (string, string) {
	t.Helper()
	repoRoot := t.TempDir()
	servicesRoot := filepath.Join(repoRoot, "schemas", "services")
	for _, fixture := range []string{"deps-db", "deps-catalog", "deps-orders"} {
		copyDir(t, filepath.Join("../internal/generator/testdata/services", fixture), filepath.Join(servicesRoot, fixture))
	}
	return repoRoot, servicesRoot
}

func runCLI(t *testing.T, args ...string) string {
	t.Helper()
	out := new(bytes.Buffer)
	root := New(Config{})
	root.SetOut(out)
	root.SetErr(new(bytes.Buffer))
	root.SetArgs(args)
	require.NoError(t, root.Execute(), out.String())
	return out.String()
}

// TestBuildAllBuildsAPIsThatCallEachOther: two APIs that call each other
// build, the build plan ordering outputs: deps-catalog comes first, so it
// builds its base outputs, deps-orders builds whole once deps-catalog's
// SDK is there, and deps-catalog's server builds last. build --with-deps
// orders the closure the same way.
func TestBuildAllBuildsAPIsThatCallEachOther(t *testing.T) {
	_, servicesRoot := prepareDepsServicesRoot(t)

	out := runCLI(t, "build-all", servicesRoot, "--out", t.TempDir(), "--parallel")
	assert.Contains(t, out, "Phase 1/4: deps-db\n")
	assert.Contains(t, out, "Phase 2/4: deps-catalog (base)\n")
	assert.Contains(t, out, "Phase 3/4: deps-orders\n")
	assert.Contains(t, out, "Phase 4/4: deps-catalog (server)\n")
	assert.Contains(t, out, "All 3 schema services built successfully")

	out = runCLI(t, "build-all", servicesRoot, "--out", t.TempDir())
	assert.Contains(t, out, "Base stage complete")
	assert.Contains(t, out, "Server stage complete")
	assert.Contains(t, out, "All 3 schema services built successfully")

	out = runCLI(t, "build", "--with-deps", filepath.Join(servicesRoot, "deps-orders"), "--out", t.TempDir())
	assert.Contains(t, out, "Resolved 3 schema services for deps-orders")
	assert.Contains(t, out, "Server stage complete")
}

// TestScaffoldWritesEachMissingImplementation: --scaffold writes a Go
// API's implementation at go/{service} from the repository root, a build
// without it writes none, and a cached run still builds a service whose
// implementation is missing so it can write it, and leaves the others up
// to date.
func TestScaffoldWritesEachMissingImplementation(t *testing.T) {
	repoRoot, servicesRoot := prepareDepsServicesRoot(t)
	outDir := t.TempDir()
	cacheRoot := t.TempDir()
	orders := filepath.Join(repoRoot, "go", "deps-orders", "implementation.go")
	catalog := filepath.Join(repoRoot, "go", "deps-catalog", "implementation.go")

	runCLI(t, "build-all", servicesRoot, "--out", outDir, "--cache", "--cache-root", cacheRoot)
	_, err := os.Stat(filepath.Join(repoRoot, "go"))
	require.True(t, os.IsNotExist(err), "build-all without --scaffold wrote %s", filepath.Join(repoRoot, "go"))

	out := runCLI(t, "build-all", servicesRoot, "--out", outDir, "--cache", "--cache-root", cacheRoot, "--scaffold")
	assert.FileExists(t, orders)
	assert.FileExists(t, catalog)
	assert.Contains(t, out, "OK: deps-db (up to date")

	require.NoError(t, os.RemoveAll(filepath.Dir(catalog)))
	out = runCLI(t, "build-all", servicesRoot, "--out", outDir, "--cache", "--cache-root", cacheRoot, "--scaffold")
	assert.FileExists(t, catalog)
	assert.Contains(t, out, "OK: deps-orders (up to date")
	assert.Contains(t, out, "OK: deps-catalog (built")

	require.NoError(t, os.RemoveAll(filepath.Dir(orders)))
	out = runCLI(t, "build", filepath.Join(servicesRoot, "deps-orders"), "--out", t.TempDir(), "--scaffold")
	assert.Contains(t, out, "implementation scaffold written to "+filepath.Dir(orders)+"\n")
	assert.FileExists(t, orders)
}
