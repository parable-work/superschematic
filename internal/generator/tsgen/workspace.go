package tsgen

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/naming"
)

// The Bun workspace of the generated TypeScript (D51; docs/stack-model.md,
// section 8.6). The output root's package.json is its root, and its
// workspaces are every generated TypeScript package (types, SDKs, API
// packages and the stacks' servers) and the TypeScript implementations at
// the naming file's [implementation_paths] typescript template, so a
// package names another with workspace:* and nothing is symlinked.
//
// The root sits in the output root, which the generator owns, rather than
// at the repository root, whose package.json is the project's own: the
// implementations lie outside it, which Bun 1.4 accepts as a `../` pattern.
// Install at the output root (`bun install` there, or in any generated
// package under it); an install in an implementation does not find the
// root.
//
// A package depends on superscalar, the HTTP runtime and the version-graph
// runtime by name. Where [paths] names a checkout of one, the root
// overrides every dependency on it with a file: path from the root. A
// member's own file: spec would do the same only from the member's depth,
// which Bun 1.4 reads wrongly once members sit at different depths: a
// types package at types/typescript/<name>, an API package at api/<name>,
// an implementation outside the root. The root also depends on superscalar
// itself, for the packages that import it without naming it: the HTTP
// runtime, whose consumer declares it, and the API packages' scalar
// imports. Under Bun's isolated installs each member then resolves it from
// the root's node_modules, above every generated package.
//
// A package depends on another schema's types package with workspace:*,
// not file:../<schema>. With a file: spec on a sibling, Bun 1.4.0
// re-resolves the tree on every install after the first, and Bun 1.4.2 on
// the install after a manifest gains such a spec; that pass reads the
// scalar path the lockfile stores relative to the root as if it were
// relative to the member, and the install fails.
//
// The install writes the workspace's lockfile, bun.lock, beside the root.
// The project commits it (D51, amended), so a server's image and the
// generated CI install the versions it pins: no build removes it, and the
// root it pairs with is the same bytes on every build of the same schemas,
// so a committed lockfile stays current until a manifest changes.

// The directories under the output root that hold the generated
// TypeScript packages, as the generator's paths.go lays them out: a
// pattern per kind for the workspace's members.
var workspacePackagePatterns = []string{
	"types/typescript/*",
	"sdk/typescript/*",
	"api/*",
	"server/*/*",
}

// LockfileName is the Bun workspace's lockfile, which an install at the
// output root writes beside the root's package.json.
const LockfileName = "bun.lock"

// IgnoredLockfile reports whether git ignores the workspace's lockfile in
// outputRoot, which the project commits (D51, amended), and the rule that
// does, as `git check-ignore --verbose` names it: `<file>:<line>:<pattern>`.
// A lockfile git tracks is not ignored, whatever the rules say. An output
// root outside a repository, or a machine without git, reports none.
func IgnoredLockfile(outputRoot string) (rule string, ignored bool) {
	checkIgnore := func(args ...string) ([]byte, error) {
		cmd := exec.Command("git", append(append([]string{"check-ignore"}, args...), "--", LockfileName)...)
		cmd.Dir = outputRoot
		return cmd.Output()
	}
	// git exits 1 when no rule ignores the path and 128 outside a
	// repository. With --verbose it exits 0 for a path a negated rule
	// takes back too, so it only names the rule.
	if _, err := checkIgnore(); err != nil {
		return "", false
	}
	out, err := checkIgnore("--verbose")
	if err != nil {
		return "", true
	}
	rule, _, _ = strings.Cut(strings.TrimSpace(string(out)), "\t")
	return rule, true
}

// WorkspaceRoot is the Bun workspace root a build writes at the output
// root.
type WorkspaceRoot struct {
	// OutputRoot is the directory that holds the generated packages, where
	// the root's package.json is written.
	OutputRoot string

	// RepositoryRoot, when set, adds the TypeScript implementations under
	// it, at the naming file's [implementation_paths] typescript template.
	RepositoryRoot string

	// Naming names the root and the runtime packages.
	Naming naming.Naming

	// Paths are the [paths] checkouts the root overrides the runtime
	// packages with.
	Paths naming.LocalPaths
}

// workspaceManifest is the root's package.json, its members in this order.
type workspaceManifest struct {
	Name         string            `json:"name"`
	Private      bool              `json:"private"`
	Workspaces   []string          `json:"workspaces"`
	Dependencies map[string]string `json:"dependencies"`
	Overrides    map[string]string `json:"overrides,omitempty"`
}

// Manifest returns the root's package.json: the same bytes for every
// build of one output root, so the builds that run in parallel each write
// it for their own package.
func (w WorkspaceRoot) Manifest() (string, error) {
	n := w.Naming.OrDefault()
	manifest := workspaceManifest{
		Name:         n.NpmWorkspacePackage(),
		Private:      true,
		Workspaces:   append([]string(nil), workspacePackagePatterns...),
		Dependencies: map[string]string{n.ScalarNpmPackage: "*"},
	}
	if w.RepositoryRoot != "" {
		rel, err := naming.PhysicalRelPath(w.OutputRoot, w.RepositoryRoot)
		if err != nil {
			return "", fmt.Errorf("workspace root: %w", err)
		}
		manifest.Workspaces = append(manifest.Workspaces, path.Join(rel, n.TypeScriptImplementationGlob()))
	}
	for _, local := range []struct{ pkg, dir string }{
		{n.ScalarNpmPackage, w.Paths.ScalarTypeScript},
		{n.HTTPRuntimeNpmPackage, w.Paths.HTTPRuntimeTypeScript},
		{n.VersionGraphNpmPackage, w.Paths.VersionGraphTypeScript},
	} {
		rel, err := naming.PhysicalRelPath(w.OutputRoot, local.dir)
		if err != nil {
			return "", fmt.Errorf("workspace root: %s: %w", local.pkg, err)
		}
		if rel == "" {
			continue
		}
		if manifest.Overrides == nil {
			manifest.Overrides = map[string]string{}
		}
		manifest.Overrides[local.pkg] = "file:" + rel
		if local.pkg == n.ScalarNpmPackage {
			manifest.Dependencies[local.pkg] = "file:" + rel
		}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(manifest); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// legacyTypesWorkspaceManifest is the package.json earlier builds wrote at
// types/typescript, the root of a workspace of the types packages alone.
func legacyTypesWorkspaceManifest(n naming.Naming) string {
	return fmt.Sprintf(`{
  "name": %q,
  "private": true,
  "workspaces": [
    "*"
  ]
}
`, n.OrDefault().NpmScope+"/types-workspace")
}

// Write writes the root's package.json into OutputRoot. The write is
// atomic and skipped when the file already has the same content. It
// removes the types-only root an earlier build wrote at types/typescript,
// when it is unchanged, so an install in a types package finds this root.
func (w WorkspaceRoot) Write() error {
	manifest, err := w.Manifest()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(w.OutputRoot, 0o755); err != nil {
		return fmt.Errorf("failed to create output root %s: %w", w.OutputRoot, err)
	}
	legacy := filepath.Join(w.OutputRoot, "types", "typescript", "package.json")
	if existing, err := os.ReadFile(legacy); err == nil && string(existing) == legacyTypesWorkspaceManifest(w.Naming) {
		if err := os.Remove(legacy); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("failed to remove the types workspace root %s: %w", legacy, err)
		}
	}
	target := filepath.Join(w.OutputRoot, "package.json")
	if existing, err := os.ReadFile(target); err == nil && string(existing) == manifest {
		return nil
	}
	tmp, err := os.CreateTemp(w.OutputRoot, ".package.json-*")
	if err != nil {
		return fmt.Errorf("failed to stage workspace manifest: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(manifest); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("failed to write workspace manifest: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("failed to close workspace manifest: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("failed to set workspace manifest mode: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("failed to publish workspace manifest: %w", err)
	}
	return nil
}
