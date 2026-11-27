package resolvers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/parable-work/superschematic/registry"
)

// resolveJSPackage is the `js-package` resolver. A package resolves to:
//
//   - every tracked file under its directory, without the workspace
//     members nested in it, which are their own;
//   - each package of its workspace it depends on by name, and each
//     package a dependency names by a path (`file:`, `link:` or
//     `portal:`), and each those depend on in turn. The four dependency
//     tables are read: `dependencies`, `devDependencies`,
//     `peerDependencies` and `optionalDependencies`;
//   - the workspace root's package.json, its lockfiles and its package
//     manager's configuration;
//   - each tsconfig its tsconfig files extend, and each project they
//     reference: a referenced directory that holds a package.json is
//     taken as a package, with what it depends on, and any other with
//     the files under it.
//
// The workspace is the nearest package.json at or above the package that
// has `workspaces`, or the nearest pnpm-workspace.yaml. Its members are
// the tracked packages its patterns name.
//
// A dependency by a path the commit does not hold is reported as
// generated. A workspace often lists a directory a build writes, such as
// the generated packages of an output root, whose members the commit
// does not hold. A dependency whose version is `workspace:` or `*`, and
// whose name no tracked member has, may be one of those, and the
// workspace's lockfile says which. The lockfile is read when the commit
// holds one the resolver reads: `bun.lock`, `package-lock.json`,
// `npm-shrinkwrap.json` or `pnpm-lock.yaml`. A name it resolves to a
// directory of the workspace is that directory, taken as a package when
// the commit holds it and reported as generated when it does not. A
// name it resolves to a version of the registry is the registry's. A
// name it does not resolve, or a workspace with no lockfile the resolver
// reads, falls back to the wider rule: the directory of each member
// pattern that names no tracked package is reported as generated. Any
// other dependency is the registry's, which the lockfile holds to what
// was installed.
func resolveJSPackage(req registry.TaskResolveRequest) (registry.TaskResolution, error) {
	var spec unitSpec
	if err := json.Unmarshal(req.Spec, &spec); err != nil {
		return registry.TaskResolution{}, err
	}
	dir, err := cleanDir(spec.Dir)
	if err != nil {
		return registry.TaskResolution{}, err
	}
	if !has(req.Tree, file(dir, "package.json")) {
		return registry.TaskResolution{}, fmt.Errorf("the commit holds no JS package at %s: no tracked %s", show(dir), file(dir, "package.json"))
	}
	j := &jsResolution{resolution: newResolution(req.Tree), names: map[string]string{}}
	if err := j.workspace(dir); err != nil {
		return registry.TaskResolution{}, err
	}
	if err := j.pkg(dir, ""); err != nil {
		return registry.TaskResolution{}, err
	}
	return j.result(), nil
}

type jsResolution struct {
	*resolution
	// wsDir is the workspace root's directory and wsFrom the file that
	// declares the workspace; hasWorkspace is false for a package that is
	// in none.
	wsDir, wsFrom string
	hasWorkspace  bool
	// names maps each tracked member's name to its directory, members are
	// those directories, and unmatched the member patterns that name no
	// tracked package.
	names     map[string]string
	members   []string
	unmatched []string
	// lock is what the workspace's lockfile says of the names it
	// resolved, read once.
	lock     *lockfile
	lockRead bool
}

// packageJSON is what the resolver reads of a package.json.
type packageJSON struct {
	Name                 string            `json:"name"`
	Workspaces           json.RawMessage   `json:"workspaces"`
	Dependencies         map[string]string `json:"dependencies"`
	DevDependencies      map[string]string `json:"devDependencies"`
	PeerDependencies     map[string]string `json:"peerDependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
}

func (j *jsResolution) read(dir string) (*packageJSON, error) {
	name := file(dir, "package.json")
	data, err := j.tree.Read(name)
	if err != nil {
		return nil, err
	}
	var pkg packageJSON
	if err := json.Unmarshal(data, &pkg); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return &pkg, nil
}

// workspacePatterns reads `workspaces`: a list of patterns, or an object
// whose `packages` is one.
func workspacePatterns(raw json.RawMessage) []string {
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return list
	}
	var object struct {
		Packages []string `json:"packages"`
	}
	if json.Unmarshal(raw, &object) == nil {
		return object.Packages
	}
	return nil
}

// rootFiles are the files of a workspace root that decide how every
// member installs and builds.
var rootFiles = []string{
	"package.json", "bun.lock", "bun.lockb", "bunfig.toml", "package-lock.json", "npm-shrinkwrap.json",
	"pnpm-lock.yaml", "pnpm-workspace.yaml", "yarn.lock", ".yarnrc.yml", ".npmrc",
}

// workspace finds the workspace dir belongs to and reads its members.
func (j *jsResolution) workspace(dir string) error {
	var patterns []string
	for _, above := range ancestors(dir) {
		if has(j.tree, file(above, "pnpm-workspace.yaml")) {
			data, err := j.tree.Read(file(above, "pnpm-workspace.yaml"))
			if err != nil {
				return err
			}
			var ws struct {
				Packages []string `yaml:"packages"`
			}
			if err := yaml.Unmarshal(data, &ws); err != nil {
				return fmt.Errorf("%s: %w", file(above, "pnpm-workspace.yaml"), err)
			}
			j.wsDir, j.wsFrom, j.hasWorkspace, patterns = above, file(above, "pnpm-workspace.yaml"), true, ws.Packages
			break
		}
		if !has(j.tree, file(above, "package.json")) {
			continue
		}
		pkg, err := j.read(above)
		if err != nil {
			return err
		}
		if found := workspacePatterns(pkg.Workspaces); len(found) > 0 {
			j.wsDir, j.wsFrom, j.hasWorkspace, patterns = above, file(above, "package.json"), true, found
			break
		}
	}
	if !j.hasWorkspace {
		return nil
	}
	for _, name := range rootFiles {
		j.add(file(j.wsDir, name))
	}
	found, unmatched, err := members(j.tree, j.wsDir, patterns, "package.json")
	if err != nil {
		return err
	}
	j.members, j.unmatched = found, unmatched
	for _, member := range found {
		pkg, err := j.read(member)
		if err != nil {
			return err
		}
		if pkg.Name != "" {
			j.names[pkg.Name] = member
		}
	}
	return nil
}

// pkg takes the package at dir, which the manifest named from points at.
func (j *jsResolution) pkg(dir, from string) error {
	name := file(dir, "package.json")
	if !has(j.tree, name) {
		j.generate(show(dir), from)
		return nil
	}
	if !j.visit(name) {
		return nil
	}
	pkg, err := j.read(dir)
	if err != nil {
		return err
	}
	j.addUnder(dir, j.inner(dir))
	if err := j.tsconfigs(dir); err != nil {
		return err
	}

	var deps []string
	specs := map[string]string{}
	for _, table := range []map[string]string{pkg.Dependencies, pkg.DevDependencies, pkg.PeerDependencies, pkg.OptionalDependencies} {
		for dep, spec := range table {
			if _, ok := specs[dep]; !ok {
				deps = append(deps, dep)
			}
			specs[dep] = spec
		}
	}
	sort.Strings(deps)
	for _, dep := range deps {
		spec := specs[dep]
		if rel, ok := localPath(spec); ok {
			target, err := joinDir(dir, rel, name)
			if err != nil {
				return err
			}
			// A path names a package's directory, or a packed tarball.
			if !has(j.tree, file(target, "package.json")) && j.add(target) {
				continue
			}
			if err := j.pkg(target, name); err != nil {
				return err
			}
			continue
		}
		if member, ok := j.names[dep]; ok {
			if err := j.pkg(member, name); err != nil {
				return err
			}
			continue
		}
		// A name no tracked member has. With a version that only a
		// workspace answers, it is a member the commit does not hold, and
		// the lockfile says where.
		if strings.HasPrefix(spec, "workspace:") || spec == "*" {
			switch entry := j.locked(dir, dep); {
			case entry.member:
				if err := j.pkg(entry.dir, entry.from); err != nil {
					return err
				}
				continue
			case entry.registry && spec == "*":
				continue
			}
			for _, pattern := range j.unmatched {
				if prefix := literalPrefix(pattern); prefix != "" && prefix != j.wsDir {
					j.generate(prefix, j.wsFrom)
				}
			}
			if strings.HasPrefix(spec, "workspace:") && !j.hasWorkspace {
				// The package's workspace is one a build writes: its
				// root is no tracked file, so nothing here says where
				// its members are.
				return fmt.Errorf("%s depends on %s by %q, and the commit holds no workspace above it: no tracked package.json with `workspaces` and no pnpm-workspace.yaml. A workspace whose root a build writes is not the commit's; name the package's files with a path input, and use the output of the task that writes the workspace", name, dep, spec)
			}
			if strings.HasPrefix(spec, "workspace:") && len(j.unmatched) == 0 {
				return fmt.Errorf("%s depends on %s by %q, and no package of the workspace at %s has that name", name, dep, spec, show(j.wsDir))
			}
		}
	}
	return nil
}

// inner returns the workspace's members nested in dir, which are their
// own. Any other directory with a package.json, a fixture's say, is
// dir's.
func (j *jsResolution) inner(dir string) []string {
	var inner []string
	for _, member := range j.members {
		if member != dir && within(member, []string{dir}) || dir == "" && member != "" {
			inner = append(inner, member)
		}
	}
	return inner
}

// lockEntry is what a lockfile says of one name: a member of the
// workspace at a directory, or a package of the registry. The zero value
// is a name the lockfile does not resolve.
type lockEntry struct {
	member, registry bool
	dir, from        string
}

// lockfile is what a workspace's lockfile resolved: each name for the
// whole workspace, and for pnpm each name per importing package.
type lockfile struct {
	names     map[string]lockEntry
	importers map[string]map[string]lockEntry
}

// locked returns what the workspace's lockfile says of a dependency of
// the package at dir.
func (j *jsResolution) locked(dir, dep string) lockEntry {
	if !j.lockRead {
		j.lockRead = true
		j.lock = j.readLock()
	}
	if j.lock == nil {
		return lockEntry{}
	}
	if entry, ok := j.lock.importers[dir][dep]; ok {
		return entry
	}
	return j.lock.names[dep]
}

// readLock reads the first lockfile of the workspace root the resolver
// reads. A lockfile it cannot parse says nothing, and the wider rule
// then holds; a path one names outside the repository is dropped the
// same way.
func (j *jsResolution) readLock() *lockfile {
	if !j.hasWorkspace {
		return nil
	}
	readers := []struct {
		name string
		read func(from string, data []byte, lock *lockfile) error
	}{
		{"bun.lock", j.readBunLock},
		{"package-lock.json", j.readNpmLock},
		{"npm-shrinkwrap.json", j.readNpmLock},
		{"pnpm-lock.yaml", j.readPnpmLock},
	}
	for _, reader := range readers {
		from := file(j.wsDir, reader.name)
		if !has(j.tree, from) {
			continue
		}
		data, err := j.tree.Read(from)
		if err != nil {
			return nil
		}
		lock := &lockfile{names: map[string]lockEntry{}, importers: map[string]map[string]lockEntry{}}
		if err := reader.read(from, data, lock); err != nil {
			return nil
		}
		return lock
	}
	return nil
}

// readBunLock reads bun's text lockfile: `workspaces` maps each member's
// directory to its name, and `packages` each resolved name to
// `<name>@<version>` or `<name>@workspace:<directory>`.
func (j *jsResolution) readBunLock(from string, data []byte, lock *lockfile) error {
	var parsed struct {
		Workspaces map[string]struct {
			Name string `json:"name"`
		} `json:"workspaces"`
		Packages map[string][]json.RawMessage `json:"packages"`
	}
	if err := json.Unmarshal(stripJSONC(data), &parsed); err != nil {
		return err
	}
	for name, resolved := range parsed.Packages {
		var first string
		if len(resolved) == 0 || json.Unmarshal(resolved[0], &first) != nil {
			continue
		}
		if rel, ok := strings.CutPrefix(first, name+"@workspace:"); ok {
			if dir, err := joinDir(j.wsDir, rel, from); err == nil {
				lock.names[name] = lockEntry{member: true, dir: dir, from: from}
			}
			continue
		}
		lock.names[name] = lockEntry{registry: true, from: from}
	}
	for rel, member := range parsed.Workspaces {
		if member.Name == "" || rel == "" {
			continue
		}
		if dir, err := joinDir(j.wsDir, rel, from); err == nil {
			lock.names[member.Name] = lockEntry{member: true, dir: dir, from: from}
		}
	}
	return nil
}

// readNpmLock reads npm's lockfile: `packages` maps `node_modules/<name>`
// to a link to a member's directory, or to a version of the registry.
func (j *jsResolution) readNpmLock(from string, data []byte, lock *lockfile) error {
	var parsed struct {
		Packages map[string]struct {
			Link     bool   `json:"link"`
			Resolved string `json:"resolved"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return err
	}
	for key, resolved := range parsed.Packages {
		name, ok := strings.CutPrefix(key, "node_modules/")
		if !ok || strings.Contains(name, "node_modules/") {
			continue
		}
		if !resolved.Link {
			lock.names[name] = lockEntry{registry: true, from: from}
			continue
		}
		if dir, err := joinDir(j.wsDir, resolved.Resolved, from); err == nil {
			lock.names[name] = lockEntry{member: true, dir: dir, from: from}
		}
	}
	return nil
}

// readPnpmLock reads pnpm's lockfile: `importers` maps each member's
// directory to its dependencies, each resolved to `link:<directory>`,
// from the member, or to a version of the registry.
func (j *jsResolution) readPnpmLock(from string, data []byte, lock *lockfile) error {
	var parsed struct {
		Importers map[string]map[string]yaml.Node `yaml:"importers"`
	}
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return err
	}
	for rel, tables := range parsed.Importers {
		importer, err := joinDir(j.wsDir, rel, from)
		if err != nil {
			continue
		}
		for table, node := range tables {
			switch table {
			case "dependencies", "devDependencies", "optionalDependencies":
			default:
				continue
			}
			// A dependency's value is its version, or a mapping that
			// holds it.
			var plain map[string]string
			var nested map[string]struct {
				Version string `yaml:"version"`
			}
			versions := map[string]string{}
			if node.Decode(&plain) == nil {
				versions = plain
			} else if node.Decode(&nested) == nil {
				for dep, entry := range nested {
					versions[dep] = entry.Version
				}
			}
			for dep, version := range versions {
				if lock.importers[importer] == nil {
					lock.importers[importer] = map[string]lockEntry{}
				}
				target, linked := strings.CutPrefix(version, "link:")
				if !linked {
					lock.importers[importer][dep] = lockEntry{registry: true, from: from}
					continue
				}
				if dir, err := joinDir(importer, target, from); err == nil {
					lock.importers[importer][dep] = lockEntry{member: true, dir: dir, from: from}
				}
			}
		}
	}
	return nil
}

// localPath reads a dependency's version as a path, when it is one.
func localPath(spec string) (string, bool) {
	for _, prefix := range []string{"file:", "link:", "portal:"} {
		if rel, ok := strings.CutPrefix(spec, prefix); ok {
			return rel, true
		}
	}
	return "", false
}

// tsconfigs takes each tsconfig the tsconfig files of dir extend by a
// path, and each those extend in turn. A base a package's name gives
// (`extends: "@tsconfig/node22"`) is the registry's.
func (j *jsResolution) tsconfigs(dir string) error {
	var pending []string
	for _, entry := range registry.TaskEntriesUnder(j.tree.Entries(), dir) {
		base := path.Base(entry.Path)
		if parentOf(entry.Path) == dir && strings.HasPrefix(base, "tsconfig") && strings.HasSuffix(base, ".json") {
			pending = append(pending, entry.Path)
		}
	}
	for len(pending) > 0 {
		name := pending[0]
		pending = pending[1:]
		if !j.visit("tsconfig:" + name) {
			continue
		}
		if !j.add(name) {
			continue
		}
		data, err := j.tree.Read(name)
		if err != nil {
			return err
		}
		var config struct {
			Extends    json.RawMessage `json:"extends"`
			References []struct {
				Path string `json:"path"`
			} `json:"references"`
		}
		if err := json.Unmarshal(stripJSONC(data), &config); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		for _, reference := range config.References {
			if reference.Path == "" {
				continue
			}
			target, err := joinDir(parentOf(name), reference.Path, name)
			if err != nil {
				return err
			}
			// A reference names a project's directory, or its tsconfig.
			project, tsconfig := target, file(target, "tsconfig.json")
			if strings.HasSuffix(target, ".json") {
				project, tsconfig = parentOf(target), target
			}
			switch {
			case has(j.tree, file(project, "package.json")):
				// A package: its files, and what it depends on.
				if err := j.pkg(project, name); err != nil {
					return err
				}
				if has(j.tree, tsconfig) {
					pending = append(pending, tsconfig)
				}
			case has(j.tree, tsconfig):
				// A project that is no package: the files under it.
				if j.visit("project:" + project) {
					j.addUnder(project, j.inner(project))
				}
				pending = append(pending, tsconfig)
			default:
				j.generate(target, name)
			}
		}
		var bases []string
		var one string
		if json.Unmarshal(config.Extends, &one) == nil {
			bases = []string{one}
		} else {
			_ = json.Unmarshal(config.Extends, &bases)
		}
		for _, base := range bases {
			if !strings.HasPrefix(base, "./") && !strings.HasPrefix(base, "../") {
				continue
			}
			target, err := joinDir(parentOf(name), base, name)
			if err != nil {
				return err
			}
			if !strings.HasSuffix(target, ".json") {
				target += ".json"
			}
			if has(j.tree, target) {
				pending = append(pending, target)
			} else {
				j.generate(target, name)
			}
		}
	}
	return nil
}

// stripJSONC turns JSON with comments and trailing commas, as a tsconfig
// is written, into JSON: a comment becomes spaces, and a comma before a
// closing bracket is dropped.
func stripJSONC(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inString := false
	for i := 0; i < len(data); i++ {
		c := data[i]
		switch {
		case inString:
			out = append(out, c)
			if c == '\\' && i+1 < len(data) {
				i++
				out = append(out, data[i])
			} else if c == '"' {
				inString = false
			}
		case c == '"':
			inString = true
			out = append(out, c)
		case c == '/' && i+1 < len(data) && data[i+1] == '/':
			for i < len(data) && data[i] != '\n' {
				i++
			}
			out = append(out, '\n')
		case c == '/' && i+1 < len(data) && data[i+1] == '*':
			end := bytes.Index(data[i+2:], []byte("*/"))
			if end < 0 {
				return out
			}
			i += end + 3
			out = append(out, ' ')
		case c == ',':
			rest := bytes.TrimLeft(stripLeadingComments(data[i+1:]), " \t\r\n")
			if len(rest) > 0 && (rest[0] == '}' || rest[0] == ']') {
				continue
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	return out
}

// stripLeadingComments skips the white space and comments data starts
// with.
func stripLeadingComments(data []byte) []byte {
	for {
		data = bytes.TrimLeft(data, " \t\r\n")
		switch {
		case bytes.HasPrefix(data, []byte("//")):
			end := bytes.IndexByte(data, '\n')
			if end < 0 {
				return nil
			}
			data = data[end+1:]
		case bytes.HasPrefix(data, []byte("/*")):
			end := bytes.Index(data, []byte("*/"))
			if end < 0 {
				return nil
			}
			data = data[end+2:]
		default:
			return data
		}
	}
}
