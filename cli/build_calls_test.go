package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// prepareDepsServicesRoot copies the deps fixtures, a database, a bucket
// (D54) and two APIs that call each other, into a fresh repository's
// schemas/services and returns the repository root and the services root.
func prepareDepsServicesRoot(t *testing.T) (string, string) {
	t.Helper()
	repoRoot := t.TempDir()
	servicesRoot := filepath.Join(repoRoot, "schemas", "services")
	for _, fixture := range []string{"deps-db", "deps-media", "deps-catalog", "deps-orders"} {
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
// orders the closure the same way. deps-media, the bucket deps-orders
// lists, builds nothing and orders nothing (D54).
func TestBuildAllBuildsAPIsThatCallEachOther(t *testing.T) {
	_, servicesRoot := prepareDepsServicesRoot(t)

	out := runCLI(t, "build-all", servicesRoot, "--out", t.TempDir(), "--parallel")
	assert.Contains(t, out, "Phase 1/4: deps-db, deps-media\n")
	assert.Contains(t, out, "Phase 2/4: deps-catalog (base)\n")
	assert.Contains(t, out, "Phase 3/4: deps-orders\n")
	assert.Contains(t, out, "Phase 4/4: deps-catalog (server)\n")
	assert.Contains(t, out, "All 4 schema services built successfully")

	out = runCLI(t, "build-all", servicesRoot, "--out", t.TempDir())
	assert.Contains(t, out, "Base stage complete")
	assert.Contains(t, out, "Server stage complete")
	assert.Contains(t, out, "All 4 schema services built successfully")

	out = runCLI(t, "build", "--with-deps", filepath.Join(servicesRoot, "deps-orders"), "--out", t.TempDir())
	assert.Contains(t, out, "Resolved 4 schema services for deps-orders")
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

// TestAStackScaffoldsTheImplementationsItServes: a stack's build writes
// each server's entrypoint and the implementation of every API its
// servers serve, without --scaffold, and a cached run rebuilds the stack
// when one of those implementations is missing so it can write it again.
func TestAStackScaffoldsTheImplementationsItServes(t *testing.T) {
	repoRoot := t.TempDir()
	servicesRoot := filepath.Join(repoRoot, "schemas", "services")
	for _, fixture := range []string{"shop-db", "shop-media", "shop-api", "shop-orders", "shop-reviews", "shop-stack"} {
		copyDir(t, filepath.Join("../internal/generator/servergen/testdata/services", fixture), filepath.Join(servicesRoot, fixture))
	}
	cacheRoot := t.TempDir()
	reviews := filepath.Join(repoRoot, "go", "shop-reviews", "implementation.go")

	runCLI(t, "build-all", servicesRoot, "--cache", "--cache-root", cacheRoot)
	for _, server := range []string{"Storefront", "shop-api"} {
		assert.FileExists(t, filepath.Join(repoRoot, "schemas", "dist", "server", "shop-stack", server, "main.go"))
	}
	for _, service := range []string{"shop-api", "shop-orders", "shop-reviews"} {
		assert.FileExists(t, filepath.Join(repoRoot, "go", service, "implementation.go"))
		assert.FileExists(t, filepath.Join(repoRoot, "go", service, "go.mod"))
	}

	require.NoError(t, os.RemoveAll(filepath.Dir(reviews)))
	out := runCLI(t, "build-all", servicesRoot, "--cache", "--cache-root", cacheRoot)
	assert.FileExists(t, reviews)
	assert.Contains(t, out, "OK: shop-reviews (up to date")
	assert.Contains(t, out, "OK: shop-stack (built")
	assert.Contains(t, out, "implementation scaffold of shop-reviews written to "+filepath.Dir(reviews)+"\n")
}

// TestScaffoldWritesEachMissingTypeScriptImplementation: --scaffold writes
// a TypeScript API's implementation at typescript/{service} from the
// repository root, the output root is the Bun workspace that holds it
// (D51), and a cached run still builds a service whose implementation is
// missing so it can write it.
func TestScaffoldWritesEachMissingTypeScriptImplementation(t *testing.T) {
	repoRoot := t.TempDir()
	servicesRoot := filepath.Join(repoRoot, "schemas", "services")
	for _, fixture := range []string{"deps-db", "deps-ts-pricing", "deps-ts-shop"} {
		copyDir(t, filepath.Join("../internal/generator/testdata/services", fixture), filepath.Join(servicesRoot, fixture))
	}
	outDir := filepath.Join(repoRoot, "schemas", "dist")
	cacheRoot := t.TempDir()
	shop := filepath.Join(repoRoot, "typescript", "deps-ts-shop", "index.ts")
	pricing := filepath.Join(repoRoot, "typescript", "deps-ts-pricing", "index.ts")

	runCLI(t, "build-all", servicesRoot, "--out", outDir, "--cache", "--cache-root", cacheRoot)
	_, err := os.Stat(filepath.Join(repoRoot, "typescript"))
	require.True(t, os.IsNotExist(err), "build-all without --scaffold wrote %s", filepath.Join(repoRoot, "typescript"))
	manifest, err := os.ReadFile(filepath.Join(outDir, "package.json"))
	require.NoError(t, err)
	assert.Contains(t, string(manifest), `"../../typescript/*"`)

	out := runCLI(t, "build-all", servicesRoot, "--out", outDir, "--cache", "--cache-root", cacheRoot, "--scaffold")
	assert.FileExists(t, shop)
	assert.FileExists(t, pricing)
	assert.FileExists(t, filepath.Join(filepath.Dir(shop), "package.json"))
	assert.Contains(t, out, "OK: deps-db (up to date")

	require.NoError(t, os.RemoveAll(filepath.Dir(pricing)))
	out = runCLI(t, "build-all", servicesRoot, "--out", outDir, "--cache", "--cache-root", cacheRoot, "--scaffold")
	assert.FileExists(t, pricing)
	assert.Contains(t, out, "OK: deps-ts-shop (up to date")
	assert.Contains(t, out, "OK: deps-ts-pricing (built")
}
