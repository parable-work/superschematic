package tasks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// Register adds the core's two input resolvers to reg: `path`, tracked
// files by path or glob, and `schema`, a schema service's sources with all
// it reaches. The resolvers of a Rust crate, a Go module, a JS package and
// an image are an extension's (D203).
func Register(reg *registry.Registry) error {
	if err := reg.RegisterTaskResolver(registry.TaskResolverSpec{
		Kind:    ir.TaskPathInput,
		Spec:    registry.PathInputSpec(),
		Resolve: resolvePath,
	}); err != nil {
		return err
	}
	return reg.RegisterTaskResolver(registry.TaskResolverSpec{
		Kind:    ir.TaskSchemaInput,
		Spec:    schemaInputSpec,
		Resolve: resolveSchema,
	})
}

// resolvePath is the `path` resolver: every tracked file a path or a glob
// of the spec covers and no pattern of `exclude` does. A path with no
// wildcard that covers no tracked file is refused: it is a slip of the
// pen, or a file a task writes, which a task reads through `uses`.
func resolvePath(req registry.TaskResolveRequest) (registry.TaskResolution, error) {
	var spec registry.TaskPathSpec
	if err := json.Unmarshal(req.Spec, &spec); err != nil {
		return registry.TaskResolution{}, err
	}
	for _, pattern := range append(append([]string{}, spec.Paths...), spec.Exclude...) {
		if err := CheckPattern(pattern); err != nil {
			return registry.TaskResolution{}, fmt.Errorf("path %v", err)
		}
	}
	var out registry.TaskResolution
	for _, pattern := range spec.Paths {
		matched := match(req.Tree, pattern)
		if len(matched) == 0 && !hasMagic(pattern) {
			return registry.TaskResolution{}, fmt.Errorf("path %q covers no tracked file of the commit; a file a task writes is read through uses, not as an input", pattern)
		}
		for _, name := range matched {
			if !excluded(spec.Exclude, name) {
				out.Paths = append(out.Paths, name)
			}
		}
	}
	return out, nil
}

func excluded(patterns []string, name string) bool {
	for _, pattern := range patterns {
		if Covers(pattern, name) {
			return true
		}
	}
	return false
}

// schemaInputSpec is the JSON Schema of a schema input's spec.
var schemaInputSpec = json.RawMessage(`{
	"type": "object",
	"required": ["service"],
	"additionalProperties": false,
	"properties": {
		"service": {"type": "string", "minLength": 1},
		"root": {"type": "string", "minLength": 1}
	}
}`)

// SchemaSpec is a schema input's spec: a schema service by its name, and
// the schemas root that holds it when the repository has several with a
// service of that name.
type SchemaSpec struct {
	Service string `json:"service"`
	Root    string `json:"root,omitempty"`
}

// configFiles are the names a service's config takes.
var configFiles = []string{"schema.config.ts", "schema.config.json", "schema.config.yaml"}

// SchemaRoot is one schemas root of a commit: the directory whose
// `services` holds a directory per service.
type SchemaRoot struct {
	// Dir is the root's path from the repository root, "" for the
	// repository root itself.
	Dir string
	// Services are the names of the root's services, sorted: each
	// directory of `services` that holds a schema config.
	Services []string
}

// ServiceDir is the directory of the root's service name.
func (r SchemaRoot) ServiceDir(name string) string {
	return path.Join(r.Dir, "services", name)
}

// SchemaRoots finds the schemas roots of a tree: every directory with a
// `services/<name>/schema.config.*` under it.
func SchemaRoots(tree registry.TaskTree) []SchemaRoot {
	services := map[string]map[string]bool{}
	for _, entry := range tree.Entries() {
		dir, file := path.Split(entry.Path)
		isConfig := false
		for _, name := range configFiles {
			isConfig = isConfig || file == name
		}
		if !isConfig {
			continue
		}
		dir = strings.TrimSuffix(dir, "/")
		parent, name := path.Split(dir)
		parent = strings.TrimSuffix(parent, "/")
		if name == "" || path.Base(parent) != "services" || strings.HasPrefix(name, "_") {
			continue
		}
		root := strings.TrimSuffix(strings.TrimSuffix(parent, "services"), "/")
		if services[root] == nil {
			services[root] = map[string]bool{}
		}
		services[root][name] = true
	}
	roots := make([]SchemaRoot, 0, len(services))
	for dir, names := range services {
		root := SchemaRoot{Dir: dir}
		for name := range names {
			root.Services = append(root.Services, name)
		}
		sort.Strings(root.Services)
		roots = append(roots, root)
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i].Dir < roots[j].Dir })
	return roots
}

// rootOf picks the schemas root a spec names: the one it gives, or the
// one root that holds its service.
func rootOf(tree registry.TaskTree, spec SchemaSpec) (SchemaRoot, error) {
	var holding []SchemaRoot
	for _, root := range SchemaRoots(tree) {
		i := sort.SearchStrings(root.Services, spec.Service)
		if i == len(root.Services) || root.Services[i] != spec.Service {
			continue
		}
		if spec.Root != "" {
			if root.Dir == strings.TrimSuffix(spec.Root, "/") {
				return root, nil
			}
			continue
		}
		holding = append(holding, root)
	}
	switch {
	case spec.Root != "":
		return SchemaRoot{}, fmt.Errorf("the commit holds no schema service %s under %s/services", spec.Service, spec.Root)
	case len(holding) == 0:
		return SchemaRoot{}, fmt.Errorf("the commit holds no schema service %s: no tracked services/%s/schema.config.{ts,json,yaml}", spec.Service, spec.Service)
	case len(holding) > 1:
		dirs := make([]string, len(holding))
		for i, root := range holding {
			dirs[i] = fmt.Sprintf("%q", root.Dir)
		}
		return SchemaRoot{}, fmt.Errorf("the commit holds a schema service %s under %d schemas roots (%s); name one with root", spec.Service, len(holding), strings.Join(dirs, ", "))
	}
	return holding[0], nil
}

// resolveSchema is the `schema` resolver. A schema service resolves to a
// static set that holds everything its build may read of the commit:
//
//   - its own directory;
//   - the directory of every service of its schemas root that it names,
//     and of every service those name in turn. A service names another by
//     the other's directory name or package name, in its config, its
//     schema files or a sidecar document: that is how a config lists a
//     dependency, how a schema references a service, and how a document
//     imports a file of another service;
//   - every tracked file of the schemas root that belongs to no service:
//     the naming file, the workspace's manifests and lockfile, and the
//     shared files the services import;
//   - but no file in a directory a build writes, by the names the build
//     cache leaves out (SkippedDirs), and not the copy of the dependency
//     graph the naming file's [deps] copy names, which a build writes;
//   - each file the naming file's [cache] inputs lists.
//
// The set is found from tracked text alone, with no schema loaded and
// nothing run, so it is wider than what one build reads and never
// narrower by design. `tasks inputs` checks it after a build against what
// the build read, and names a file the set lacks.
func resolveSchema(req registry.TaskResolveRequest) (registry.TaskResolution, error) {
	var spec SchemaSpec
	if err := json.Unmarshal(req.Spec, &spec); err != nil {
		return registry.TaskResolution{}, err
	}
	root, err := rootOf(req.Tree, spec)
	if err != nil {
		return registry.TaskResolution{}, err
	}
	set, err := SchemaInputSet(req.Tree, root, spec.Service)
	if err != nil {
		return registry.TaskResolution{}, err
	}
	return registry.TaskResolution{Paths: set.Paths, Generated: set.Generated}, nil
}

// SchemaSet is the static input set of a schema service.
type SchemaSet struct {
	// Services are the services the set holds, the service itself among
	// them, sorted.
	Services []string
	// Paths are the tracked files of the set.
	Paths []string
	// Shared are the files of Paths that belong to no service: the
	// schemas root's own, which are in every service's set.
	Shared []string
	// Generated are the [cache] inputs the commit does not hold.
	Generated []registry.TaskGeneratedPath
}

// SkippedDirs are the directory names a schema input leaves out wherever
// they sit under its schemas root: what a build writes and a tool keeps.
// They are the names the build cache leaves out of a service's digest
// (internal/buildcache), so a file in one cannot change what a service
// generates. A lockfile committed under the output root is one: a build
// of the generated workspace reads it, and a build of the schemas does
// not.
var SkippedDirs = []string{".git", "node_modules", "dist", "bin", "target", "__pycache__", ".venv", ".pytest_cache", ".ruff_cache"}

// skipped reports whether a path of a schemas root sits in a directory a
// schema input leaves out.
func skipped(rootDir, p string) bool {
	rel := strings.TrimPrefix(strings.TrimPrefix(p, rootDir), "/")
	segments := strings.Split(rel, "/")
	for _, segment := range segments[:len(segments)-1] {
		for _, name := range SkippedDirs {
			if segment == name {
				return true
			}
		}
	}
	return false
}

// SchemaInputSet computes the static input set of the root's service
// name.
func SchemaInputSet(tree registry.TaskTree, root SchemaRoot, name string) (SchemaSet, error) {
	if i := sort.SearchStrings(root.Services, name); i == len(root.Services) || root.Services[i] != name {
		return SchemaSet{}, fmt.Errorf("the commit holds no schema service %s under %s", name, path.Join(root.Dir, "services"))
	}
	idents, err := serviceIdentifiers(tree, root)
	if err != nil {
		return SchemaSet{}, err
	}
	rootFiles := under(tree.Entries(), root.Dir)
	byService := map[string][]registry.TaskTreeEntry{}
	var shared []registry.TaskTreeEntry
	servicesDir := path.Join(root.Dir, "services") + "/"
	known := map[string]bool{}
	for _, service := range root.Services {
		known[service] = true
	}
	// The naming file says what else is no input: the copy of the
	// dependency graph build-all writes ([deps] copy), which a build
	// produces and none reads.
	namingFile := path.Join(root.Dir, naming.FileName)
	var names *naming.Naming
	if _, ok := tree.Entry(namingFile); ok {
		data, err := tree.Read(namingFile)
		if err != nil {
			return SchemaSet{}, err
		}
		parsed, err := naming.Parse(data, namingFile)
		if err != nil {
			return SchemaSet{}, err
		}
		names = &parsed
	}
	depsCopy := ""
	if names != nil && names.Deps.Copy != "" {
		depsCopy = path.Join(path.Dir(root.Dir), names.Deps.Copy)
	}
	for _, entry := range rootFiles {
		if skipped(root.Dir, entry.Path) || entry.Path == depsCopy {
			continue
		}
		rest, inServices := strings.CutPrefix(entry.Path, servicesDir)
		if service, _, ok := strings.Cut(rest, "/"); inServices && ok && known[service] {
			byService[service] = append(byService[service], entry)
			continue
		}
		shared = append(shared, entry)
	}

	// The services name reaches: a scan of each service's tracked text
	// for the others' identifiers, followed until nothing new is named.
	reached := map[string]bool{name: true}
	queue := []string{name}
	for len(queue) > 0 {
		service := queue[0]
		queue = queue[1:]
		named, err := namedServices(tree, byService[service], service, idents)
		if err != nil {
			return SchemaSet{}, err
		}
		for _, other := range named {
			if !reached[other] {
				reached[other] = true
				queue = append(queue, other)
			}
		}
	}

	set := SchemaSet{}
	for service := range reached {
		set.Services = append(set.Services, service)
		for _, entry := range byService[service] {
			set.Paths = append(set.Paths, entry.Path)
		}
	}
	sort.Strings(set.Services)
	for _, entry := range shared {
		set.Paths = append(set.Paths, entry.Path)
		set.Shared = append(set.Shared, entry.Path)
	}

	// The naming file's [cache] inputs: files generation reads from
	// outside the schemas root. One the commit does not hold is a file a
	// task writes.
	if names != nil {
		// An entry is relative to the parent of the schemas root, as the
		// build cache reads it.
		for _, rel := range names.Cache.Inputs {
			file := path.Join(path.Dir(root.Dir), rel)
			if root.Dir == "" || file == ".." || strings.HasPrefix(file, "../") {
				return SchemaSet{}, fmt.Errorf("%s: [cache] inputs names %q, which is outside the repository, so no commit holds it", namingFile, rel)
			}
			if _, ok := tree.Entry(file); ok {
				set.Paths = append(set.Paths, file)
				continue
			}
			set.Generated = append(set.Generated, registry.TaskGeneratedPath{Path: file, From: namingFile})
		}
	}
	sort.Strings(set.Paths)
	return set, nil
}

// serviceIdentifiers returns, for each service of the root, the names
// another service may name it by: its directory's name, and its
// package's name when its directory holds a package.json.
func serviceIdentifiers(tree registry.TaskTree, root SchemaRoot) (map[string][]string, error) {
	idents := make(map[string][]string, len(root.Services))
	for _, service := range root.Services {
		idents[service] = []string{service}
		manifest := path.Join(root.ServiceDir(service), "package.json")
		if _, ok := tree.Entry(manifest); !ok {
			continue
		}
		data, err := tree.Read(manifest)
		if err != nil {
			return nil, err
		}
		var pkg struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(data, &pkg); err != nil {
			return nil, fmt.Errorf("%s: %w", manifest, err)
		}
		if pkg.Name != "" && pkg.Name != service {
			idents[service] = append(idents[service], pkg.Name)
		}
	}
	return idents, nil
}

// namedServices returns the services whose identifiers the tracked files
// of a service hold, sorted. A file over the tree's limit is not read,
// and the service is then taken to name every service: what the scan
// cannot see it cannot rule out. A symbolic link, a submodule and a file
// with a NUL byte name nothing.
func namedServices(tree registry.TaskTree, files []registry.TaskTreeEntry, self string, idents map[string][]string) ([]string, error) {
	named := map[string]bool{}
	all := func() []string {
		out := make([]string, 0, len(idents))
		for service := range idents {
			if service != self {
				out = append(out, service)
			}
		}
		sort.Strings(out)
		return out
	}
	for _, entry := range files {
		if entry.Mode != ModeFile && entry.Mode != ModeExecutable {
			continue
		}
		data, err := tree.Read(entry.Path)
		var tooLarge *TooLargeError
		if errors.As(err, &tooLarge) {
			return all(), nil
		}
		if err != nil {
			return nil, err
		}
		if bytes.IndexByte(data, 0) >= 0 {
			continue
		}
		for service, names := range idents {
			if service == self || named[service] {
				continue
			}
			for _, name := range names {
				if holdsName(data, name) {
					named[service] = true
					break
				}
			}
		}
	}
	out := make([]string, 0, len(named))
	for service := range named {
		out = append(out, service)
	}
	sort.Strings(out)
	return out, nil
}

// holdsName reports whether text holds name as a whole name: not as a
// part of a longer one, so `shop` is not found in `shop-api`.
func holdsName(text []byte, name string) bool {
	needle := []byte(name)
	for at := 0; ; {
		i := bytes.Index(text[at:], needle)
		if i < 0 {
			return false
		}
		start, end := at+i, at+i+len(needle)
		if (start == 0 || !nameByte(text[start-1])) && (end == len(text) || !nameByte(text[end])) {
			return true
		}
		at = start + 1
	}
}

// nameByte reports whether b may be part of a service's or a package's
// name.
func nameByte(b byte) bool {
	return b == '-' || b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}
