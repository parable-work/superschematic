package buildcache

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Depfiles in the cache. A build writes two depfiles under the schemas
// root's dist, the authoring imports (authoring.go) and the schema
// references (references.go), and the next input hash reads them: they
// list what the key covers beyond the service's own tree. A dist that
// holds no build of the service, a fresh checkout's or one removed, has
// neither, so the key it computes leaves those files out and misses every
// entry a build that recorded them stored.
//
// So an entry keeps the depfiles its key was computed from, with the
// digest of the service's own tree, and a hasher given the cache
// (InputHasher.WithCache) reads a service's depfiles from the newest entry
// of the same tree when dist holds neither the service's stamp nor a
// depfile of it. That is sound for the reason a stale depfile left in dist
// is: the key still covers every listed file's contents as they are now,
// so it finds an entry only when the list and each file it names are what
// that entry's build read. A restore writes the entry's depfiles back into
// dist, so the stamp it writes and the next hash agree.

// knownDepfiles are the depfiles a build writes, each at its path under
// the schemas root.
var knownDepfiles = []struct {
	name string
	path func(schemasRoot, service string) string
}{
	{"authoring-imports", authoringImportsPath},
	{"schema-references", schemaReferencesPath},
}

// entryDepfilePath is where an entry keeps a depfile.
func entryDepfilePath(entry, depfile string) string {
	return filepath.Join(entry, "depfiles", depfile+".json")
}

// localDepfiles returns the depfiles a build of service left under the
// schemas root's dist, by name.
func localDepfiles(repoRoot, service string) map[string][]byte {
	found := map[string][]byte{}
	schemasRoot := filepath.Join(repoRoot, SchemasDir)
	for _, d := range knownDepfiles {
		if data, err := os.ReadFile(d.path(schemasRoot, service)); err == nil {
			found[d.name] = data
		}
	}
	return found
}

// entryDepfiles returns the depfiles an entry keeps, by name.
func entryDepfiles(entry string, names []string) map[string][]byte {
	found := map[string][]byte{}
	for _, name := range names {
		if data, err := os.ReadFile(entryDepfilePath(entry, name)); err == nil {
			found[name] = data
		}
	}
	return found
}

// stageDepfiles copies service's depfiles in dist into a staged entry and
// returns the names it copied.
func stageDepfiles(staging, repoRoot, service string) ([]string, error) {
	local := localDepfiles(repoRoot, service)
	var names []string
	for _, d := range knownDepfiles {
		data, ok := local[d.name]
		if !ok {
			continue
		}
		path := entryDepfilePath(staging, d.name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return nil, err
		}
		names = append(names, d.name)
	}
	return names, nil
}

// restoreDepfiles leaves the depfiles of the entry's service in dist as
// the build that stored the entry left them: the ones it kept are
// written, the others removed. An entry with no tree, which kept none,
// leaves dist's alone.
func restoreDepfiles(entry, repoRoot string, m manifest) error {
	if m.Tree == "" {
		return nil
	}
	kept := map[string]bool{}
	for _, name := range m.Depfiles {
		kept[name] = true
	}
	schemasRoot := filepath.Join(repoRoot, SchemasDir)
	for _, d := range knownDepfiles {
		path := d.path(schemasRoot, m.Name)
		if !kept[d.name] {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			continue
		}
		data, err := os.ReadFile(entryDepfilePath(entry, d.name))
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// newestEntryOfTree returns the newest entry of service whose build read
// the service's own tree as tree, or "" when there is none.
func newestEntryOfTree(cacheRoot, kind, service, tree string) (string, manifest) {
	parent := filepath.Join(cacheRoot, kind, service)
	dirs, err := os.ReadDir(parent)
	if err != nil {
		return "", manifest{}
	}
	var newest string
	var found manifest
	for _, dir := range dirs {
		if !dir.IsDir() {
			continue
		}
		entry := filepath.Join(parent, dir.Name())
		data, err := os.ReadFile(filepath.Join(entry, "manifest.json"))
		if err != nil {
			continue
		}
		var m manifest
		if err := json.Unmarshal(data, &m); err != nil || m.Tree == "" || m.Tree != tree {
			continue
		}
		if newest == "" || m.Created > found.Created || (m.Created == found.Created && entry > newest) {
			newest, found = entry, m
		}
	}
	return newest, found
}

// depfiles returns the depfiles the hash of service reads, by name: dist's,
// or, when fromCache is set, the hasher has a cache and dist holds neither
// the service's stamp nor a depfile of it, those of the newest entry of
// the service's tree.
func (ih *InputHasher) depfiles(service string, fromCache bool) map[string][]byte {
	local := localDepfiles(ih.repoRoot, service)
	if !fromCache || ih.cacheRoot == "" || len(local) > 0 || ReadStamp(ih.repoRoot, service) != "" {
		return local
	}
	if cached, ok := ih.cachedDepfiles[service]; ok {
		return cached
	}
	cached := map[string][]byte{}
	if entry, m := newestEntryOfTree(ih.cacheRoot, "schemas", service, ih.serviceDigests[service]); entry != "" {
		cached = entryDepfiles(entry, m.Depfiles)
	}
	ih.cachedDepfiles[service] = cached
	return cached
}
