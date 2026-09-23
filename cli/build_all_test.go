package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/schemadeps"
)

func TestBuildAllCommand_ProfileJSONService(t *testing.T) {
	servicesRoot := prepareJSONServicesRoot(t)
	outDir := t.TempDir()
	out := new(bytes.Buffer)
	errOut := new(bytes.Buffer)
	root := New(Config{})
	root.SetOut(out)
	root.SetErr(errOut)
	root.SetArgs([]string{"build-all", servicesRoot, "--out", outDir, "--profile"})

	err := root.Execute()
	require.NoError(t, err)
	assert.Contains(t, out.String(), "Discovered 1 schema services: fixture-db")
	assert.Contains(t, out.String(), "All 1 schema services built successfully")
	assert.Contains(t, out.String(), "Profile summary (top cumulative phases):")
	assert.Contains(t, errOut.String(), "superschematic-profile service=fixture-db phase=build.load duration_ms=")
	assert.Contains(t, errOut.String(), "superschematic-profile service=fixture-db phase=generator.output.types-go.generate duration_ms=")
	assert.Contains(t, errOut.String(), "superschematic-profile service=fixture-db phase=generator.output.types-go.write duration_ms=")
	assert.Contains(t, errOut.String(), "superschematic-profile service=fixture-db phase=generator.codegen.template duration_ms=")
	assert.Contains(t, errOut.String(), "superschematic-profile service=fixture-db phase=generator.codegen.format duration_ms=")
	assert.Contains(t, errOut.String(), "superschematic-profile service=fixture-db phase=generator.codegen.format.go duration_ms=")
	assert.Contains(t, errOut.String(), "superschematic-profile service=fixture-db phase=generator.output.types-go.codegen.format.go duration_ms=")
	assert.Contains(t, errOut.String(), "superschematic-profile service=fixture-db phase=generator.codegen.write duration_ms=")
	assert.DirExists(t, filepath.Join(outDir, "types", "go", "fixture-db"))
}

func TestBuildAllCommand_SkipFormatSuppressesFormatterProfiles(t *testing.T) {
	servicesRoot := prepareJSONServicesRoot(t)
	outDir := t.TempDir()
	out := new(bytes.Buffer)
	errOut := new(bytes.Buffer)
	root := New(Config{})
	root.SetOut(out)
	root.SetErr(errOut)
	root.SetArgs([]string{"build-all", servicesRoot, "--out", outDir, "--profile", "--skip-format"})

	err := root.Execute()
	require.NoError(t, err)
	assert.Contains(t, out.String(), "All 1 schema services built successfully")
	assert.Contains(t, errOut.String(), "phase=generator.codegen.template")
	assert.Contains(t, errOut.String(), "phase=generator.codegen.write")
	assert.NotContains(t, errOut.String(), "phase=generator.codegen.format")
	assert.NotContains(t, errOut.String(), ".codegen.format")
	assert.DirExists(t, filepath.Join(outDir, "types", "go", "fixture-db"))
}

// TestBuildAllCommand_CacheSkipsUpToDateService also pins that the
// .deps.json graph is written from dist: the all-cached run builds nothing
// and still rewrites it.
func TestBuildAllCommand_CacheSkipsUpToDateService(t *testing.T) {
	servicesRoot := prepareJSONServicesRoot(t)
	outDir := t.TempDir()
	cacheRoot := t.TempDir()

	root := New(Config{})
	root.SetOut(new(bytes.Buffer))
	root.SetErr(new(bytes.Buffer))
	root.SetArgs([]string{"build-all", servicesRoot, "--out", outDir, "--cache", "--cache-root", cacheRoot})
	require.NoError(t, root.Execute())

	depsPath := schemadeps.DepsPath(outDir)
	firstDeps, err := os.ReadFile(depsPath)
	require.NoError(t, err, "first build-all wrote no %s", schemadeps.DepsFileName)
	assert.Contains(t, string(firstDeps), "fixture-db")
	require.NoError(t, os.Remove(depsPath))

	out := new(bytes.Buffer)
	root.SetOut(out)
	root.SetErr(new(bytes.Buffer))
	root.SetArgs([]string{"build-all", servicesRoot, "--out", outDir, "--cache", "--cache-root", cacheRoot})

	require.NoError(t, root.Execute())
	assert.Contains(t, out.String(), "OK: fixture-db (up to date")
	assert.NotContains(t, out.String(), "(built")
	assert.Contains(t, out.String(), "All 1 schema services built successfully")
	secondDeps, err := os.ReadFile(depsPath)
	require.NoError(t, err, "all-cached build-all did not rewrite %s", schemadeps.DepsFileName)
	assert.Equal(t, string(firstDeps), string(secondDeps))
}

// TestBuildAllCommand_DepsCopyFlag: --deps-copy writes the graph a second
// time, byte for byte, and every package in it names the service that
// produced it.
func TestBuildAllCommand_DepsCopyFlag(t *testing.T) {
	servicesRoot := prepareJSONServicesRoot(t)
	outDir := t.TempDir()
	copyPath := filepath.Join(t.TempDir(), "graph", "deps.json")
	out := new(bytes.Buffer)
	root := New(Config{})
	root.SetOut(out)
	root.SetErr(new(bytes.Buffer))
	root.SetArgs([]string{"build-all", servicesRoot, "--out", outDir, "--deps-copy", copyPath})

	require.NoError(t, root.Execute())
	assert.Contains(t, out.String(), "Wrote "+copyPath)
	distBytes, err := os.ReadFile(schemadeps.DepsPath(outDir))
	require.NoError(t, err)
	copyBytes, err := os.ReadFile(copyPath)
	require.NoError(t, err)
	assert.Equal(t, string(distBytes), string(copyBytes))

	graph, err := schemadeps.Read(copyPath)
	require.NoError(t, err)
	require.NotEmpty(t, graph.Packages)
	for _, pkg := range graph.Packages {
		assert.Equal(t, "fixture-db", pkg.Service, "%s/%s at %s", pkg.Language, pkg.ID, pkg.Path)
	}
}

// TestBuildAllCommand_DepsCopyFromNaming: [deps] copy is relative to the
// repository root, the parent of the schemas root.
func TestBuildAllCommand_DepsCopyFromNaming(t *testing.T) {
	servicesRoot := prepareJSONServicesRoot(t)
	schemasRoot := filepath.Dir(servicesRoot)
	require.NoError(t, os.WriteFile(filepath.Join(schemasRoot, "superschematic.toml"), []byte("[deps]\ncopy = \"schemas/deps.json\"\n"), 0o644))
	outDir := t.TempDir()
	root := New(Config{})
	root.SetOut(new(bytes.Buffer))
	root.SetErr(new(bytes.Buffer))
	root.SetArgs([]string{"build-all", servicesRoot, "--out", outDir})

	require.NoError(t, root.Execute())
	distBytes, err := os.ReadFile(schemadeps.DepsPath(outDir))
	require.NoError(t, err)
	copyBytes, err := os.ReadFile(filepath.Join(schemasRoot, "deps.json"))
	require.NoError(t, err, "[deps] copy was not written")
	assert.Equal(t, string(distBytes), string(copyBytes))
	assert.Contains(t, string(copyBytes), `"service": "fixture-db"`)
}

// TestBuildAllCommand_UnownedPackageFails: a package directory under the
// output root that no discovered service writes, such as one left by a
// removed service, fails the build and is named; the graph is not written.
func TestBuildAllCommand_UnownedPackageFails(t *testing.T) {
	servicesRoot := prepareJSONServicesRoot(t)
	outDir := t.TempDir()
	stale := filepath.Join(outDir, "types", "typescript", "removed")
	require.NoError(t, os.MkdirAll(stale, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stale, "package.json"), []byte(`{"name": "@schemas/removed-types"}`), 0o644))
	root := New(Config{})
	root.SetOut(new(bytes.Buffer))
	root.SetErr(new(bytes.Buffer))
	root.SetArgs([]string{"build-all", servicesRoot, "--out", outDir})

	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not produced by any schema service")
	assert.Contains(t, err.Error(), "typescript/removed-types at types/typescript/removed")
	assert.NoFileExists(t, schemadeps.DepsPath(outDir))
}

func TestBuildAllCommand_UncachedBuildWritesStamps(t *testing.T) {
	servicesRoot := prepareJSONServicesRoot(t)
	schemasRoot := filepath.Dir(servicesRoot)
	stamp := filepath.Join(schemasRoot, "dist", ".build-stamps", "fixture-db")

	root := New(Config{})
	root.SetOut(new(bytes.Buffer))
	root.SetErr(new(bytes.Buffer))
	root.SetArgs([]string{"build-all", servicesRoot})
	require.NoError(t, root.Execute())

	data, err := os.ReadFile(stamp)
	require.NoError(t, err, "a build without --cache must still write the service stamp")
	first := strings.TrimSpace(string(data))
	assert.Regexp(t, `^[0-9a-f]{64}$`, first)

	// The next uncached run clears the stamps and writes the same hash for
	// the same inputs.
	root.SetArgs([]string{"build-all", servicesRoot})
	require.NoError(t, root.Execute())
	data, err = os.ReadFile(stamp)
	require.NoError(t, err)
	assert.Equal(t, first, strings.TrimSpace(string(data)))
}

func TestBuildAllCommand_CacheRestoresEmptyStampedOutput(t *testing.T) {
	servicesRoot := prepareJSONServicesRoot(t)
	outDir := t.TempDir()
	cacheRoot := t.TempDir()

	root := New(Config{})
	root.SetOut(new(bytes.Buffer))
	root.SetErr(new(bytes.Buffer))
	root.SetArgs([]string{"build-all", servicesRoot, "--out", outDir, "--cache", "--cache-root", cacheRoot})
	require.NoError(t, root.Execute())

	goOutput := filepath.Join(outDir, "types", "go", "fixture-db")
	require.NoError(t, os.RemoveAll(goOutput))
	require.NoError(t, os.MkdirAll(goOutput, 0o755))

	out := new(bytes.Buffer)
	root.SetOut(out)
	root.SetErr(new(bytes.Buffer))
	root.SetArgs([]string{"build-all", servicesRoot, "--out", outDir, "--cache", "--cache-root", cacheRoot})

	require.NoError(t, root.Execute())
	assert.Contains(t, out.String(), "OK: fixture-db (restored from cache)")
	assert.FileExists(t, filepath.Join(goOutput, "go.mod"))
}

func TestBuildAllCommand_UsesSharedTypeScriptProgramByDefault(t *testing.T) {
	servicesRoot := prepareTSServicesRoot(t, "fixture-db")
	outDir := t.TempDir()
	out := new(bytes.Buffer)
	errOut := new(bytes.Buffer)
	root := New(Config{})
	root.SetOut(out)
	root.SetErr(errOut)
	root.SetArgs([]string{"build-all", servicesRoot, "--out", outDir, "--profile"})

	require.NoError(t, root.Execute())
	assert.Contains(t, out.String(), "Shared TypeScript program: 1 service(s)")
	assert.Contains(t, errOut.String(), "phase=tsreader.program.workspace-create")
	assert.NotContains(t, errOut.String(), "phase=tsreader.program.create")
}

func TestBuildAllCommand_IsolatedTypeScriptProgramsFallback(t *testing.T) {
	servicesRoot := prepareTSServicesRoot(t, "fixture-db")
	outDir := t.TempDir()
	out := new(bytes.Buffer)
	errOut := new(bytes.Buffer)
	root := New(Config{})
	root.SetOut(out)
	root.SetErr(errOut)
	root.SetArgs([]string{"build-all", servicesRoot, "--out", outDir, "--profile", "--isolated-ts-programs"})

	require.NoError(t, root.Execute())
	assert.NotContains(t, out.String(), "Shared TypeScript program")
	assert.Contains(t, errOut.String(), "phase=tsreader.program.create")
	assert.NotContains(t, errOut.String(), "phase=tsreader.program.workspace-create")
}

func prepareJSONServicesRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "schemas")
	servicesRoot := filepath.Join(root, "services")
	copyDir(t, "../internal/loader/testdata/services/fixture-db-json", filepath.Join(servicesRoot, "fixture-db-json"))
	return servicesRoot
}

// prepareTSServicesRoot copies the named tsreader fixtures into a fresh
// schemas/services layout with a base tsconfig whose authoring-package and
// superscalar paths point back at this checkout.
func prepareTSServicesRoot(t *testing.T, fixtures ...string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "schemas")
	servicesRoot := filepath.Join(root, "services")
	for _, fixture := range fixtures {
		copyDir(t, filepath.Join(tsreaderTestdata, fixture), filepath.Join(servicesRoot, fixture))
	}
	baseConfig, err := os.ReadFile("../internal/loader/tsreader/testdata/tsconfig.base.json")
	require.NoError(t, err)
	packagesRoot, err := filepath.Abs("../packages")
	require.NoError(t, err)
	// superscalar resolves to the pinned checkout under third_party, not to a
	// packages member.
	scalarsRoot, err := filepath.Abs("../third_party/superscalar")
	require.NoError(t, err)
	baseConfigText := strings.ReplaceAll(string(baseConfig), "../../../../third_party/superscalar", filepath.ToSlash(scalarsRoot))
	baseConfigText = strings.ReplaceAll(baseConfigText, "../../../../packages", filepath.ToSlash(packagesRoot))
	require.NoError(t, os.WriteFile(filepath.Join(root, "tsconfig.base.json"), []byte(baseConfigText), 0o644))
	return servicesRoot
}

func copyDir(t *testing.T, src string, dst string) {
	t.Helper()
	entries, err := os.ReadDir(src)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dst, 0o755))
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			copyDir(t, srcPath, dstPath)
			continue
		}
		data, err := os.ReadFile(srcPath)
		require.NoError(t, err)
		info, err := entry.Info()
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(dstPath, data, info.Mode()))
	}
}
