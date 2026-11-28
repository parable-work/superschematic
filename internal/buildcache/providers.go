package buildcache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/buildplan"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
)

// The runtime provider packages an API's config lists (D159) sit outside
// its service's tree, by a directory or an npm name in node_modules
// (#608), and the build reads each one's manifest: the provider it
// declares, and the package, crate and module a server links. So an API's
// hash covers the manifests of its providers, and the hash of a schema
// that references the API, a stack that deploys it, covers them too
// (sourcesDigest).

// ProviderManifests are the files of a provider package the build reads:
// the package.json and Cargo.toml that declare it (D159, D185), the
// superschematic.json of a provider of Go alone and the go.mod that names
// its module (D184).
var ProviderManifests = []string{"package.json", "Cargo.toml", "superschematic.json", "go.mod"}

// providersDigest digests the manifests of every runtime provider the
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
		_, _ = fmt.Fprintf(&b, "%s=", entry)
		for _, name := range ProviderManifests {
			_, _ = fmt.Fprintf(&b, "%s:%s;", name, fileHashOrMissing(filepath.Join(dir, name)))
		}
		b.WriteString("\n")
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}
