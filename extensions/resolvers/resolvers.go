// Package resolvers is the official extension that resolves the common
// units of a repository as inputs of a task (D203): a Go module, a Rust
// crate, a JS package and an image. A Tasks schema names one as an input,
//
//	inputs: [{ kind: "go-module", spec: { dir: "go/shop-api" } }]
//
// and the plan keys the task on the tracked files the unit resolves to.
// The core resolves a path and a schema service; a unit of a toolchain is
// an extension's, since which toolchains a repository builds with is the
// repository's (D10). It registers through the public registry package
// alone, as any extension does, in a Go module of its own (D1).
//
// Each resolver keeps the rules of the seam (registry.TaskResolverSpec).
// It reads tracked files only, through the tree it is given. It parses
// manifests and runs no build tool: `cargo metadata` and `go list` fail
// on a commit whose generated members do not exist yet, and they run code
// on a tree somebody else wrote. It starts no process and uses no
// network. A path a manifest names and the commit does not hold, such as
// a module a build writes, is reported as generated, and the plan then
// requires the task to use an output that covers it.
//
// What a unit resolves to is the unit's own directory, without the units
// nested in it, and each unit of the repository it names in its manifest,
// with the workspace's manifests and lockfiles that decide how it builds.
// It is wider than what one build of the unit reads and never narrower by
// design. A file a unit reads from outside itself and names in no
// manifest, as `include_str!("../../x")` does, is a path input's.
package resolvers

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/registry"
)

// Name is the extension's name.
const Name = "resolvers"

// The input kinds the extension resolves.
const (
	GoModule  = "go-module"
	RustCrate = "rust-crate"
	JSPackage = "js-package"
	Image     = "image"
)

// Extension is what a binary passes to cli.New.
type Extension struct{}

// Name implements registry.Extension.
func (Extension) Name() string { return Name }

// Register implements registry.Extension: one resolver per kind.
func (Extension) Register(r *registry.Registry) error {
	for _, spec := range []registry.TaskResolverSpec{
		{Kind: GoModule, Spec: dirSpec, Resolve: resolveGoModule},
		{Kind: RustCrate, Spec: dirSpec, Resolve: resolveRustCrate},
		{Kind: JSPackage, Spec: dirSpec, Resolve: resolveJSPackage},
		{Kind: Image, Spec: imageSpec, Resolve: resolveImage},
	} {
		spec.Extension = Name
		if err := r.RegisterTaskResolver(spec); err != nil {
			return err
		}
	}
	return nil
}

// dirSpec is the JSON Schema of the spec of a unit named by its
// directory.
var dirSpec = []byte(`{
	"type": "object",
	"required": ["dir"],
	"additionalProperties": false,
	"properties": {
		"dir": {"type": "string", "minLength": 1}
	}
}`)

// unitSpec is the spec of a unit named by its directory: the directory
// that holds its manifest, from the repository root, `.` for the root
// itself.
type unitSpec struct {
	Dir string `json:"dir"`
}

// resolution gathers what an input resolves to.
type resolution struct {
	tree      registry.TaskTree
	paths     map[string]bool
	generated map[string]string
	seen      map[string]bool
}

func newResolution(tree registry.TaskTree) *resolution {
	return &resolution{tree: tree, paths: map[string]bool{}, generated: map[string]string{}, seen: map[string]bool{}}
}

// add takes a tracked file, when the commit holds it, and reports
// whether it does.
func (r *resolution) add(file string) bool {
	if _, ok := r.tree.Entry(file); !ok {
		return false
	}
	r.paths[file] = true
	return true
}

// addUnder takes every tracked file under dir but those inside one of
// the nested directories.
func (r *resolution) addUnder(dir string, nested []string) {
	for _, entry := range registry.TaskEntriesUnder(r.tree.Entries(), dir) {
		if !within(entry.Path, nested) {
			r.paths[entry.Path] = true
		}
	}
}

// generate reports a path a manifest names and the commit does not hold.
func (r *resolution) generate(p, from string) {
	if _, ok := r.generated[p]; !ok {
		r.generated[p] = from
	}
}

// visit reports whether a unit is met for the first time.
func (r *resolution) visit(key string) bool {
	if r.seen[key] {
		return false
	}
	r.seen[key] = true
	return true
}

func (r *resolution) result() registry.TaskResolution {
	out := registry.TaskResolution{}
	for p := range r.paths {
		out.Paths = append(out.Paths, p)
	}
	sort.Strings(out.Paths)
	for p, from := range r.generated {
		out.Generated = append(out.Generated, registry.TaskGeneratedPath{Path: p, From: from})
	}
	sort.Slice(out.Generated, func(i, j int) bool { return out.Generated[i].Path < out.Generated[j].Path })
	return out
}

// has reports whether the commit holds a file.
func has(tree registry.TaskTree, file string) bool {
	_, ok := tree.Entry(file)
	return ok
}

// cleanDir reads a directory a spec or a manifest names, from the
// repository root. The root itself is "", written `.`.
func cleanDir(dir string) (string, error) {
	if strings.Contains(dir, `\`) || strings.HasPrefix(dir, "/") {
		return "", fmt.Errorf("%q is no directory of the repository: write it from the repository root, slash-separated", dir)
	}
	clean := path.Clean(dir)
	if clean == "." {
		return "", nil
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%q is outside the repository", dir)
	}
	return clean, nil
}

// joinDir resolves a path a manifest in dir names, relative to dir. It
// fails for a path that leaves the repository or is absolute, which no
// commit holds.
func joinDir(dir, rel, from string) (string, error) {
	if strings.HasPrefix(rel, "/") || strings.Contains(rel, `\`) {
		return "", fmt.Errorf("%s names %q, an absolute path, which no commit holds", from, rel)
	}
	joined := path.Join(dir, rel)
	if joined == "." {
		return "", nil
	}
	if joined == ".." || strings.HasPrefix(joined, "../") {
		return "", fmt.Errorf("%s names %q, which is outside the repository, so no commit holds it", from, rel)
	}
	return joined, nil
}

// file names a file of dir.
func file(dir, name string) string { return path.Join(dir, name) }

// show names a directory for an error: the root as `.`.
func show(dir string) string {
	if dir == "" {
		return "."
	}
	return dir
}

// within reports whether p sits inside one of the directories.
func within(p string, dirs []string) bool {
	for _, dir := range dirs {
		if strings.HasPrefix(p, dir+"/") {
			return true
		}
	}
	return false
}

// nested returns the directories under dir, dir itself left out, that
// hold a file named marker: the units nested in a unit, whose files are
// their own.
func nested(tree registry.TaskTree, dir, marker string) []string {
	var out []string
	for _, entry := range registry.TaskEntriesUnder(tree.Entries(), dir) {
		if path.Base(entry.Path) != marker {
			continue
		}
		if parent := parentOf(entry.Path); parent != dir {
			out = append(out, parent)
		}
	}
	return out
}

// parentOf is the directory that holds p, "" for the root.
func parentOf(p string) string {
	parent := path.Dir(p)
	if parent == "." {
		return ""
	}
	return parent
}

// ancestors returns dir and each directory above it, the root last.
func ancestors(dir string) []string {
	out := []string{dir}
	for dir != "" {
		dir = parentOf(dir)
		out = append(out, dir)
	}
	return out
}

// matchDir reports whether a workspace's member pattern names dir
// exactly: its segments match dir's one for one, `*`, `?` and `[...]`
// within a segment and `**` for any number of segments.
func matchDir(pattern, dir string) bool {
	return matchExact(strings.Split(pattern, "/"), strings.Split(dir, "/"))
}

func matchExact(pattern, name []string) bool {
	for len(pattern) > 0 {
		if pattern[0] == "**" {
			for skip := 0; skip <= len(name); skip++ {
				if matchExact(pattern[1:], name[skip:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		if ok, err := path.Match(pattern[0], name[0]); err != nil || !ok {
			return false
		}
		pattern, name = pattern[1:], name[1:]
	}
	return len(name) == 0
}

// literalPrefix is the leading segments of a pattern that hold no
// wildcard.
func literalPrefix(pattern string) string {
	segments := strings.Split(pattern, "/")
	n := 0
	for n < len(segments) && !strings.ContainsAny(segments[n], "*?[") {
		n++
	}
	return strings.Join(segments[:n], "/")
}

// members returns the directories that hold a file named marker and that
// one of a workspace's member patterns names, each pattern relative to
// the workspace's directory, with the patterns that named none. A member
// may sit outside the workspace's directory (`../sibling`), so each
// pattern is looked for where it starts. A pattern that starts with `!`
// takes directories out.
func members(tree registry.TaskTree, wsDir string, patterns []string, marker string) (dirs []string, unmatched []string, err error) {
	taken := map[string]bool{}
	var order []string
	for _, pattern := range patterns {
		negate := strings.HasPrefix(pattern, "!")
		pattern = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(pattern, "!"), "./"), "/")
		full, err := joinDir(wsDir, pattern, file(wsDir, marker))
		if err != nil {
			return nil, nil, err
		}
		found := false
		for _, entry := range registry.TaskEntriesUnder(tree.Entries(), literalPrefix(full)) {
			if path.Base(entry.Path) != marker {
				continue
			}
			candidate := parentOf(entry.Path)
			if candidate == wsDir || !matchDir(full, candidate) {
				continue
			}
			found = true
			if _, seen := taken[candidate]; !seen {
				order = append(order, candidate)
			}
			taken[candidate] = !negate
		}
		if !found && !negate {
			unmatched = append(unmatched, full)
		}
	}
	for _, candidate := range order {
		if taken[candidate] {
			dirs = append(dirs, candidate)
		}
	}
	sort.Strings(dirs)
	return dirs, unmatched, nil
}
