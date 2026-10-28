package generator

import (
	"path/filepath"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/rustrestgen"
	"github.com/parable-work/superschematic/internal/generator/tsutil"
)

// warnScaffoldDependencies warns when the TypeScript package at dir, which
// the build scaffolded once and no longer writes (D51, D55), lacks one of
// deps in every dependency table of its package.json (D100): what names
// the package, and why says what needs deps. The warning gives the lines
// to add; the build does not add them, since the package is the author's.
func (r run) warnScaffoldDependencies(what, dir string, deps []tsutil.Dependency, why string) {
	missing, err := tsutil.MissingDependencies(dir, deps)
	if err != nil {
		r.Warnf("%s, at %s: reading what its package.json depends on: %v", what, r.repositoryPath(dir), err)
		return
	}
	names := make([]string, len(missing))
	for i, dep := range missing {
		names[i] = dep.Name
	}
	r.warnScaffoldLacks(what, dir, names, why, "package's package.json", tsutil.DependencyLines(missing), "its dependencies")
}

// warnCrateDependencies is warnScaffoldDependencies for the Rust crate at
// dir, which the build scaffolded once and no longer writes (D57): a
// dependency of deps its Cargo.toml lacks (D100).
func (r run) warnCrateDependencies(what, dir string, deps []rustrestgen.CargoDependency, why string) {
	missing, err := rustrestgen.MissingCargoDependencies(dir, deps)
	if err != nil {
		r.Warnf("%s, at %s: reading what its Cargo.toml depends on: %v", what, r.repositoryPath(dir), err)
		return
	}
	names := make([]string, len(missing))
	lines := make([]string, len(missing))
	for i, dep := range missing {
		names[i], lines[i] = dep.Crate, "    "+dep.Line()
	}
	r.warnScaffoldLacks(what, dir, names, why, "crate's Cargo.toml", strings.Join(lines, "\n"), "its [dependencies]")
}

// warnScaffoldLacks warns that the scaffold at dir, what, lacks the
// dependencies names, which why says what needs, and that manifest takes
// lines in where.
func (r run) warnScaffoldLacks(what, dir string, names []string, why, manifest, lines, where string) {
	if len(names) == 0 {
		return
	}
	r.Warnf("%s, at %s, does not depend on %s, which %s; the build wrote the %s once and does not write it again, so add\n\n%s\n\nto %s (D100)",
		what, r.repositoryPath(dir), joinAnd(names), why, manifest, lines, where)
}

// repositoryPath is dir from the repository root, as a message names a
// directory of the project, or dir itself when it lies elsewhere.
func (r run) repositoryPath(dir string) string {
	rel, err := filepath.Rel(r.Options.RepositoryRoot, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return dir
	}
	return filepath.ToSlash(rel)
}
