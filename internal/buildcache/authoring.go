package buildcache

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Authoring imports: deploy documents are executable
// TypeScript and may import source files from other schema directories (a
// deploy.values.ts importing the platform model). Those imports change the
// schema's output bytes without being declared dependencies, so each build
// persists the bun harness's crawled module graph as a depfile under
// dist/.authoring-imports/<schema>.json and the input hash covers the listed
// files' contents.
//
// The depfile pattern is sound without pre-build knowledge of the imports: a
// new import can only appear by editing a file that is already hashed (the
// document itself lives in the schema directory, and every previously
// recorded import is hashed by path). The one deliberate cost is that a
// fresh worktree, having no depfile, computes a hash that never matches an
// entry stored by an import-carrying build, so those schemas rebuild once.
// For the same reason, StoreEntry and WriteStamp must use a hash computed
// AFTER the build wrote the depfile -- storing under the pre-build hash
// would let a later worktree with different import contents false-hit.

func authoringImportsPath(repoRoot, name string) string {
	return filepath.Join(repoRoot, SchemasDir, "dist", ".authoring-imports", name+".json")
}

// WriteAuthoringImports persists the schema's authoring-import depfile.
// imports are absolute paths as reported by the harness; only files under
// the schemas root but outside the schema's own directory are recorded
// (the schema directory has its own tree digest, and the tool is covered by
// ToolDigest). hasDocs distinguishes "no sidecar
// documents" (any stale depfile is removed) from "documents with no
// external imports" (an empty list is written, replacing stale content).
func WriteAuthoringImports(distRoot, name, servicePath string, imports []string, hasDocs bool) error {
	schemasRoot := filepath.Dir(distRoot)
	repoRoot := filepath.Dir(schemasRoot)
	path := authoringImportsPath(repoRoot, name)

	if !hasDocs {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("removing stale authoring imports for %s: %w", name, err)
		}
		return nil
	}

	serviceReal, err := filepath.EvalSymlinks(servicePath)
	if err != nil {
		return fmt.Errorf("resolving service path %s: %w", servicePath, err)
	}
	schemasReal, err := filepath.EvalSymlinks(schemasRoot)
	if err != nil {
		return fmt.Errorf("resolving schemas root: %w", err)
	}
	// Relativize against the resolved root so a symlinked checkout (macOS
	// /var -> /private/var, most temp dirs) yields clean repo-relative paths.
	repoRootReal := filepath.Dir(schemasReal)

	seen := map[string]bool{}
	var rels []string
	for _, imp := range imports {
		real, err := filepath.EvalSymlinks(imp)
		if err != nil {
			// The file was resolvable moments ago in the harness; treat a
			// vanished path as content churn, not an error.
			continue
		}
		if !within(real, schemasReal) || within(real, serviceReal) {
			continue
		}
		rel, err := filepath.Rel(repoRootReal, real)
		if err != nil {
			return err
		}
		slash := filepath.ToSlash(rel)
		if !seen[slash] {
			seen[slash] = true
			rels = append(rels, slash)
		}
	}
	sort.Strings(rels)

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(rels, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// ReadAuthoringImports returns the recorded repo-relative import paths for a
// schema, or nil when no depfile exists (fresh worktree, or a schema without
// deploy documents).
func ReadAuthoringImports(repoRoot, name string) []string {
	data, err := os.ReadFile(authoringImportsPath(repoRoot, name))
	if err != nil {
		return nil
	}
	var rels []string
	if err := json.Unmarshal(data, &rels); err != nil {
		return nil
	}
	return rels
}

func within(path, root string) bool {
	if path == root {
		return true
	}
	return strings.HasPrefix(path, root+string(filepath.Separator))
}
