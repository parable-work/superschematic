package buildcache

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/internal/buildplan"
	"github.com/parable-work/superschematic/internal/generator/naming"
)

func (tree referenceTree) cachedHashes(cacheRoot string) (*InputHasher, map[string]string) {
	hasher := NewInputHasher(tree.services, tree.repo, naming.Naming{}).WithCache(cacheRoot)
	return hasher, hasher.HashAll(tree.services)
}

func (tree referenceTree) service(t *testing.T, name string) buildplan.Service {
	t.Helper()
	for _, service := range tree.services {
		if service.Name == name {
			return service
		}
	}
	t.Fatalf("no service %s", name)
	return buildplan.Service{}
}

// A dist with no build of a service, a fresh checkout's or one removed,
// has no depfile, so without the cache its key leaves out the sentinel the
// last build read and misses the entry that build stored. The entry keeps
// the depfile: with the cache the key is the entry's, a restore writes the
// depfile back, and the stamp it writes agrees with the next hash. The key
// still covers the listed sentinel's contents.
func TestAFreshDistHashesWithTheDepfileItsEntryKept(t *testing.T) {
	tree := newReferenceTree(t)
	cacheRoot := filepath.Join(t.TempDir(), "cache")
	dist := filepath.Join(tree.schemasRoot, "dist")
	rel := "schemas/dist/api/shop-api"
	writeFile(t, filepath.Join(tree.repo, rel, "server.go"), "package api")
	require.NoError(t, WriteSchemaReferences(tree.schemasRoot, "shop-api", nil, []string{tree.sentinel("orders")}))
	depfile := mustRead(t, schemaReferencesPath(tree.schemasRoot, "shop-api"))

	hasher, hashes := tree.cachedHashes(cacheRoot)
	built := hashes["shop-api"]
	require.NoError(t, StoreEntry(cacheRoot, "schemas", "shop-api", built, tree.repo, []string{rel}, hasher.Tree("shop-api")))
	require.NoError(t, WriteStamp(tree.repo, "shop-api", built))

	require.NoError(t, os.RemoveAll(dist))
	assert.NotEqual(t, built, tree.hashes(t)["shop-api"], "without its depfile, shop-api's key covered the sentinel")
	_, hashes = tree.cachedHashes(cacheRoot)
	require.Equal(t, built, hashes["shop-api"], "a fresh dist missed the entry its depfile keys")

	entry := FindEntry(cacheRoot, "schemas", "shop-api", built)
	require.NotEmpty(t, entry)
	_, err := RestoreEntry(entry, tree.repo)
	require.NoError(t, err)
	assert.Equal(t, "package api", mustRead(t, filepath.Join(tree.repo, rel, "server.go")))
	assert.Equal(t, depfile, mustRead(t, schemaReferencesPath(tree.schemasRoot, "shop-api")), "the restore left the depfile out")
	require.NoError(t, WriteStamp(tree.repo, "shop-api", built))
	assert.Equal(t, built, tree.hashes(t)["shop-api"], "the restored dist hashes to another key than its stamp")

	require.NoError(t, os.RemoveAll(dist))
	writeFile(t, tree.sentinel("orders"), `export const S = service({ name: "orders-v2", kind: "API" });`)
	_, hashes = tree.cachedHashes(cacheRoot)
	assert.NotEqual(t, built, hashes["shop-api"], "a changed sentinel found the entry of the old one")
}

// The cache fills in only for a dist that holds no build of the service:
// a stamp says dist's depfiles, none among them, are what its build read.
// Recompute, after a build, reads what the build wrote. And an entry of
// other sources than the service's own is not read.
func TestTheCacheFillsInOnlyForADistWithNoBuildOfTheService(t *testing.T) {
	tree := newReferenceTree(t)
	cacheRoot := filepath.Join(t.TempDir(), "cache")
	require.NoError(t, WriteSchemaReferences(tree.schemasRoot, "shop-api", nil, []string{tree.sentinel("orders")}))
	hasher, hashes := tree.cachedHashes(cacheRoot)
	built := hashes["shop-api"]
	require.NoError(t, StoreEntry(cacheRoot, "schemas", "shop-api", built, tree.repo, nil, hasher.Tree("shop-api")))
	require.NoError(t, os.Remove(schemaReferencesPath(tree.schemasRoot, "shop-api")))
	unreferenced := tree.hashes(t)["shop-api"]
	require.NotEqual(t, built, unreferenced)

	require.NoError(t, WriteStamp(tree.repo, "shop-api", unreferenced))
	_, hashes = tree.cachedHashes(cacheRoot)
	assert.Equal(t, unreferenced, hashes["shop-api"], "a stamped dist read its depfiles from the cache")

	require.NoError(t, os.RemoveAll(filepath.Join(tree.schemasRoot, "dist")))
	hasher, hashes = tree.cachedHashes(cacheRoot)
	assert.Equal(t, built, hashes["shop-api"])
	assert.Equal(t, unreferenced, hasher.Recompute(tree.service(t, "shop-api")), "Recompute read the depfile from the cache")

	tree.edit(t, "shop-api", "shop-api.schema.ts")
	_, hashes = tree.cachedHashes(cacheRoot)
	assert.Equal(t, tree.hashes(t)["shop-api"], hashes["shop-api"], "edited sources read the depfile of the entry of the old ones")
}

// An entry stored with no tree, as entries were before they kept
// depfiles, is never read for them, and its restore leaves dist's
// depfiles alone. One stored with a tree leaves dist's depfiles as its
// build did: one it did not keep is removed.
func TestRestoreLeavesTheDepfilesAsTheEntrysBuildDid(t *testing.T) {
	tree := newReferenceTree(t)
	cacheRoot := filepath.Join(t.TempDir(), "cache")
	depfile := schemaReferencesPath(tree.schemasRoot, "orders")

	require.NoError(t, WriteSchemaReferences(tree.schemasRoot, "orders", nil, []string{tree.sentinel("shop-api")}))
	before := tree.hashes(t)["orders"]
	require.NoError(t, StoreEntry(cacheRoot, "schemas", "orders", before, tree.repo, nil, ""))
	require.NoError(t, os.Remove(depfile))
	_, hashes := tree.cachedHashes(cacheRoot)
	assert.NotEqual(t, before, hashes["orders"], "an entry with no tree gave its depfiles")

	require.NoError(t, WriteSchemaReferences(tree.schemasRoot, "orders", nil, []string{tree.sentinel("shop-api")}))
	_, err := RestoreEntry(FindEntry(cacheRoot, "schemas", "orders", before), tree.repo)
	require.NoError(t, err)
	assert.FileExists(t, depfile, "an entry with no tree removed dist's depfile")

	unreferenced := stringsOf("b", 64)
	require.NoError(t, os.Remove(depfile))
	require.NoError(t, StoreEntry(cacheRoot, "schemas", "orders", unreferenced, tree.repo, nil, stringsOf("c", 64)))
	require.NoError(t, WriteSchemaReferences(tree.schemasRoot, "orders", nil, []string{tree.sentinel("shop-api")}))
	_, err = RestoreEntry(FindEntry(cacheRoot, "schemas", "orders", unreferenced), tree.repo)
	require.NoError(t, err)
	assert.NoFileExists(t, depfile, "the restore kept a depfile the entry's build did not write")
}
