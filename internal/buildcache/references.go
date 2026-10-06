package buildcache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// Schema references (D41): a service handle in a decorator argument names
// another service. A reference (ir.Schema.References) lets the schema's
// output read that service's IR, so the input hash covers the sources of the
// referenced service and of every service it reaches through its config. An
// identity (a handle under a DecoratorSpec.Identities path) reads only the
// name and kind its sentinel holds, so the hash covers that file alone.
// Neither orders the build.
//
// Discovery reads configs, not schemas, so the references are known only
// once the schema loads. Each build persists them as a depfile under
// dist/.schema-references/<schema>.json, and the next hash reads it, as the
// authoring imports do (authoring.go): a new reference can only appear by
// editing a file the service's own tree digest already covers, and a fresh
// worktree, having no depfile, rebuilds a referencing service once.

// SchemaReferences is the depfile's content.
type SchemaReferences struct {
	// References are the referenced services' names, sorted.
	References []string `json:"references,omitempty"`

	// Identities are the identity handles' sentinel files, repo-relative and
	// sorted.
	Identities []string `json:"identities,omitempty"`
}

func schemaReferencesPath(schemasRoot, name string) string {
	return filepath.Join(schemasRoot, "dist", ".schema-references", name+".json")
}

// WriteSchemaReferences persists the schema's references and the sentinel
// files of its identity handles under <schemasRoot>/dist, where
// ReadSchemaReferences looks for them, and removes the depfile of a schema
// that has neither. identitySentinels are absolute paths; only files under
// the schemas root are recorded, repo-relative.
func WriteSchemaReferences(schemasRoot, name string, references []ir.ServiceRef, identitySentinels []string) error {
	path := schemaReferencesPath(schemasRoot, name)
	var refs SchemaReferences
	for _, ref := range references {
		refs.References = append(refs.References, ref.Name)
	}
	refs.References = sortedUnique(refs.References)

	if len(identitySentinels) > 0 {
		schemasReal, err := filepath.EvalSymlinks(schemasRoot)
		if err != nil {
			return fmt.Errorf("resolving schemas root: %w", err)
		}
		repoRootReal := filepath.Dir(schemasReal)
		for _, file := range identitySentinels {
			real, err := filepath.EvalSymlinks(file)
			if err != nil || !within(real, schemasReal) {
				continue
			}
			rel, err := filepath.Rel(repoRootReal, real)
			if err != nil {
				return err
			}
			refs.Identities = append(refs.Identities, filepath.ToSlash(rel))
		}
		refs.Identities = sortedUnique(refs.Identities)
	}

	if len(refs.References) == 0 && len(refs.Identities) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("removing stale schema references for %s: %w", name, err)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(refs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// ReadSchemaReferences returns the depfile a schema's last build wrote, or
// the zero value when there is none.
func ReadSchemaReferences(repoRoot, name string) SchemaReferences {
	data, err := os.ReadFile(schemaReferencesPath(filepath.Join(repoRoot, SchemasDir), name))
	if err != nil {
		return SchemaReferences{}
	}
	var refs SchemaReferences
	if err := json.Unmarshal(data, &refs); err != nil {
		return SchemaReferences{}
	}
	return refs
}

// writeReferences adds a schema's references and identities to its hash.
func (ih *InputHasher) writeReferences(h io.Writer, name string) {
	refs := ReadSchemaReferences(ih.repoRoot, name)
	for _, ref := range refs.References {
		_, _ = fmt.Fprintf(h, "|ref:%s:%s", ref, ih.sourcesDigest(ref))
	}
	for _, rel := range refs.Identities {
		_, _ = fmt.Fprintf(h, "|identity:%s:%s", rel, fileHashOrMissing(filepath.Join(ih.repoRoot, filepath.FromSlash(rel))))
	}
}

// sourcesDigest digests the sources of the named service and of every
// service it reaches through its config's dependencies, authDb and calls:
// all a schema that references it can read of it through the IR, as a
// stack's resolver reads the services its entry points reach. The walk
// collects a set, so services that reach each other digest without a
// cycle. A service that was not discovered digests by its directory under
// the services root, "missing" when there is none: a renamed service
// changes the hash of every schema that referenced it by the old name.
func (ih *InputHasher) sourcesDigest(name string) string {
	seen := map[string]bool{name: true}
	pending := []string{name}
	var entries []string
	for len(pending) > 0 {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		digest, ok := ih.serviceDigests[current]
		if !ok {
			digest = TreeDigest(filepath.Join(ih.repoRoot, SchemasDir, "services", current))
		}
		entries = append(entries, current+"="+digest)
		service, ok := ih.services[current]
		if !ok || service.Config == nil {
			continue
		}
		next := make([]string, 0, len(service.Config.Dependencies)+len(service.Config.Calls)+1)
		for _, dep := range service.Config.Dependencies {
			next = append(next, dep.Name)
		}
		if service.Config.AuthDB != "" {
			next = append(next, service.Config.AuthDB)
		}
		for _, call := range service.Config.Calls {
			next = append(next, call.Name)
		}
		for _, n := range next {
			if !seen[n] {
				seen[n] = true
				pending = append(pending, n)
			}
		}
	}
	sort.Strings(entries)
	sum := sha256.Sum256([]byte(strings.Join(entries, "\n")))
	return hex.EncodeToString(sum[:])
}

// sortedUnique sorts values and drops repeats.
func sortedUnique(values []string) []string {
	sort.Strings(values)
	out := values[:0]
	for i, v := range values {
		if i == 0 || v != values[i-1] {
			out = append(out, v)
		}
	}
	return out
}
