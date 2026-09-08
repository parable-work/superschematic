package buildcache

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/internal/buildplan"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	ir "github.com/parable-work/superschematic/ir"
)

func writeFile(t *testing.T, path string, contents string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o644))
}

func TestTreeDigestDeterministicAndSkipsArtifacts(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "one")
	writeFile(t, filepath.Join(root, "sub", "b.txt"), "two")

	first := TreeDigest(root)
	assert.Equal(t, first, TreeDigest(root))

	writeFile(t, filepath.Join(root, "node_modules", "dep.js"), "junk")
	writeFile(t, filepath.Join(root, "dist", "out.js"), "junk")
	assert.Equal(t, first, TreeDigest(root))

	writeFile(t, filepath.Join(root, "a.txt"), "changed")
	assert.NotEqual(t, first, TreeDigest(root))
}

func TestStoreRestoreRoundTrip(t *testing.T) {
	base := t.TempDir()
	repoA := filepath.Join(base, "repo-a")
	repoB := filepath.Join(base, "repo-b")
	cacheRoot := filepath.Join(base, "cache")
	rel := filepath.ToSlash(filepath.Join("platform-schemas", "dist", "types", "typescript", "demo"))
	out := filepath.Join(repoA, filepath.FromSlash(rel))

	writeFile(t, filepath.Join(out, "src", "index.ts"), "export const x = 1;")
	writeFile(t, filepath.Join(out, "node_modules", "dep", "index.js"), "module.exports=1")
	writeFile(t, filepath.Join(out, "Cargo.toml"), "[package]")
	writeFile(t, filepath.Join(out, "target", "debug", "junk.o"), "binary")
	require.NoError(t, os.Symlink(filepath.Join(repoA, "vendor", "scalars"), filepath.Join(out, "node_modules", "linked-pkg")))
	require.NoError(t, os.Symlink("../dep/index.js", filepath.Join(out, "node_modules", "rel-link")))

	require.NoError(t, StoreEntry(cacheRoot, "schemas", "demo", stringsOf("f", 64), repoA, []string{rel}))
	entry := FindEntry(cacheRoot, "schemas", "demo", stringsOf("f", 64))
	require.NotEmpty(t, entry)
	assert.NoDirExists(t, filepath.Join(entry, "outputs", "0", "target"))
	assert.True(t, sameInode(t, filepath.Join(out, "node_modules", "dep", "index.js"), filepath.Join(entry, "outputs", "0", "node_modules", "dep", "index.js")))

	_, err := RestoreEntry(entry, repoB)
	require.NoError(t, err)
	restored := filepath.Join(repoB, filepath.FromSlash(rel))
	assert.Equal(t, "export const x = 1;", mustRead(t, filepath.Join(restored, "src", "index.ts")))
	assert.Equal(t, filepath.Join(repoB, "vendor", "scalars"), mustReadlink(t, filepath.Join(restored, "node_modules", "linked-pkg")))
	assert.Equal(t, "../dep/index.js", mustReadlink(t, filepath.Join(restored, "node_modules", "rel-link")))
	assert.NoDirExists(t, filepath.Join(restored, "target"))
}

func TestRestoreRejectsDamagedEntry(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	cacheRoot := filepath.Join(base, "cache")
	rel := filepath.ToSlash(filepath.Join("platform-schemas", "dist", "types", "go", "demo"))
	writeFile(t, filepath.Join(repo, filepath.FromSlash(rel), "types.go"), "package demo")

	require.NoError(t, StoreEntry(cacheRoot, "schemas", "demo", stringsOf("a", 64), repo, []string{rel}))
	entry := FindEntry(cacheRoot, "schemas", "demo", stringsOf("a", 64))
	require.NotEmpty(t, entry)
	require.NoError(t, os.Remove(filepath.Join(entry, "outputs", "0", "types.go")))

	_, err := RestoreEntry(entry, filepath.Join(base, "other-repo"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "damaged")
}

func TestComputeInputHashesPropagatesDependencyChanges(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "utils", "psgen", "main.go"), "package main")
	writeFile(t, filepath.Join(repo, "vendor", "scalars", "permissions.yml"), "{}")
	writeFile(t, filepath.Join(repo, "platform-schemas", "package.json"), "{}")
	writeFile(t, filepath.Join(repo, "platform-schemas", "bun.lock"), "")

	baseDir := filepath.Join(repo, "platform-schemas", "services", "base")
	leafDir := filepath.Join(repo, "platform-schemas", "services", "leaf")
	otherDir := filepath.Join(repo, "platform-schemas", "services", "other")
	writeFile(t, filepath.Join(baseDir, "src", "base.schema.ts"), "export class Base {}")
	writeFile(t, filepath.Join(leafDir, "src", "leaf.schema.ts"), "export class Leaf {}")
	writeFile(t, filepath.Join(otherDir, "src", "other.schema.ts"), "export class Other {}")

	services := []buildplan.Service{
		{Name: "base", Dir: baseDir, Config: &schemaconfig.SchemaConfig{Name: "base", Kind: ir.SchemaKindGeneral}},
		{Name: "other", Dir: otherDir, Config: &schemaconfig.SchemaConfig{Name: "other", Kind: ir.SchemaKindGeneral}},
		{
			Name: "leaf",
			Dir:  leafDir,
			Config: &schemaconfig.SchemaConfig{
				Name:         "leaf",
				Kind:         ir.SchemaKindGeneral,
				Dependencies: []schemaconfig.ServiceDependency{{Name: "base", Kind: ir.SchemaKindGeneral}},
			},
		},
	}

	before, err := ComputeInputHashes(services, repo, naming.Naming{})
	require.NoError(t, err)
	writeFile(t, filepath.Join(baseDir, "src", "base.schema.ts"), "export class Base2 {}")
	after, err := ComputeInputHashes(services, repo, naming.Naming{})
	require.NoError(t, err)

	assert.NotEqual(t, before["base"], after["base"])
	assert.NotEqual(t, before["leaf"], after["leaf"])
	assert.Equal(t, before["other"], after["other"])
}

func TestAuthoringImportsInvalidateInputHash(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "utils", "psgen", "main.go"), "package main")
	writeFile(t, filepath.Join(repo, "vendor", "scalars", "permissions.yml"), "{}")
	writeFile(t, filepath.Join(repo, "platform-schemas", "package.json"), "{}")
	writeFile(t, filepath.Join(repo, "platform-schemas", "bun.lock"), "")

	envDir := filepath.Join(repo, "platform-schemas", "services", "env")
	modelDir := filepath.Join(repo, "platform-schemas", "services", "model")
	writeFile(t, filepath.Join(envDir, "deploy.values.ts"), "export default {}")
	modelFile := filepath.Join(modelDir, "workloads.ts")
	writeFile(t, modelFile, "export const a = 1")
	ownFile := filepath.Join(envDir, "helper.ts")
	writeFile(t, ownFile, "export const b = 2")
	outsideFile := filepath.Join(repo, "utils", "psgen", "packages", "deploy", "index.ts")
	writeFile(t, outsideFile, "export const c = 3")

	services := []buildplan.Service{
		{Name: "env", Dir: envDir, Config: &schemaconfig.SchemaConfig{Name: "env", Kind: ir.SchemaKindGeneral}},
	}
	distRoot := filepath.Join(repo, "platform-schemas", "dist")

	// The depfile records only platform-schemas files outside the service
	// directory: the service's own files and utils/psgen sources are covered
	// by the service and toolchain digests.
	require.NoError(t, WriteAuthoringImports(distRoot, "env", envDir, []string{modelFile, ownFile, outsideFile}, true))
	assert.Equal(t, []string{"platform-schemas/services/model/workloads.ts"}, ReadAuthoringImports(repo, "env"))

	before, err := ComputeInputHashes(services, repo, naming.Naming{})
	require.NoError(t, err)
	writeFile(t, modelFile, "export const a = 2")
	after, err := ComputeInputHashes(services, repo, naming.Naming{})
	require.NoError(t, err)
	assert.NotEqual(t, before["env"], after["env"], "editing a recorded authoring import must invalidate the hash")

	// A schema without deploy documents removes its stale depfile, and the
	// hash returns to the import-free form.
	require.NoError(t, WriteAuthoringImports(distRoot, "env", envDir, nil, false))
	assert.Nil(t, ReadAuthoringImports(repo, "env"))
	writeFile(t, modelFile, "export const a = 3")
	first, err := ComputeInputHashes(services, repo, naming.Naming{})
	require.NoError(t, err)
	writeFile(t, modelFile, "export const a = 4")
	second, err := ComputeInputHashes(services, repo, naming.Naming{})
	require.NoError(t, err)
	assert.Equal(t, first["env"], second["env"], "without a depfile, model edits must not affect the hash")
}

// TestComputeInputHashesFollowNaming: the resolved naming is part of the key,
// so a build under --naming X cannot reuse an entry built under the defaults.
// A zero Naming and an explicit Default() hash alike: a missing
// superschematic.toml and one that spells out the defaults are the same
// build.
func TestComputeInputHashesFollowNaming(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "utils", "psgen", "main.go"), "package main")
	svcDir := filepath.Join(repo, "platform-schemas", "services", "svc")
	writeFile(t, filepath.Join(svcDir, "src", "svc.schema.ts"), "export class Svc {}")
	services := []buildplan.Service{
		{Name: "svc", Dir: svcDir, Config: &schemaconfig.SchemaConfig{Name: "svc", Kind: ir.SchemaKindGeneral}},
	}

	zero, err := ComputeInputHashes(services, repo, naming.Naming{})
	require.NoError(t, err)
	defaults, err := ComputeInputHashes(services, repo, naming.Default())
	require.NoError(t, err)
	assert.Equal(t, zero["svc"], defaults["svc"], "zero and explicit default naming must hash alike")

	acme, err := ComputeInputHashes(services, repo, naming.Naming{NpmScope: "@acme"})
	require.NoError(t, err)
	assert.NotEqual(t, zero["svc"], acme["svc"], "a different npm scope must change the key")

	withExt, err := ComputeInputHashes(services, repo, naming.Naming{Extensions: map[string]map[string]any{"acme": {"k": "v"}}})
	require.NoError(t, err)
	assert.NotEqual(t, zero["svc"], withExt["svc"], "extension tables reach generators through the registry and must change the key")
}

// [cache] inputs are the only way a file outside the schema tree reaches the
// key: with none declared, permissions.yml edits do not invalidate; with the
// path declared, they do, and so does declaring the path itself.
func TestComputeInputHashesFollowCacheInputs(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "utils", "psgen", "main.go"), "package main")
	perms := filepath.Join(repo, "vendor", "scalars", "permissions.yml")
	writeFile(t, perms, "a: 1")
	svcDir := filepath.Join(repo, "platform-schemas", "services", "svc")
	writeFile(t, filepath.Join(svcDir, "src", "svc.schema.ts"), "export class Svc {}")
	services := []buildplan.Service{
		{Name: "svc", Dir: svcDir, Config: &schemaconfig.SchemaConfig{Name: "svc", Kind: ir.SchemaKindGeneral}},
	}
	declared := naming.Naming{Cache: naming.CacheConfig{Inputs: []string{"vendor/scalars/permissions.yml"}}}

	plain, err := ComputeInputHashes(services, repo, naming.Naming{})
	require.NoError(t, err)
	withInput, err := ComputeInputHashes(services, repo, declared)
	require.NoError(t, err)
	assert.NotEqual(t, plain["svc"], withInput["svc"], "declaring an input must change the key")

	writeFile(t, perms, "a: 2")
	plainAfter, err := ComputeInputHashes(services, repo, naming.Naming{})
	require.NoError(t, err)
	withInputAfter, err := ComputeInputHashes(services, repo, declared)
	require.NoError(t, err)
	assert.Equal(t, plain["svc"], plainAfter["svc"], "an undeclared file must not affect the key")
	assert.NotEqual(t, withInput["svc"], withInputAfter["svc"], "editing a declared input must invalidate the key")

	require.NoError(t, os.Remove(perms))
	missing, err := ComputeInputHashes(services, repo, declared)
	require.NoError(t, err)
	assert.NotEqual(t, withInputAfter["svc"], missing["svc"], "a missing declared input hashes as missing, not as its last contents")
}

func TestDefaultRootPrecedence(t *testing.T) {
	t.Setenv("SUPERSCHEMATIC_BUILD_CACHE_DIR", "")
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "xdg"))

	xdg := DefaultRoot("")
	assert.Equal(t, filepath.Join(os.Getenv("XDG_CACHE_HOME"), "superschematic", "build", CacheFormat), xdg)

	configured := DefaultRoot("~/schema-cache")
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, "schema-cache", CacheFormat), configured, "[cache] root wins over XDG and expands ~")

	t.Setenv("SUPERSCHEMATIC_BUILD_CACHE_DIR", filepath.Join(t.TempDir(), "env"))
	assert.Equal(t, filepath.Join(os.Getenv("SUPERSCHEMATIC_BUILD_CACHE_DIR"), CacheFormat), DefaultRoot("~/schema-cache"), "the environment wins over [cache] root")
}

func stringsOf(value string, count int) string {
	result := ""
	for i := 0; i < count; i++ {
		result += value
	}
	return result
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

func mustReadlink(t *testing.T, path string) string {
	t.Helper()
	target, err := os.Readlink(path)
	require.NoError(t, err)
	return target
}

func sameInode(t *testing.T, a string, b string) bool {
	t.Helper()
	aInfo, err := os.Stat(a)
	require.NoError(t, err)
	bInfo, err := os.Stat(b)
	require.NoError(t, err)
	return os.SameFile(aInfo, bInfo)
}
