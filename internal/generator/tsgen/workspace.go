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
	"slices"
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
// The project's own packages, the implementations and the sites, are
// members of one output root's workspace alone: the default one,
// `<schemas-root>/dist`, where every command writes without --out. An
// install links each member's node_modules into the root that installs it,
// so a second root that listed them would take their links over, and leave
// them dangling once it was gone. Any other output root lists the mirrors
// the stack's build copies the implementations into, `impl/*` (D65;
// servergen's tsmirror.go), and no package of the project's: its servers,
// jobs and workers build against its own mirrors, as its Rust ones do. A
// mirror keeps its package's name, so a server's package.json names the
// implementation alike in every root, and Bun, which refuses two members of
// one name, finds one.
//
// A package depends on superscalar, the HTTP runtime, the version-graph
// runtime and the behavior engine (D58) by name. Where [paths] names a
// checkout of one, the root overrides every dependency on it with a file:
// path from the root. A
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
// root it pairs with is the same bytes on every build of every service of
// the same schemas, so a committed lockfile stays current until a
// manifest changes.
//
// The output root holds only the packages of the services built into it:
// a stack's build, which stack dev runs, builds that stack's services and
// the services they need, and no other. An implementation or a site
// depends on its API's generated packages with workspace:*, which Bun
// cannot resolve when they are missing, and it refuses the whole install
// then, even one --filter narrows. So the root leaves out, with a `!`
// pattern after the patterns that take it in, each implementation or site
// whose workspace dependencies the output root does not hold, nor another
// member (LeftOutPattern; D51, amended). The generated packages need no
// such pattern: their patterns match only the packages a build wrote, and
// a build writes the packages each one depends on. Once every service is
// built, as build-all builds them, the root leaves out nothing and is the
// manifest the committed lockfile pins. A root that leaves a package out
// is a partial workspace, whose install must not save the lockfile
// (WorkspaceLeftOut).

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

// MirrorRoot is the directory of the output root that holds the mirrors of
// the implementations its servers, jobs and workers build against, Rust's
// and TypeScript's alike (D65): `impl/<service>/`. In an output root other
// than the default one, the workspace's members include `impl/*`, which
// takes in the TypeScript mirrors; Bun passes over a Rust one, which holds
// no package.json.
const MirrorRoot = "impl"

// DefaultOutputRoot is the output root every command writes into without
// --out, `<schemas-root>/dist`, whose Bun workspace the project's own
// TypeScript packages are members of. It is "" for no schemas root.
func DefaultOutputRoot(schemasRoot string) string {
	if schemasRoot == "" {
		return ""
	}
	return filepath.Join(schemasRoot, "dist")
}

// Mirrored reports whether outputRoot is an output root other than the
// project's default one, whose servers, jobs and workers build against
// mirrors of the TypeScript implementations and whose workspace lists
// those in place of the project's packages (D65). With no schemas root to
// find the default by, as in a build no command runs, every output root is
// taken as the default.
func Mirrored(outputRoot, schemasRoot string) bool {
	def := DefaultOutputRoot(schemasRoot)
	if def == "" || outputRoot == "" {
		return false
	}
	return !SamePath(outputRoot, def)
}

// SamePath reports whether a and b name one directory, whether or not it
// exists yet, through any symbolic link on the way.
func SamePath(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return absA == absB || naming.PhysicalPath(absA) == naming.PhysicalPath(absB)
}

// IgnoredLockfile reports whether git ignores the workspace's lockfile in
// outputRoot, which the project commits (D51, amended), and the rule that
// does, as `git check-ignore --verbose` names it: `<file>:<line>:<pattern>`.
// A lockfile git tracks is not ignored, whatever the rules say. An output
// root outside a repository, or a machine without git, reports none.
func IgnoredLockfile(outputRoot string) (rule string, ignored bool) {
	return IgnoredFile(outputRoot, LockfileName)
}

// IgnoredFile reports whether git ignores the file name in dir, and the
// rule that does, as IgnoredLockfile reports it for the Bun workspace's
// lockfile: the Cargo workspace's lockfile beside it is another file the
// project commits (D57).
func IgnoredFile(dir, name string) (rule string, ignored bool) {
	checkIgnore := func(args ...string) ([]byte, error) {
		cmd := exec.Command("git", append(append([]string{"check-ignore"}, args...), "--", name)...)
		cmd.Dir = dir
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
	// it, at the naming file's [implementation_paths] typescript template,
	// and the sites, at its site template (D55), unless Mirrored is set.
	RepositoryRoot string

	// Mirrored, set for an output root other than the default one
	// (Mirrored), lists the implementations' mirrors under impl/ in place
	// of the project's packages (D65).
	Mirrored bool

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
// build of one output root holding the same packages, so the builds that
// run in parallel each write it for their own package. The packages it
// leaves out depend on the generated packages on disk, so a build that
// writes several, as build-all does, writes the root once more when they
// are all in place.
func (w WorkspaceRoot) Manifest() (string, error) {
	n := w.Naming.OrDefault()
	manifest := workspaceManifest{
		Name:         n.NpmWorkspacePackage(),
		Private:      true,
		Workspaces:   append([]string(nil), workspacePackagePatterns...),
		Dependencies: map[string]string{n.ScalarNpmPackage: "*"},
	}
	switch {
	case w.Mirrored:
		// The mirrors of the implementations this root's servers, jobs and
		// workers build against; the project's packages are the default
		// root's members.
		manifest.Workspaces = append(manifest.Workspaces, MirrorRoot+"/*")
	case w.RepositoryRoot != "":
		rel, err := naming.PhysicalRelPath(w.OutputRoot, w.RepositoryRoot)
		if err != nil {
			return "", fmt.Errorf("workspace root: %w", err)
		}
		manifest.Workspaces = append(manifest.Workspaces, path.Join(rel, n.TypeScriptImplementationGlob()))
		// The sites sit in the workspace too, so a site imports the SDKs
		// of the APIs it calls with workspace:* (D55).
		if site := path.Join(rel, n.SiteImplementationGlob()); !slices.Contains(manifest.Workspaces, site) {
			manifest.Workspaces = append(manifest.Workspaces, site)
		}
		leftOut, err := w.leftOut(rel, n)
		if err != nil {
			return "", fmt.Errorf("workspace root: %w", err)
		}
		for _, member := range leftOut {
			manifest.Workspaces = append(manifest.Workspaces, LeftOutPattern+member)
		}
	}
	for _, local := range []struct{ pkg, dir string }{
		{n.ScalarNpmPackage, w.Paths.ScalarTypeScript},
		{n.HTTPRuntimeNpmPackage, w.Paths.HTTPRuntimeTypeScript},
		{n.VersionGraphNpmPackage, w.Paths.VersionGraphTypeScript},
		{n.EngineNpmPackage, w.Paths.EngineTypeScript},
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

// LeftOutPattern opens a workspace pattern that leaves a package out of
// the workspace: Bun's negation, which the root writes for each
// implementation or site whose workspace dependencies the output root
// lacks.
const LeftOutPattern = "!"

// workspacePackage is what the root reads of a package's package.json:
// its name and the packages it names with a workspace: spec.
type workspacePackage struct {
	member string
	name   string
	needs  []string
	in     bool
}

// readWorkspacePackage reads the package in dir. A package.json that is
// not JSON names no dependency, so the root keeps the package and Bun
// reports the error.
func readWorkspacePackage(dir string) (workspacePackage, error) {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return workspacePackage{}, err
	}
	var manifest struct {
		Name                 string            `json:"name"`
		Dependencies         map[string]string `json:"dependencies"`
		DevDependencies      map[string]string `json:"devDependencies"`
		OptionalDependencies map[string]string `json:"optionalDependencies"`
		PeerDependencies     map[string]string `json:"peerDependencies"`
	}
	if json.Unmarshal(data, &manifest) != nil {
		return workspacePackage{}, nil
	}
	pkg := workspacePackage{name: manifest.Name}
	for _, deps := range []map[string]string{manifest.Dependencies, manifest.DevDependencies, manifest.OptionalDependencies, manifest.PeerDependencies} {
		for dep, spec := range deps {
			if strings.HasPrefix(spec, "workspace:") && !slices.Contains(pkg.needs, dep) {
				pkg.needs = append(pkg.needs, dep)
			}
		}
	}
	return pkg, nil
}

// leftOut returns the implementations and sites the root leaves out, as
// members relative to the output root (rel is the repository root's):
// each package the implementations' or the sites' pattern takes in that
// depends with workspace: on a package that is neither a generated
// package on disk nor another member, sorted.
func (w WorkspaceRoot) leftOut(rel string, n naming.Naming) ([]string, error) {
	present := map[string]bool{}
	for _, pattern := range workspacePackagePatterns {
		matches, err := filepath.Glob(filepath.Join(w.OutputRoot, filepath.FromSlash(pattern), "package.json"))
		if err != nil {
			return nil, err
		}
		for _, m := range matches {
			pkg, err := readWorkspacePackage(filepath.Dir(m))
			if errors.Is(err, fs.ErrNotExist) {
				// A build running beside this one emptied the
				// package's directory; it writes the root again.
				continue
			}
			if err != nil {
				return nil, err
			}
			if pkg.name != "" {
				present[pkg.name] = true
			}
		}
	}
	var candidates []workspacePackage
	for _, glob := range []string{n.TypeScriptImplementationGlob(), n.SiteImplementationGlob()} {
		matches, err := filepath.Glob(filepath.Join(w.RepositoryRoot, filepath.FromSlash(glob), "package.json"))
		if err != nil {
			return nil, err
		}
		for _, m := range matches {
			dir := filepath.Dir(m)
			sub, err := filepath.Rel(w.RepositoryRoot, dir)
			if err != nil {
				return nil, err
			}
			member := path.Join(rel, filepath.ToSlash(sub))
			if slices.ContainsFunc(candidates, func(c workspacePackage) bool { return c.member == member }) {
				continue
			}
			pkg, err := readWorkspacePackage(dir)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, err
			}
			pkg.member = member
			candidates = append(candidates, pkg)
		}
	}
	// A member may depend on another, so the members grow until none
	// more resolves.
	for grew := true; grew; {
		grew = false
		for i, c := range candidates {
			if c.in || !all(c.needs, func(dep string) bool { return present[dep] }) {
				continue
			}
			if c.name != "" {
				present[c.name] = true
			}
			candidates[i].in = true
			grew = true
		}
	}
	var out []string
	for _, c := range candidates {
		if !c.in {
			out = append(out, c.member)
		}
	}
	slices.Sort(out)
	return out, nil
}

// all reports whether every element of s satisfies f.
func all(s []string, f func(string) bool) bool {
	for _, v := range s {
		if !f(v) {
			return false
		}
	}
	return true
}

// WorkspaceLeftOut returns the packages the Bun workspace whose root is
// outputRoot's package.json leaves out, as its `!` patterns name them: a
// partial workspace, whose install must not save the lockfile, which pins
// the whole workspace (D51, amended). Bun drops from the lockfile what a
// manifest no longer names, and an engineer's or a deploy's install of
// one stack would drop the other stacks' packages from the lockfile the
// project commits.
func WorkspaceLeftOut(outputRoot string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(outputRoot, "package.json"))
	if err != nil {
		return nil, err
	}
	var manifest struct {
		Workspaces []string `json:"workspaces"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("the Bun workspace's root %s: %w", filepath.Join(outputRoot, "package.json"), err)
	}
	var out []string
	for _, pattern := range manifest.Workspaces {
		if member, ok := strings.CutPrefix(pattern, LeftOutPattern); ok {
			out = append(out, member)
		}
	}
	return out, nil
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
