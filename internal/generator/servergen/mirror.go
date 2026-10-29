package servergen

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/tsgen"
	ir "github.com/parable-work/superschematic/ir"
)

// The mirror of a Rust implementation (D65; docs/stack-model.md, section
// 8.6). The crate the project edits, at the naming file's
// [implementation_paths] rust template, names the API crate by its path
// under the output root its scaffold was written for, which its editor and
// its own `cargo test` build against. A server crate under another output
// root depends on that root's API crate, and Cargo refuses a workspace
// that reaches two crates of one name. So each build of a stack copies
// each Rust implementation its servers, jobs and workers build against
// into `<output-root>/impl/<service>/`, and their crates depend on the
// copy: an output root builds against its own API crates alone, and an
// image's context needs no implementation directory.
//
// The mirror holds the crate's files, as a package of the crate would
// hold them, so it holds what a fresh checkout of the crate builds from:
//
//   - Cargo.toml, written in its place: every path dependency re-based for
//     the mirror's directory; the API crate's, the dependency whose package
//     is the API crate's name, naming the current output root's
//     `api/<service>`; any other under the output root the crate names its
//     API crate under naming the same place under the current one; and
//     the crate's own [workspace] table, and any `package.workspace`, left
//     out. Everything else is kept. A crate that inherits a field or a
//     dependency from a workspace (`workspace = true`), or whose build
//     script or targets lie outside it, is refused: the mirror belongs to
//     no workspace and holds the crate's own files.
//   - every other file, but the root's Cargo.lock, the lockfile of the
//     crate's own workspace, which the output root's lockfile stands in
//     for; the root's target/; `.git`; any directory with a Cargo.toml of
//     its own, a crate of its own, which Cargo leaves out of a package
//     too; and, as Cargo packages a crate, what [package] include leaves
//     out when it is set, or else what [package] exclude and the crate's
//     .gitignore files leave out, the .gitignore files standing in for
//     git's ignore rules, so the mirror is the same whether or not the
//     crate is in a git work tree (gitignore.go). A TypeScript mirror
//     selects its files by the same walk (mirrorFiles).
//
// A build copies only the files whose content changed, so an unchanged
// file keeps its modification time and Cargo's fingerprints keep a rebuild
// cheap, and removes the mirror's files the crate no longer has. The
// mirror is no member of the output root's workspace (workspace.go): it is
// a path dependency, as the implementation was, so the lockfile holds the
// same packages, none of the crate's dev-dependencies among them. A build
// removes a mirror no server, job or worker crate under the output root
// depends on.

// MirrorRoot is the directory of the output root that holds the mirrors,
// a Rust implementation's and a TypeScript one's alike (tsmirror.go).
const MirrorRoot = tsgen.MirrorRoot

// MirrorDir is the mirror of service's implementation under the output
// root. An API has one language, so its implementation has one mirror.
func MirrorDir(outputRoot, service string) string {
	return filepath.Join(outputRoot, MirrorRoot, service)
}

// AppMirrorDir is the mirror of the crate of app, an @app class of
// service, under the output root (D78): beside the mirror of its API's
// implementation, named after both, with a dot no service's name holds.
func AppMirrorDir(outputRoot, service, app string) string {
	return filepath.Join(outputRoot, MirrorRoot, service+"."+ir.Kebab(app))
}

// RustMirror is the plan of one implementation's mirror.
type RustMirror struct {
	Service string

	// App is the @app class whose crate the mirror is of, empty for the
	// mirror of the API's implementation (D78).
	App string

	// Source is the implementation crate's directory, Dir the mirror's,
	// MirrorDir, and OutputRoot the output root it lies in, each
	// absolute.
	Source     string
	Dir        string
	OutputRoot string

	// APICrate is the API crate's package name, and APIDir its directory
	// under the output root.
	APICrate string
	APIDir   string
}

// planRustMirror plans the mirror of the implementation of API a of a
// server whose output root is outputRoot, and checks the crate's manifest
// when it is on disk; a scaffold the build is about to write needs no
// checking.
func planRustMirror(outputRoot string, a RustAPIInput) (*RustMirror, error) {
	m := &RustMirror{Service: a.Output.SchemaName, APICrate: a.Output.CrateName}
	var err error
	for _, p := range []struct {
		dst  *string
		from string
	}{
		{&m.Source, a.Implementation.Dir},
		{&m.OutputRoot, outputRoot},
		{&m.Dir, MirrorDir(outputRoot, m.Service)},
		{&m.APIDir, a.Dir},
	} {
		if *p.dst, err = filepath.Abs(p.from); err != nil {
			return nil, err
		}
	}
	if within(m.Dir, m.Source) || within(m.Source, m.Dir) {
		return nil, m.errorf("lies in or around its mirror's directory, %s, which each build rewrites; move the crate, with the naming file's [implementation_paths] rust", m.Dir)
	}
	if a.Implementation.Manifest != nil {
		if _, _, err := m.manifest(a.Implementation.Manifest); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// planRustAppMirror plans the mirror of the crate of app, an app of API a,
// of an app's entrypoint whose output root is outputRoot (D78), as
// planRustMirror plans an implementation's.
func planRustAppMirror(outputRoot string, a RustAPIInput, app *AppInput) (*RustMirror, error) {
	m := &RustMirror{Service: a.Output.SchemaName, App: app.Name, APICrate: a.Output.CrateName}
	var err error
	for _, p := range []struct {
		dst  *string
		from string
	}{
		{&m.Source, app.Implementation.Dir},
		{&m.OutputRoot, outputRoot},
		{&m.Dir, AppMirrorDir(outputRoot, m.Service, app.Name)},
		{&m.APIDir, a.Dir},
	} {
		if *p.dst, err = filepath.Abs(p.from); err != nil {
			return nil, err
		}
	}
	if within(m.Dir, m.Source) || within(m.Source, m.Dir) {
		return nil, m.errorf("lies in or around its mirror's directory, %s, which each build rewrites; move the crate, with the naming file's [implementation_paths] rust_app", m.Dir)
	}
	if app.Implementation.Manifest != nil {
		if _, _, err := m.manifest(app.Implementation.Manifest); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// what names the crate m mirrors: the implementation of its API, or the
// program of its app.
func (m *RustMirror) what() string {
	if m.App != "" {
		return "the crate of app " + m.App + " of " + m.Service
	}
	return "the implementation of " + m.Service
}

// errorf is an error about the implementation m mirrors, named with its
// directory.
func (m *RustMirror) errorf(format string, args ...any) error {
	return fmt.Errorf("%s, at %s, "+format, append([]any{m.what(), m.Source}, args...)...)
}

// mirrorSelection is what a crate's manifest says of the files a package
// of it holds: [package] include and exclude.
type mirrorSelection struct {
	include, exclude []ignoreRule
}

// manifest returns the mirror's Cargo.toml for the crate's, source, and
// the files its manifest leaves out.
func (m *RustMirror) manifest(source []byte) ([]byte, mirrorSelection, error) {
	var doc map[string]any
	md, err := toml.Decode(string(source), &doc)
	if err != nil {
		return nil, mirrorSelection{}, m.errorf("has a Cargo.toml that does not parse: %w", err)
	}
	if key := inheritance(doc); key != "" {
		return nil, mirrorSelection{}, m.errorf("inherits %s from a workspace (workspace = true), and the mirror of it the output root builds against belongs to none (D65); write it out in the crate's Cargo.toml", key)
	}
	pkg, _ := doc["package"].(map[string]any)
	for _, t := range targetPaths(doc) {
		if escapes(t.path) {
			return nil, mirrorSelection{}, m.errorf("names %s %q, outside the crate, and the mirror the output root builds against holds the crate's own files (D65); move it into the crate", t.key, t.path)
		}
	}
	var sel mirrorSelection
	if include := stringList(pkg["include"]); len(include) > 0 {
		sel.include = parseIgnore("", include)
	} else {
		sel.exclude = parseIgnore("", stringList(pkg["exclude"]))
	}

	delete(doc, "workspace")
	delete(pkg, "workspace")
	named := m.namedOutputRoot(doc)
	for _, deps := range dependencyTables(doc) {
		for name, spec := range deps {
			s, ok := spec.(map[string]any)
			if !ok {
				continue
			}
			p, ok := s["path"].(string)
			if !ok {
				continue
			}
			pkgName, _ := s["package"].(string)
			if pkgName == "" {
				pkgName, _, _ = strings.Cut(name, ":")
			}
			if s["path"], err = m.rebase(pkgName, p, named); err != nil {
				return nil, mirrorSelection{}, err
			}
		}
	}
	for _, deps := range dependencyTables(doc) {
		for name, spec := range deps {
			if s, ok := spec.(map[string]any); ok {
				deps[name] = inline(s)
			}
		}
	}

	var b bytes.Buffer
	b.WriteString(m.header())
	enc := toml.NewEncoder(&b)
	enc.Indent = ""
	// Keys outside any table come first, as TOML has them; the tables
	// follow in the crate's order.
	top := map[string]any{}
	for k, v := range doc {
		if !isTable(v) {
			top[k] = v
		}
	}
	if len(top) > 0 {
		if err := enc.Encode(top); err != nil {
			return nil, mirrorSelection{}, m.errorf("Cargo.toml: %w", err)
		}
	}
	for _, k := range tableOrder(md, doc) {
		if err := enc.Encode(map[string]any{k: doc[k]}); err != nil {
			return nil, mirrorSelection{}, m.errorf("Cargo.toml: %w", err)
		}
	}
	return b.Bytes(), sel, nil
}

// inlineTable is a table the encoder writes inline, `{ key = value, ... }`,
// as a dependency's is written: its source, then the rest by name.
type inlineTable map[string]any

// inline is t as an inlineTable, and each table inside it.
func inline(t map[string]any) inlineTable {
	out := inlineTable{}
	for k, v := range t {
		if nested, ok := v.(map[string]any); ok {
			v = inline(nested)
		}
		out[k] = v
	}
	return out
}

// inlineFirst are the keys of a dependency written first, in this order.
var inlineFirst = []string{"package", "version", "path", "git", "branch", "tag", "rev", "registry"}

// MarshalTOML writes t on one line.
func (t inlineTable) MarshalTOML() ([]byte, error) {
	keys := slices.Sorted(maps.Keys(t))
	slices.SortStableFunc(keys, func(a, b string) int {
		rank := func(k string) int {
			if i := slices.Index(inlineFirst, k); i >= 0 {
				return i
			}
			return len(inlineFirst)
		}
		return rank(a) - rank(b)
	})
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		var b bytes.Buffer
		if err := toml.NewEncoder(&b).Encode(map[string]any{k: t[k]}); err != nil {
			return nil, err
		}
		line := strings.TrimSpace(b.String())
		if strings.Contains(line, "\n") {
			return nil, fmt.Errorf("%s does not fit an inline table", k)
		}
		parts = append(parts, line)
	}
	if len(parts) == 0 {
		return []byte("{}"), nil
	}
	return []byte("{ " + strings.Join(parts, ", ") + " }"), nil
}

// header opens the mirror's Cargo.toml, by which a build knows the
// directory is a mirror it writes.
func (m *RustMirror) header() string {
	source, err := naming.RelPath(m.Dir, m.Source)
	if err != nil {
		source = filepath.ToSlash(m.Source)
	}
	return rustWorkspaceHeader + `#
# The mirror of ` + m.what() + `, the crate at
#   ` + source + `
# (D65; docs/stack-model.md, section 8.6). Each build of a stack copies the
# crate's files here and writes this file from its Cargo.toml, with each
# path dependency re-based for this directory, the API crate's naming this
# output root's, and the crate's own workspace left out. The server, job,
# worker, deriver and app crates of the output root depend on the mirror.
# Edit the crate, not the mirror.

`
}

// namedOutputRoot is the output root the crate's manifest names its API
// crate under, `<root>/api/<service>`, or "" when its path to the API
// crate has another shape or it names none. Each table's entries are read
// by name, so the first that names the API crate is the same on every
// build (D94).
func (m *RustMirror) namedOutputRoot(doc map[string]any) string {
	for _, deps := range dependencyTables(doc) {
		for _, name := range slices.Sorted(maps.Keys(deps)) {
			spec := deps[name]
			s, ok := spec.(map[string]any)
			if !ok {
				continue
			}
			p, ok := s["path"].(string)
			if !ok {
				continue
			}
			if pkgName, _ := s["package"].(string); pkgName != m.APICrate && (pkgName != "" || name != m.APICrate) {
				continue
			}
			target := m.resolve(p)
			if filepath.Base(target) == m.Service && filepath.Base(filepath.Dir(target)) == "api" {
				return filepath.Dir(filepath.Dir(target))
			}
		}
	}
	return ""
}

// resolve is the directory a path dependency of the crate names.
func (m *RustMirror) resolve(p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(m.Source, filepath.FromSlash(p))
}

// rebase is the mirror's path for the crate's path dependency p on the
// package pkg: the current output root's API crate for the API crate, the
// same place under the current output root for a crate under the output
// root named (the one the crate names its API crate under), and the same
// directory otherwise, an absolute path left as it is.
func (m *RustMirror) rebase(pkg, p, named string) (string, error) {
	target, kept, err := m.rebasedTarget(pkg, p, named)
	if err != nil {
		return "", err
	}
	if kept {
		return p, nil
	}
	rel, err := naming.RelPath(m.Dir, target)
	if err != nil {
		return "", m.errorf("its path dependency %s: %w", pkg, err)
	}
	return rel, nil
}

// rebasedTarget is the directory the mirror's path dependency p on the
// package pkg names once rebase has re-based it, absolute; kept is set
// for an absolute p, which the mirror writes as it is.
func (m *RustMirror) rebasedTarget(pkg, p, named string) (target string, kept bool, err error) {
	target = m.resolve(p)
	switch {
	case pkg == m.APICrate:
		return m.APIDir, false, nil
	case named != "" && within(named, target):
		rel, err := filepath.Rel(named, target)
		if err != nil {
			return "", false, err
		}
		return filepath.Join(m.OutputRoot, rel), false, nil
	}
	return target, filepath.IsAbs(p), nil
}

// pathDependencies are the directories the mirror's path dependencies
// name once re-based, absolute and sorted: what a build of the mirror
// reads beside it. A dev-dependency is left out, which a build of a crate
// that is no member of the workspace does not read.
func (m *RustMirror) pathDependencies(source []byte) ([]string, error) {
	var doc map[string]any
	if _, err := toml.Decode(string(source), &doc); err != nil {
		return nil, m.errorf("has a Cargo.toml that does not parse: %w", err)
	}
	named := m.namedOutputRoot(doc)
	var dirs []string
	for _, dep := range pathDependencies(doc) {
		target, _, err := m.rebasedTarget(dep.pkg, dep.path, named)
		if err != nil {
			return nil, err
		}
		dirs = append(dirs, target)
	}
	slices.Sort(dirs)
	return slices.Compact(dirs), nil
}

// pathDependency is a dependency a manifest names by path: its package
// and the path as the manifest writes it.
type pathDependency struct{ pkg, path string }

// pathDependencies lists the path dependencies of a manifest's normal and
// build dependencies, its targets' among them, and its patches.
func pathDependencies(doc map[string]any) []pathDependency {
	var out []pathDependency
	for _, deps := range buildDependencyTables(doc) {
		for _, name := range slices.Sorted(maps.Keys(deps)) {
			s, ok := deps[name].(map[string]any)
			if !ok {
				continue
			}
			p, ok := s["path"].(string)
			if !ok {
				continue
			}
			pkg, _ := s["package"].(string)
			if pkg == "" {
				pkg, _, _ = strings.Cut(name, ":")
			}
			out = append(out, pathDependency{pkg: pkg, path: p})
		}
	}
	return out
}

// within reports whether p is root or lies under it.
func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// escapes reports whether p, a path in a manifest, leaves the crate's
// directory.
func escapes(p string) bool {
	clean := filepath.Clean(filepath.FromSlash(p))
	return filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

// isTable reports whether a decoded value is a table or an array of
// tables, which the encoder writes under a header.
func isTable(v any) bool {
	switch v.(type) {
	case map[string]any, []map[string]any:
		return true
	}
	return false
}

// tableOrder lists the top-level tables of doc in the order the manifest
// declared them.
func tableOrder(md toml.MetaData, doc map[string]any) []string {
	var order []string
	seen := map[string]bool{}
	for _, key := range md.Keys() {
		if k := key[0]; !seen[k] && isTable(doc[k]) {
			seen[k] = true
			order = append(order, k)
		}
	}
	for _, k := range slices.Sorted(maps.Keys(doc)) {
		if !seen[k] && isTable(doc[k]) {
			order = append(order, k)
		}
	}
	return order
}

// dependencyTables are the tables of doc whose entries are dependencies:
// the dependencies, dev-dependencies and build-dependencies, each target's
// too, and the patches and replacements, which Cargo reads at a workspace's
// root alone but the mirror keeps.
func dependencyTables(doc map[string]any) []map[string]any {
	return dependencyTablesOf(doc, []string{"dependencies", "dev-dependencies", "dev_dependencies", "build-dependencies", "build_dependencies"})
}

// buildDependencyTables is dependencyTables without the dev-dependencies.
func buildDependencyTables(doc map[string]any) []map[string]any {
	return dependencyTablesOf(doc, []string{"dependencies", "build-dependencies", "build_dependencies"})
}

// dependencyTablesOf lists doc's dependency tables of the kinds named, its
// targets' among them, and its patches and replacements.
func dependencyTablesOf(doc map[string]any, names []string) []map[string]any {
	var tables []map[string]any
	kinds := func(t map[string]any) {
		for _, k := range names {
			if deps, ok := t[k].(map[string]any); ok {
				tables = append(tables, deps)
			}
		}
	}
	kinds(doc)
	if targets, ok := doc["target"].(map[string]any); ok {
		for _, cfg := range slices.Sorted(maps.Keys(targets)) {
			if t, ok := targets[cfg].(map[string]any); ok {
				kinds(t)
			}
		}
	}
	if patch, ok := doc["patch"].(map[string]any); ok {
		for _, source := range slices.Sorted(maps.Keys(patch)) {
			if deps, ok := patch[source].(map[string]any); ok {
				tables = append(tables, deps)
			}
		}
	}
	if replace, ok := doc["replace"].(map[string]any); ok {
		tables = append(tables, replace)
	}
	return tables
}

// inheritance names the first key of doc, outside its [workspace] table
// and the free-form package.metadata, that inherits from a workspace, or
// "".
func inheritance(doc map[string]any) string {
	var walk func(prefix string, t map[string]any) string
	walk = func(prefix string, t map[string]any) string {
		if inherits, _ := t["workspace"].(bool); inherits && prefix != "" {
			return prefix
		}
		for _, k := range slices.Sorted(maps.Keys(t)) {
			key := k
			if prefix != "" {
				key = prefix + "." + k
			}
			if key == "workspace" || key == "package.metadata" {
				continue
			}
			switch v := t[k].(type) {
			case map[string]any:
				if found := walk(key, v); found != "" {
					return found
				}
			case []map[string]any:
				for _, item := range v {
					if found := walk(key, item); found != "" {
						return found
					}
				}
			}
		}
		return ""
	}
	return walk("", doc)
}

// targetPath is a file the manifest names a build script or target by.
type targetPath struct{ key, path string }

// targetPaths are the build script and the targets' files doc names.
func targetPaths(doc map[string]any) []targetPath {
	var paths []targetPath
	if pkg, ok := doc["package"].(map[string]any); ok {
		if build, ok := pkg["build"].(string); ok {
			paths = append(paths, targetPath{"package.build", build})
		}
	}
	if lib, ok := doc["lib"].(map[string]any); ok {
		if p, ok := lib["path"].(string); ok {
			paths = append(paths, targetPath{"lib.path", p})
		}
	}
	for _, kind := range []string{"bin", "example", "test", "bench"} {
		targets, _ := doc[kind].([]map[string]any)
		for _, t := range targets {
			if p, ok := t["path"].(string); ok {
				paths = append(paths, targetPath{"[[" + kind + "]] path", p})
			}
		}
	}
	return paths
}

// stringList is a decoded array of strings, its other items left out.
func stringList(v any) []string {
	items, _ := v.([]any)
	var list []string
	for _, item := range items {
		if s, ok := item.(string); ok {
			list = append(list, s)
		}
	}
	return list
}

// mirrorFile is a file of the crate the mirror holds: its slash-separated
// path in the crate, and its permissions.
type mirrorFile struct {
	rel  string
	mode fs.FileMode
}

// mirrorSkip reports whether a mirror leaves out the entry name of the
// directory rel of the package, slash-separated and "" for the package's
// own, at path on disk, a directory when dir is set, by its language's
// rules, which come before the .gitignore files'.
type mirrorSkip func(rel, name, path string, dir bool) bool

// skipRust is what a Rust mirror leaves out by Cargo's rules: the root's
// Cargo.toml, which the mirror writes, and Cargo.lock, the lockfile of the
// crate's own workspace, which the output root's stands in for; the root's
// target/; and any directory with a Cargo.toml of its own, a crate of its
// own, which Cargo leaves out of a package too.
func skipRust(rel, name, path string, dir bool) bool {
	switch {
	case !dir:
		return rel == "" && (name == RustManifestFile || name == RustLockfileName)
	case rel == "" && name == "target":
		return true
	}
	_, err := os.Stat(filepath.Join(path, RustManifestFile))
	return err == nil
}

// mirrorFiles lists the files of the crate at source the mirror holds,
// but its Cargo.toml, which the mirror writes.
func mirrorFiles(source string, sel mirrorSelection) ([]mirrorFile, error) {
	return packageFiles(source, sel, skipRust)
}

// packageFiles lists the files of the package at source a mirror holds:
// every file but `.git`, what skip leaves out, and what the selection or
// the package's .gitignore files leave out, as mirrorFiles reads them.
func packageFiles(source string, sel mirrorSelection, skip mirrorSkip) ([]mirrorFile, error) {
	var files []mirrorFile
	open := map[string]bool{}
	var walk func(rel string, gitignore []ignoreRule) error
	walk = func(rel string, gitignore []ignoreRule) error {
		dir := filepath.Join(source, filepath.FromSlash(rel))
		// A link to a directory the walk is in would loop.
		real, err := filepath.EvalSymlinks(dir)
		if err != nil {
			return err
		}
		if open[real] {
			return nil
		}
		open[real] = true
		defer delete(open, real)
		if data, err := os.ReadFile(filepath.Join(dir, ".gitignore")); err == nil {
			gitignore = append(slices.Clip(gitignore), parseIgnore(rel, strings.Split(string(data), "\n"))...)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			name := e.Name()
			p := path.Join(rel, name)
			if name == ".git" {
				continue
			}
			info, err := os.Stat(filepath.Join(dir, name))
			if errors.Is(err, fs.ErrNotExist) {
				// A link to nothing.
				continue
			}
			if err != nil {
				return err
			}
			if skip(rel, name, filepath.Join(dir, name), info.IsDir()) {
				continue
			}
			switch {
			case info.IsDir():
				if sel.include == nil && (ignored(gitignore, p, true) || ignored(sel.exclude, p, true)) {
					continue
				}
				if err := walk(p, gitignore); err != nil {
					return err
				}
			case info.Mode().IsRegular():
				if sel.include != nil {
					if !includedBy(sel.include, p) {
						continue
					}
				} else if ignored(gitignore, p, false) || ignored(sel.exclude, p, false) {
					continue
				}
				files = append(files, mirrorFile{rel: p, mode: info.Mode().Perm()})
			}
		}
		return nil
	}
	if err := walk("", nil); err != nil {
		return nil, err
	}
	return files, nil
}

// WriteRustMirror brings the mirror m up to date with its crate: it
// writes each file whose content differs from the crate's, and the
// manifest, and removes every other file. It refuses a directory a build
// did not write.
func WriteRustMirror(m *RustMirror) error {
	fail := func(err error) error {
		if m.App != "" {
			return fmt.Errorf("servergen: the mirror of %s: %w", m.what(), err)
		}
		return fmt.Errorf("servergen: the mirror of %s's implementation: %w", m.Service, err)
	}
	source, err := os.ReadFile(filepath.Join(m.Source, RustManifestFile))
	if err != nil {
		return fail(err)
	}
	manifest, sel, err := m.manifest(source)
	if err != nil {
		return fail(err)
	}
	files, err := mirrorFiles(m.Source, sel)
	if err != nil {
		return fail(err)
	}
	target := filepath.Join(m.Dir, RustManifestFile)
	if existing, err := os.ReadFile(target); err == nil && !bytes.HasPrefix(existing, []byte(rustWorkspaceHeader)) {
		return fail(fmt.Errorf("%s holds a Cargo.toml no build wrote, and the build owns that directory: move the crate out of the output root", m.Dir))
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fail(err)
	}
	if err := writeMirror(m.Source, m.Dir, files, RustManifestFile, manifest); err != nil {
		return fail(err)
	}
	return nil
}

// writeMirror brings the mirror at dir up to date with the package at
// source: it removes every file under dir that is neither one of files nor
// the manifest, writes each of files whose content differs from source's,
// and writes the manifest, the file the mirror writes in place of the
// package's own, at name. A file whose content is the same keeps its
// modification time.
func writeMirror(source, dir string, files []mirrorFile, name string, manifest []byte) error {
	want := map[string]bool{name: true}
	for _, f := range files {
		want[f.rel] = true
	}
	if err := pruneMirror(dir, want); err != nil {
		return err
	}
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(source, filepath.FromSlash(f.rel)))
		if err != nil {
			return err
		}
		if err := writeIfChanged(filepath.Join(dir, filepath.FromSlash(f.rel)), data, f.mode); err != nil {
			return err
		}
	}
	// The manifest goes last: a mirror whose manifest is the build's is
	// whole.
	return writeIfChanged(filepath.Join(dir, name), manifest, 0o644)
}

// pruneMirror removes each file under dir that want, by its
// slash-separated path, does not list, then each directory left empty.
func pruneMirror(dir string, want map[string]bool) error {
	var dirs []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) && p == dir {
			return filepath.SkipAll
		}
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil || rel == "." {
			return err
		}
		if d.IsDir() {
			dirs = append(dirs, p)
			return nil
		}
		if !want[filepath.ToSlash(rel)] {
			return os.Remove(p)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, d := range slices.Backward(dirs) {
		if entries, err := os.ReadDir(d); err == nil && len(entries) == 0 {
			if err := os.Remove(d); err != nil {
				return err
			}
		}
	}
	return nil
}

// writeIfChanged writes data to the file target with mode, through a
// file beside it renamed into place, unless target holds data already;
// then it changes the mode alone, if that differs, and keeps the file's
// modification time.
func writeIfChanged(target string, data []byte, mode fs.FileMode) error {
	if info, err := os.Lstat(target); err == nil && info.Mode().IsRegular() {
		if existing, err := os.ReadFile(target); err == nil && bytes.Equal(existing, data) {
			if info.Mode().Perm() != mode {
				return os.Chmod(target, mode)
			}
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".mirror-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, err = tmp.Write(data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(name, mode)
	}
	if err == nil {
		err = os.Rename(name, target)
	}
	if err != nil {
		_ = os.Remove(name)
	}
	return err
}

// PruneRustMirrors removes each mirror under outputRoot's impl/ that no
// Rust crate under server/ depends on, whichever stack's it is, and impl/
// itself once it is empty. It leaves a directory there that holds no
// manifest a build wrote.
func PruneRustMirrors(outputRoot string) error {
	fail := func(err error) error {
		return fmt.Errorf("servergen: the mirrors of the Rust implementations: %w", err)
	}
	outputRoot, err := filepath.Abs(outputRoot)
	if err != nil {
		return fail(err)
	}
	root := filepath.Join(outputRoot, MirrorRoot)
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fail(err)
	}
	crates, err := filepath.Glob(filepath.Join(outputRoot, "server", "*", "*", RustManifestFile))
	if err != nil {
		return fail(err)
	}
	used := map[string]bool{}
	for _, file := range crates {
		var crate struct {
			Dependencies map[string]any `toml:"dependencies"`
		}
		if _, err := toml.DecodeFile(file, &crate); err != nil {
			return fail(err)
		}
		for _, spec := range crate.Dependencies {
			if s, ok := spec.(map[string]any); ok {
				if p, ok := s["path"].(string); ok {
					used[filepath.Join(filepath.Dir(file), filepath.FromSlash(p))] = true
				}
			}
		}
	}
	for _, e := range entries {
		dir := filepath.Join(root, e.Name())
		if !e.IsDir() || used[dir] {
			continue
		}
		if manifest, err := os.ReadFile(filepath.Join(dir, RustManifestFile)); err != nil || !bytes.HasPrefix(manifest, []byte(rustWorkspaceHeader)) {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			return fail(err)
		}
	}
	if left, err := os.ReadDir(root); err == nil && len(left) == 0 {
		if err := os.Remove(root); err != nil {
			return fail(err)
		}
	}
	return nil
}
