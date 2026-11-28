package resolvers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

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
// A workspace's root need not sit above its members, and a build may
// write it: a generator that owns an output root writes the root's
// package.json there and lists the project's own packages as `../`
// members. The spec's `workspace` names that root's directory. When the
// commit holds the root's manifest it is read as any other. When it does
// not, the manifest is reported as generated, and the workspace's
// lockfile, which the project commits, says what the manifest would: the
// members and their names. A root a build writes with no lockfile the
// resolver reads is refused, since nothing tracked says where a member
// is.
//
// A dependency by a path the commit does not hold is reported as
// generated. A workspace often lists a directory a build writes, such as
// the generated packages of an output root, whose members the commit
// does not hold. A dependency whose version is `workspace:` or `*`, and
// whose name no tracked member has, may be one of those, and the
// workspace's lockfile says which. The lockfile is read when the commit
// holds one the resolver reads: `bun.lock`, `package-lock.json`,
// `npm-shrinkwrap.json`, `pnpm-lock.yaml` or `yarn.lock`. bun's binary
// lockfile, `bun.lockb`, is not read: its format is bun's own and
// unwritten, and bun writes `bun.lock` in its place. A name it resolves to a
// directory of the workspace is that directory, taken as a package when
// the commit holds it and reported as generated when it does not. A
// name it resolves to a version of the registry is the registry's. A
// name it does not resolve, or a workspace with no lockfile the resolver
// reads, falls back to the wider rule: the directory of each member
// pattern that names no tracked package is reported as generated. Any
// other dependency is the registry's, which the lockfile holds to what
// was installed.
func resolveJSPackage(req registry.TaskResolveRequest) (registry.TaskResolution, error) {
	var spec jsSpec
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
	if spec.Workspace != "" {
		root, err := cleanDir(spec.Workspace)
		if err != nil {
			return registry.TaskResolution{}, fmt.Errorf("workspace: %w", err)
		}
		err = j.workspaceAt(root)
		if err != nil {
			return registry.TaskResolution{}, err
		}
	} else if err := j.workspace(dir); err != nil {
		return registry.TaskResolution{}, err
	}
	if err := j.pkg(dir, ""); err != nil {
		return registry.TaskResolution{}, err
	}
	return j.result(), nil
}

// jsSpecSchema is the JSON Schema of a js-package input's spec.
var jsSpecSchema = []byte(`{
	"type": "object",
	"required": ["dir"],
	"additionalProperties": false,
	"properties": {
		"dir": {"type": "string", "minLength": 1},
		"workspace": {"type": "string", "minLength": 1}
	}
}`)

// jsSpec is a js-package input's spec: the package's directory, and the
// directory of its workspace's root when that is not found above the
// package, because it sits elsewhere or because a build writes it.
type jsSpec struct {
	Dir       string `json:"dir"`
	Workspace string `json:"workspace"`
}

// specWorkspace is what names a workspace root the spec gives, in an
// error.
const specWorkspace = "the js-package input's `workspace`"

// lockNames are the lockfiles the resolver reads, in the order it looks
// for them.
var lockNames = []string{"bun.lock", "package-lock.json", "npm-shrinkwrap.json", "pnpm-lock.yaml", "yarn.lock"}

type jsResolution struct {
	*resolution
	// wsDir is the workspace root's directory and wsFrom the file that
	// declares the workspace; hasWorkspace is false for a package that is
	// in none.
	wsDir, wsFrom string
	hasWorkspace  bool
	// wsGenerated is set for a workspace whose root the commit does not
	// hold: a build writes it, and its lockfile stands in for it.
	wsGenerated bool
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
	// lockFrom is the lockfile that was read, or tried, and lockErr why
	// one the commit holds says nothing.
	lockFrom string
	lockErr  error
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

// declared reads the workspace a directory declares, if it declares one:
// its pnpm-workspace.yaml, or its package.json's `workspaces`.
func (j *jsResolution) declared(dir string) (patterns []string, from string, ok bool, err error) {
	if name := file(dir, "pnpm-workspace.yaml"); has(j.tree, name) {
		data, err := j.tree.Read(name)
		if err != nil {
			return nil, "", false, err
		}
		var ws struct {
			Packages []string `yaml:"packages"`
		}
		if err := yaml.Unmarshal(data, &ws); err != nil {
			return nil, "", false, fmt.Errorf("%s: %w", name, err)
		}
		return ws.Packages, name, true, nil
	}
	if !has(j.tree, file(dir, "package.json")) {
		return nil, "", false, nil
	}
	pkg, err := j.read(dir)
	if err != nil {
		return nil, "", false, err
	}
	if found := workspacePatterns(pkg.Workspaces); len(found) > 0 {
		return found, file(dir, "package.json"), true, nil
	}
	return nil, "", false, nil
}

// workspace finds the workspace dir belongs to and reads its members.
func (j *jsResolution) workspace(dir string) error {
	for _, above := range ancestors(dir) {
		patterns, from, ok, err := j.declared(above)
		if err != nil {
			return err
		}
		if ok {
			return j.tracked(above, from, patterns)
		}
	}
	return nil
}

// workspaceAt takes the workspace whose root the spec names. A root the
// commit holds is read as one found above the package. A root it does
// not hold is one a build writes: its manifest is reported as generated,
// and its lockfile gives the members.
func (j *jsResolution) workspaceAt(root string) error {
	patterns, from, ok, err := j.declared(root)
	if err != nil {
		return err
	}
	if ok {
		return j.tracked(root, from, patterns)
	}
	if has(j.tree, file(root, "package.json")) {
		return fmt.Errorf("%s names %s, whose package.json declares no workspace: it has no `workspaces`, and no pnpm-workspace.yaml sits beside it", specWorkspace, show(root))
	}
	j.wsDir, j.wsFrom, j.hasWorkspace, j.wsGenerated = root, specWorkspace, true, true
	j.generate(file(root, "package.json"), specWorkspace)
	for _, name := range rootFiles {
		j.add(file(root, name))
	}
	lock := j.lockfile()
	if lock == nil {
		return nil
	}
	// The lockfile's members stand in for the manifest's patterns: those
	// the commit holds are the workspace's tracked members.
	for _, member := range lock.members {
		if member == root || !has(j.tree, file(member, "package.json")) {
			continue
		}
		pkg, err := j.read(member)
		if err != nil {
			return err
		}
		j.members = append(j.members, member)
		if pkg.Name != "" {
			j.names[pkg.Name] = member
		}
	}
	sort.Strings(j.members)
	return nil
}

// tracked takes a workspace whose root the commit holds: the file from
// declares it at dir, with the member patterns.
func (j *jsResolution) tracked(dir, from string, patterns []string) error {
	j.wsDir, j.wsFrom, j.hasWorkspace = dir, from, true
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
				// The package's workspace is not above it: its root sits
				// elsewhere, or a build writes it, so nothing here says
				// where its members are.
				return fmt.Errorf("%s depends on %s by %q, and the commit holds no workspace above it: no tracked package.json with `workspaces` and no pnpm-workspace.yaml. Name the directory of the workspace's root in the input's `workspace`; a root a build writes is read through the lockfile the commit holds beside it", name, dep, spec)
			}
			if strings.HasPrefix(spec, "workspace:") && j.wsGenerated {
				return j.unlocked(name, dep, spec)
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
	// members are the directories of the workspace's members, the root's
	// own among them, in no order.
	members []string
}

// member records a name the lockfile resolves to a member's directory.
func (l *lockfile) member(name, dir, from string) {
	l.names[name] = lockEntry{member: true, dir: dir, from: from}
	l.members = append(l.members, dir)
}

// registry records a name the lockfile resolves to no member, unless it
// has resolved the name to one: a member's name is the member's.
func (l *lockfile) registry(name, from string) {
	if !l.names[name].member {
		l.names[name] = lockEntry{registry: true, from: from}
	}
}

// locked returns what the workspace's lockfile says of a dependency of
// the package at dir.
func (j *jsResolution) locked(dir, dep string) lockEntry {
	lock := j.lockfile()
	if lock == nil {
		return lockEntry{}
	}
	if entry, ok := lock.importers[dir][dep]; ok {
		return entry
	}
	return lock.names[dep]
}

// lockfile returns what the workspace's lockfile resolved, read once, or
// nil when the commit holds none the resolver reads.
func (j *jsResolution) lockfile() *lockfile {
	if !j.lockRead {
		j.lockRead = true
		j.lock = j.readLock()
	}
	return j.lock
}

// readLock reads the first lockfile of the workspace root the resolver
// reads. A lockfile it cannot read or parse says nothing, and the wider
// rule then holds; a path one names outside the repository is dropped
// the same way.
func (j *jsResolution) readLock() *lockfile {
	if !j.hasWorkspace {
		return nil
	}
	readers := map[string]func(from string, data []byte, lock *lockfile) error{
		"bun.lock":            j.readBunLock,
		"package-lock.json":   j.readNpmLock,
		"npm-shrinkwrap.json": j.readNpmLock,
		"pnpm-lock.yaml":      j.readPnpmLock,
		"yarn.lock":           j.readYarnLock,
	}
	for _, name := range lockNames {
		from := file(j.wsDir, name)
		if !has(j.tree, from) {
			continue
		}
		j.lockFrom = from
		data, err := j.tree.Read(from)
		if err != nil {
			j.lockErr = err
			return nil
		}
		lock := &lockfile{names: map[string]lockEntry{}, importers: map[string]map[string]lockEntry{}}
		if err := readers[name](from, data, lock); err != nil {
			j.lockErr = fmt.Errorf("%s: %w", from, err)
			return nil
		}
		return lock
	}
	return nil
}

// unlocked is the error for a `workspace:` dependency of a workspace
// whose root a build writes, on a name its lockfile does not resolve:
// nothing tracked says where the member is.
func (j *jsResolution) unlocked(name, dep, spec string) error {
	root := show(j.wsDir)
	switch {
	case j.lockErr != nil:
		return fmt.Errorf("%s depends on %s by %q. The commit does not hold the root of the workspace at %s, which a build writes, so its lockfile says where a member is, and that lockfile says nothing: %w", name, dep, spec, root, j.lockErr)
	case j.lockFrom == "":
		return fmt.Errorf("%s depends on %s by %q. The commit does not hold the root of the workspace at %s, which a build writes, and holds no lockfile of it the resolver reads (%s), so nothing tracked says where a member is. Commit the workspace's lockfile, or name the package's files with a path input", name, dep, spec, root, strings.Join(lockNames, ", "))
	}
	return fmt.Errorf("%s depends on %s by %q, and %s, the lockfile of the workspace at %s, resolves no member of that name. The commit does not hold the workspace's root, which a build writes, so the lockfile is what says where a member is: install again and commit it", name, dep, spec, j.lockFrom, root)
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
				lock.member(name, dir, from)
			}
			continue
		}
		lock.registry(name, from)
	}
	for rel, member := range parsed.Workspaces {
		if member.Name == "" || rel == "" {
			continue
		}
		if dir, err := joinDir(j.wsDir, rel, from); err == nil {
			lock.member(member.Name, dir, from)
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
			lock.registry(name, from)
			continue
		}
		if dir, err := joinDir(j.wsDir, resolved.Resolved, from); err == nil {
			lock.member(name, dir, from)
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
		lock.members = append(lock.members, importer)
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

// readYarnLock reads yarn's lockfile, in either of its forms. Yarn 2 and
// later write YAML: each entry's `resolution` is
// `<name>@workspace:<directory>` for a member, from the root, and
// `<name>@npm:<version>` or another protocol for anything else. Yarn 1
// writes a form of its own, a line of `<name>@<range>` descriptors above
// each entry, and holds no member at all: a name it resolves is not a
// member's, and a name it lacks is left to the wider rule.
func (j *jsResolution) readYarnLock(from string, data []byte, lock *lockfile) error {
	if !bytes.Contains(data, []byte("\n__metadata:")) && !bytes.HasPrefix(data, []byte("__metadata:")) {
		for _, line := range strings.Split(string(data), "\n") {
			if line == "" || line[0] == ' ' || line[0] == '\t' || line[0] == '#' || !strings.HasSuffix(strings.TrimRight(line, "\r"), ":") {
				continue
			}
			for _, descriptor := range strings.Split(strings.TrimSuffix(strings.TrimRight(line, "\r"), ":"), ",") {
				if name := descriptorName(strings.Trim(strings.TrimSpace(descriptor), `"`)); name != "" {
					lock.registry(name, from)
				}
			}
		}
		return nil
	}
	var parsed map[string]struct {
		Resolution string `yaml:"resolution"`
	}
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return err
	}
	// Each name once and in order, so a name that is a member's and a
	// registry package's both is the member's on every run.
	keys := make([]string, 0, len(parsed))
	for key := range parsed {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		resolution := parsed[key].Resolution
		name := descriptorName(resolution)
		if name == "" {
			continue
		}
		rel, member := strings.CutPrefix(resolution[len(name):], "@workspace:")
		if !member {
			lock.registry(name, from)
			continue
		}
		if dir, err := joinDir(j.wsDir, rel, from); err == nil {
			lock.member(name, dir, from)
		}
	}
	return nil
}

// descriptorName is the package's name of `<name>@<rest>`, a scope's `@`
// at its start being the name's.
func descriptorName(descriptor string) string {
	if len(descriptor) < 2 {
		return ""
	}
	at := strings.IndexByte(descriptor[1:], '@')
	if at < 0 {
		return ""
	}
	return descriptor[:at+1]
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
