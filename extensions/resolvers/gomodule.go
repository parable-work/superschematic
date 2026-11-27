package resolvers

import (
	"encoding/json"
	"fmt"

	"golang.org/x/mod/modfile"

	"github.com/parable-work/superschematic/registry"
)

// resolveGoModule is the `go-module` resolver. A Go module resolves to:
//
//   - every tracked file under its directory, without the modules nested
//     in it, which are their own;
//   - each module a `replace` of its go.mod points at by a path, and each
//     those point at in turn;
//   - the go.work above it, with its go.work.sum, and each module the
//     workspace uses that one of the modules above requires.
//
// A replaced module the commit does not hold is reported as generated.
// So is every directory a go.work uses and the commit does not hold: the
// go command refuses a workspace with a member it cannot load, so the
// task cannot run without what writes it.
//
// The module cache and the network are not the commit's: go.sum, which
// is the module's, holds them to what was fetched.
func resolveGoModule(req registry.TaskResolveRequest) (registry.TaskResolution, error) {
	var spec unitSpec
	if err := json.Unmarshal(req.Spec, &spec); err != nil {
		return registry.TaskResolution{}, err
	}
	dir, err := cleanDir(spec.Dir)
	if err != nil {
		return registry.TaskResolution{}, err
	}
	if !has(req.Tree, file(dir, "go.mod")) {
		return registry.TaskResolution{}, fmt.Errorf("the commit holds no Go module at %s: no tracked %s", show(dir), file(dir, "go.mod"))
	}
	g := &goResolution{resolution: newResolution(req.Tree), requires: map[string]bool{}}
	if err := g.module(dir, ""); err != nil {
		return registry.TaskResolution{}, err
	}
	if err := g.workspace(dir); err != nil {
		return registry.TaskResolution{}, err
	}
	return g.result(), nil
}

type goResolution struct {
	*resolution
	// requires are the module paths the modules taken so far require.
	requires map[string]bool
}

// parseGoMod reads a go.mod of the commit.
func (g *goResolution) parseGoMod(name string) (*modfile.File, error) {
	data, err := g.tree.Read(name)
	if err != nil {
		return nil, err
	}
	// The strict parse: the lax one skips `replace`, which is what a
	// module's local dependencies are read from.
	parsed, err := modfile.Parse(name, data, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return parsed, nil
}

// module takes the module at dir, which the go.mod named from points at.
func (g *goResolution) module(dir, from string) error {
	manifest := file(dir, "go.mod")
	if !has(g.tree, manifest) {
		g.generate(show(dir), from)
		return nil
	}
	if !g.visit(manifest) {
		return nil
	}
	parsed, err := g.parseGoMod(manifest)
	if err != nil {
		return err
	}
	g.addUnder(dir, nested(g.tree, dir, "go.mod"))
	for _, req := range parsed.Require {
		g.requires[req.Mod.Path] = true
	}
	for _, replace := range parsed.Replace {
		if replace.New.Version != "" || !modfile.IsDirectoryPath(replace.New.Path) {
			continue
		}
		target, err := joinDir(dir, replace.New.Path, manifest)
		if err != nil {
			return err
		}
		if err := g.module(target, manifest); err != nil {
			return err
		}
	}
	return nil
}

// workspace takes the go.work at or above dir: the file, its sum, and
// each module it uses that a module taken so far requires. A used
// directory the commit does not hold is generated.
func (g *goResolution) workspace(dir string) error {
	var work string
	for _, above := range ancestors(dir) {
		if has(g.tree, file(above, "go.work")) {
			work = file(above, "go.work")
			break
		}
	}
	if work == "" {
		return nil
	}
	data, err := g.tree.Read(work)
	if err != nil {
		return err
	}
	parsed, err := modfile.ParseWork(work, data, nil)
	if err != nil {
		return fmt.Errorf("%s: %w", work, err)
	}
	g.add(work)
	g.add(work + ".sum")
	workDir := parentOf(work)
	// The used modules by their module path. One the modules above
	// require is taken, and what it requires may take another.
	used := map[string]string{}
	for _, use := range parsed.Use {
		target, err := joinDir(workDir, use.Path, work)
		if err != nil {
			return err
		}
		manifest := file(target, "go.mod")
		if !has(g.tree, manifest) {
			g.generate(show(target), work)
			continue
		}
		mod, err := g.parseGoMod(manifest)
		if err != nil {
			return err
		}
		if mod.Module != nil {
			used[mod.Module.Mod.Path] = target
		}
	}
	for changed := true; changed; {
		changed = false
		for modPath, target := range used {
			if !g.requires[modPath] || g.seen[file(target, "go.mod")] {
				continue
			}
			if err := g.module(target, work); err != nil {
				return err
			}
			changed = true
		}
	}
	return nil
}
