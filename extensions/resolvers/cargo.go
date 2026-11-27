package resolvers

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/BurntSushi/toml"

	"github.com/parable-work/superschematic/registry"
)

// resolveRustCrate is the `rust-crate` resolver. A crate resolves to:
//
//   - every tracked file under its directory, without the crates nested
//     in it, which are their own;
//   - each crate one of its dependency tables names by a path
//     (`dependencies`, `dev-dependencies`, `build-dependencies`, and the
//     same under each `target`), and each those name in turn. A
//     dependency written `workspace = true` is read from the workspace's
//     `[workspace.dependencies]`;
//   - each crate a `[patch]` or `[replace]` of it or of its workspace
//     points at by a path;
//   - the workspace root's Cargo.toml and Cargo.lock;
//   - the `.cargo/config.toml` and `rust-toolchain.toml` of its directory
//     and of each directory above it.
//
// A directory that is itself a workspace, a root package's or a virtual
// manifest's, resolves to each of its members too.
//
// A path dependency or a member the commit does not hold is reported as
// generated. The parse is of Cargo.toml alone: `cargo metadata` fails
// when a path dependency is missing, which a crate a build writes is
// until that build ran.
func resolveRustCrate(req registry.TaskResolveRequest) (registry.TaskResolution, error) {
	var spec unitSpec
	if err := json.Unmarshal(req.Spec, &spec); err != nil {
		return registry.TaskResolution{}, err
	}
	dir, err := cleanDir(spec.Dir)
	if err != nil {
		return registry.TaskResolution{}, err
	}
	if !has(req.Tree, file(dir, "Cargo.toml")) {
		return registry.TaskResolution{}, fmt.Errorf("the commit holds no Rust crate at %s: no tracked %s", show(dir), file(dir, "Cargo.toml"))
	}
	c := &cargoResolution{resolution: newResolution(req.Tree), manifests: map[string]map[string]any{}}
	if err := c.crate(dir, ""); err != nil {
		return registry.TaskResolution{}, err
	}
	return c.result(), nil
}

type cargoResolution struct {
	*resolution
	manifests map[string]map[string]any
}

// manifest reads the Cargo.toml of dir.
func (c *cargoResolution) manifest(dir string) (map[string]any, error) {
	name := file(dir, "Cargo.toml")
	if m, ok := c.manifests[name]; ok {
		return m, nil
	}
	data, err := c.tree.Read(name)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := toml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	c.manifests[name] = m
	return m, nil
}

// table reads a table of a manifest by its keys, nil when it has none.
func table(m map[string]any, keys ...string) map[string]any {
	for _, key := range keys {
		next, ok := m[key].(map[string]any)
		if !ok {
			return nil
		}
		m = next
	}
	return m
}

// sortedKeys returns a table's keys in order, so a resolution reads a
// manifest the same way on every run.
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// crate takes the crate at dir, which the manifest named from points at.
func (c *cargoResolution) crate(dir, from string) error {
	name := file(dir, "Cargo.toml")
	if !has(c.tree, name) {
		c.generate(show(dir), from)
		return nil
	}
	if !c.visit(name) {
		return nil
	}
	m, err := c.manifest(dir)
	if err != nil {
		return err
	}
	c.addUnder(dir, nested(c.tree, dir, "Cargo.toml"))
	for _, above := range ancestors(dir) {
		for _, config := range []string{".cargo/config.toml", ".cargo/config", "rust-toolchain.toml", "rust-toolchain"} {
			c.add(file(above, config))
		}
	}

	// The workspace the crate belongs to: its own, or the nearest above
	// it, or the one `package.workspace` names.
	wsDir, ws, err := c.workspaceOf(dir, m)
	if err != nil {
		return err
	}
	if ws != nil {
		c.add(file(wsDir, "Cargo.toml"))
		c.add(file(wsDir, "Cargo.lock"))
		if err := c.patches(wsDir, ws); err != nil {
			return err
		}
	}
	if err := c.patches(dir, m); err != nil {
		return err
	}

	// The dependency tables, the crate's and each target's.
	tables := []map[string]any{m}
	targets := table(m, "target")
	for _, key := range sortedKeys(targets) {
		if t, ok := targets[key].(map[string]any); ok {
			tables = append(tables, t)
		}
	}
	for _, t := range tables {
		for _, kind := range []string{"dependencies", "dev-dependencies", "build-dependencies"} {
			deps := table(t, kind)
			for _, dep := range sortedKeys(deps) {
				spec, ok := deps[dep].(map[string]any)
				if !ok {
					continue
				}
				if rel, ok := spec["path"].(string); ok {
					if err := c.dependency(dir, rel, name); err != nil {
						return err
					}
					continue
				}
				if inherited, _ := spec["workspace"].(bool); inherited && ws != nil {
					shared, _ := table(ws, "workspace", "dependencies")[dep].(map[string]any)
					if rel, ok := shared["path"].(string); ok {
						if err := c.dependency(wsDir, rel, file(wsDir, "Cargo.toml")); err != nil {
							return err
						}
					}
				}
			}
		}
	}

	// A workspace's members, when dir is one.
	if workspace := table(m, "workspace"); workspace != nil {
		return c.members(dir, workspace)
	}
	return nil
}

// dependency takes the crate a manifest in dir names by the path rel.
func (c *cargoResolution) dependency(dir, rel, from string) error {
	target, err := joinDir(dir, rel, from)
	if err != nil {
		return err
	}
	return c.crate(target, from)
}

// patches takes each crate a `[patch]` or `[replace]` of the manifest in
// dir points at by a path: it stands in for a dependency of the crates
// that build with the manifest.
func (c *cargoResolution) patches(dir string, m map[string]any) error {
	from := file(dir, "Cargo.toml")
	sources := []map[string]any{table(m, "replace")}
	patch := table(m, "patch")
	for _, registryName := range sortedKeys(patch) {
		if t, ok := patch[registryName].(map[string]any); ok {
			sources = append(sources, t)
		}
	}
	for _, source := range sources {
		for _, dep := range sortedKeys(source) {
			spec, ok := source[dep].(map[string]any)
			if !ok {
				continue
			}
			if rel, ok := spec["path"].(string); ok {
				if err := c.dependency(dir, rel, from); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// workspaceOf finds the workspace a crate builds in: its own manifest
// when it has `[workspace]`, the manifest `package.workspace` names, or
// the nearest manifest above it that has `[workspace]`. It returns nil
// for a crate that is its own.
func (c *cargoResolution) workspaceOf(dir string, m map[string]any) (string, map[string]any, error) {
	if table(m, "workspace") != nil {
		return dir, m, nil
	}
	if rel, ok := table(m, "package")["workspace"].(string); ok {
		wsDir, err := joinDir(dir, rel, file(dir, "Cargo.toml"))
		if err != nil {
			return "", nil, err
		}
		if !has(c.tree, file(wsDir, "Cargo.toml")) {
			c.generate(show(wsDir), file(dir, "Cargo.toml"))
			return "", nil, nil
		}
		ws, err := c.manifest(wsDir)
		return wsDir, ws, err
	}
	for _, above := range ancestors(dir)[1:] {
		if !has(c.tree, file(above, "Cargo.toml")) {
			continue
		}
		ws, err := c.manifest(above)
		if err != nil {
			return "", nil, err
		}
		if table(ws, "workspace") != nil {
			return above, ws, nil
		}
	}
	return "", nil, nil
}

// members takes each member of the workspace at dir: a crate a member
// pattern names and no `exclude` path holds. A member with no wildcard
// that the commit does not hold is generated, and so is the directory a
// pattern that names no crate starts at.
func (c *cargoResolution) members(dir string, workspace map[string]any) error {
	from := file(dir, "Cargo.toml")
	patterns := stringList(workspace["members"])
	found, unmatched, err := members(c.tree, dir, patterns, "Cargo.toml")
	if err != nil {
		return err
	}
	var excluded []string
	for _, rel := range stringList(workspace["exclude"]) {
		target, err := joinDir(dir, rel, from)
		if err != nil {
			return err
		}
		excluded = append(excluded, target)
	}
	for _, member := range found {
		skip := false
		for _, ex := range excluded {
			skip = skip || member == ex || within(member, []string{ex})
		}
		if skip {
			continue
		}
		if err := c.crate(member, from); err != nil {
			return err
		}
	}
	for _, pattern := range unmatched {
		if prefix := literalPrefix(pattern); prefix != "" && prefix != dir {
			c.generate(prefix, from)
		}
	}
	return nil
}

// stringList reads a manifest's list of strings.
func stringList(value any) []string {
	list, _ := value.([]any)
	out := make([]string, 0, len(list))
	for _, item := range list {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
