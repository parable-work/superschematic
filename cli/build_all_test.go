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
	assert.Contains(t, errOut.String(), "psgen-profile service=fixture-db phase=build.load duration_ms=")
	assert.Contains(t, errOut.String(), "psgen-profile service=fixture-db phase=generator.output.types-go.generate duration_ms=")
	assert.Contains(t, errOut.String(), "psgen-profile service=fixture-db phase=generator.output.types-go.write duration_ms=")
	assert.Contains(t, errOut.String(), "psgen-profile service=fixture-db phase=generator.codegen.template duration_ms=")
	assert.Contains(t, errOut.String(), "psgen-profile service=fixture-db phase=generator.codegen.format duration_ms=")
	assert.Contains(t, errOut.String(), "psgen-profile service=fixture-db phase=generator.codegen.format.go duration_ms=")
	assert.Contains(t, errOut.String(), "psgen-profile service=fixture-db phase=generator.output.types-go.codegen.format.go duration_ms=")
	assert.Contains(t, errOut.String(), "psgen-profile service=fixture-db phase=generator.codegen.write duration_ms=")
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
// .deps.json graph is a build output, not a hook: the all-cached run builds
// nothing and fires no hook, and still rewrites the graph from dist.
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
	servicesRoot := prepareTSServicesRoot(t)
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
	servicesRoot := prepareTSServicesRoot(t)
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

func prepareTSServicesRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "schemas")
	servicesRoot := filepath.Join(root, "services")
	copyDir(t, "../internal/loader/tsreader/testdata/services/fixture-db", filepath.Join(servicesRoot, "fixture-db"))
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
