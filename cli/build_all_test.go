package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/internal/sentinel"
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

// TestBuildAllCommand_CachedRunWritesTypeScriptWorkspaceRoot: the manifest
// that makes the generated TypeScript types packages one Bun workspace
// belongs to no single service, so a restore from the cache does not bring
// it back. A run that builds nothing writes it anyway.
func TestBuildAllCommand_CachedRunWritesTypeScriptWorkspaceRoot(t *testing.T) {
	servicesRoot := prepareJSONServicesRoot(t)
	outDir := t.TempDir()
	cacheRoot := t.TempDir()
	typesRoot := filepath.Join(outDir, "types", "typescript")
	manifest := filepath.Join(typesRoot, "package.json")
	run := func() string {
		t.Helper()
		out := new(bytes.Buffer)
		root := New(Config{})
		root.SetOut(out)
		root.SetErr(new(bytes.Buffer))
		root.SetArgs([]string{"build-all", servicesRoot, "--out", outDir, "--cache", "--cache-root", cacheRoot})
		require.NoError(t, root.Execute())
		return out.String()
	}

	run()
	want, err := os.ReadFile(manifest)
	require.NoError(t, err, "the first build-all wrote no workspace manifest")

	require.NoError(t, os.Remove(manifest))
	out := run()
	assert.Contains(t, out, "OK: fixture-db (up to date")
	got, err := os.ReadFile(manifest)
	require.NoError(t, err, "an up-to-date build-all did not write the workspace manifest")
	assert.Equal(t, string(want), string(got))

	require.NoError(t, os.RemoveAll(filepath.Join(outDir, "types")))
	out = run()
	assert.Contains(t, out, "OK: fixture-db (restored from cache")
	assert.NotContains(t, out, "(built")
	got, err = os.ReadFile(manifest)
	require.NoError(t, err, "a build-all that restored every service did not write the workspace manifest")
	assert.Equal(t, string(want), string(got))
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

// TestBuildAllCommand_AliasedConfigImport: a distribution republishes the
// config package under its own name and maps that name onto it in
// [package_aliases]. build-all and build --with-deps discover services whose
// schema.config.ts imports the aliased name, and the sentinels build-all
// writes import it too.
func TestBuildAllCommand_AliasedConfigImport(t *testing.T) {
	servicesRoot := prepareTSServicesRoot(t, "fixture-db", "fixture-api")
	aliasConfigImports(t, servicesRoot, "@acme/schema-config", "fixture-db", "fixture-api")

	out := new(bytes.Buffer)
	root := New(Config{})
	root.SetOut(out)
	root.SetErr(new(bytes.Buffer))
	root.SetArgs([]string{"build-all", servicesRoot, "--out", t.TempDir()})
	require.NoError(t, root.Execute())
	assert.Contains(t, out.String(), "Discovered 2 schema services: fixture-db, fixture-api")
	generated, err := os.ReadFile(filepath.Join(servicesRoot, "fixture-db", "src", "service.generated.ts"))
	require.NoError(t, err)
	assert.Contains(t, string(generated), `from "@acme/schema-config"`)

	out.Reset()
	root = New(Config{})
	root.SetOut(out)
	root.SetErr(new(bytes.Buffer))
	root.SetArgs([]string{"build", "--with-deps", filepath.Join(servicesRoot, "fixture-api"), "--out", t.TempDir()})
	require.NoError(t, root.Execute())
	assert.Contains(t, out.String(), "Resolved 2 schema services for fixture-api: fixture-db, fixture-api")
}

// aliasConfigImports rewrites each service's schema.config.ts to import the
// config package as alias, resolves alias to the same sources in the base
// tsconfig, and maps it onto the config package in the schemas root's
// naming file.
func aliasConfigImports(t *testing.T, servicesRoot string, alias string, services ...string) {
	t.Helper()
	schemasRoot := filepath.Dir(servicesRoot)
	basePath := filepath.Join(schemasRoot, "tsconfig.base.json")
	data, err := os.ReadFile(basePath)
	require.NoError(t, err)
	var base map[string]any
	require.NoError(t, json.Unmarshal(data, &base))
	paths := base["compilerOptions"].(map[string]any)["paths"].(map[string]any)
	paths[alias] = paths[sentinel.ConfigPackage]
	data, err = json.MarshalIndent(base, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(basePath, data, 0o644))

	for _, service := range services {
		path := filepath.Join(servicesRoot, service, "schema.config.ts")
		source, err := os.ReadFile(path)
		require.NoError(t, err)
		rewritten := strings.ReplaceAll(string(source), `from "`+sentinel.ConfigPackage+`"`, `from "`+alias+`"`)
		require.NotEqual(t, string(source), rewritten, "%s imports no config package", path)
		require.NoError(t, os.WriteFile(path, []byte(rewritten), 0o644))
	}

	toml := fmt.Sprintf("[package_aliases]\n%q = %q\n", alias, sentinel.ConfigPackage)
	require.NoError(t, os.WriteFile(filepath.Join(schemasRoot, "superschematic.toml"), []byte(toml), 0o644))
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
