package buildcache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/buildplan"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
)

// The runtime provider packages an API's config lists (D159) sit outside
// its service's tree, by a directory or an npm name in node_modules
// (#608). The build reads each one's manifest (package.json, Cargo.toml,
// superschematic.json and go.mod: the provider it declares and the
// package, crate and module a server links), and the server links its
// code. So an API's hash covers each provider package's whole tree, its
// manifests and its sources, and the hash of a schema that references
// the API, a stack that deploys it, covers them too (sourcesDigest): a
// change to a provider's code rebuilds the APIs that list it and the
// stacks that deploy them.
//
// A provider listed by its directory digests as a service's tree does
// (TreeDigest): what a build writes and a tool keeps, its node_modules,
// dist and target among them, is no input. One listed by its npm name is
// an installed package, whose dist is often all of its code, so it
// digests everything but its own node_modules (installedSkipDirs).

// installedSkipDirs are the directories an installed provider package's
// digest leaves out: the packages it installed itself, and a checkout's
// history.
var installedSkipDirs = map[string]bool{".git": true, "node_modules": true}

// providersDigest digests the tree of every runtime provider the
// service's config lists, by entry; "" for a service that lists none. A
// package that does not resolve digests as missing, so installing it
// changes the hash.
func providersDigest(service buildplan.Service) string {
	if service.Config == nil || len(service.Config.Providers) == 0 {
		return ""
	}
	entries := append([]string(nil), service.Config.Providers...)
	sort.Strings(entries)
	var b strings.Builder
	for _, entry := range entries {
		dir, err := schemaconfig.ResolveProvider(service.Dir, entry)
		if err != nil {
			_, _ = fmt.Fprintf(&b, "%s=missing\n", entry)
			continue
		}
		_, _ = fmt.Fprintf(&b, "%s=%s\n", entry, providerTreeDigest(dir, schemaconfig.IsProviderPackageName(entry)))
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// providerTreeDigest digests a provider package's directory, a link
// followed to the directory it names, as an install links a package: as a
// service's tree for a directory, and all but its node_modules and .git for
// an installed package (installed).
func providerTreeDigest(dir string, installed bool) string {
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	if !installed {
		return TreeDigest(dir)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return "missing"
	}
	var entries []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == dir {
			return nil
		}
		if d.IsDir() && installedSkipDirs[d.Name()] {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		switch {
		case d.Type()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			entries = append(entries, "L "+rel+" "+target)
		case !d.IsDir():
			sum, err := fileSHA256(path)
			if err != nil {
				return err
			}
			entries = append(entries, "F "+rel+" "+sum)
		}
		return nil
	})
	if err != nil {
		return "error"
	}
	sort.Strings(entries)
	h := sha256.Sum256([]byte(strings.Join(entries, "\n")))
	return hex.EncodeToString(h[:])
}
