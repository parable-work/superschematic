package schemadeps

import (
	"fmt"
	"path/filepath"
)

// ProviderKind is the kind of a runtime provider package in the graph
// (D159): a package an API's config lists, which the API package of its
// language links, and which no service builds.
const ProviderKind = "provider"

// Provider is a runtime provider package an API's config lists (D159), by
// its directory or its npm name (#608).
type Provider struct {
	// Service is the API whose config lists it.
	Service string
	// Name is the provider's name in its manifest; its packages' ID is
	// <name>-provider.
	Name string
	// Dir is the package's directory, absolute.
	Dir string
	// Package, Module and Crate are the names of the npm package, the Go
	// module and the Rust crate the directory holds, empty for one it does
	// not hold.
	Package, Module, Crate string
}

// AddProviders adds to the graph the runtime provider packages providers
// lists (D159, #608), and to each API package that links one an edge to
// it: a TypeScript API package to its npm package, a Go one to its module
// and a Rust one to its crate, the API package being the one of the
// listing service in that language. A provider package is a package of
// kind provider, with the ID <name>-provider, its directory relative to
// distRoot as its Path (outside the output root, so it starts with ".."
// as a rule) and no Service. A provider no API package of its languages
// links stays out. Two directories that give one ID in a language are
// refused: the graph names a package once.
func (g *Graph) AddProviders(distRoot string, providers []Provider) error {
	if len(providers) == 0 {
		return nil
	}
	root, err := filepath.Abs(distRoot)
	if err != nil {
		return err
	}
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	apis := map[string]int{} // language/service -> index of its API package
	byKey := map[string]int{}
	for i, p := range g.Packages {
		byKey[p.Key()] = i
		if p.Kind == "api" && p.Service != "" {
			apis[p.Language+"/"+p.Service] = i
		}
	}
	for _, p := range providers {
		dir := p.Dir
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			// A workspace's node_modules links the package's own directory.
			dir = real
		}
		rel, err := filepath.Rel(root, dir)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		id := p.Name + "-" + ProviderKind
		for _, lang := range []struct{ language, name string }{{"typescript", p.Package}, {"go", p.Module}, {"rust", p.Crate}} {
			api, ok := apis[lang.language+"/"+p.Service]
			if lang.name == "" || !ok {
				continue
			}
			node := Package{ID: id, Language: lang.language, Kind: ProviderKind, Name: lang.name, Path: rel}
			if i, ok := byKey[node.Key()]; ok {
				if existing := g.Packages[i]; existing.Kind != ProviderKind || existing.Path != rel {
					return fmt.Errorf("schemadeps: runtime provider %s of %s at %s gives the %s package %s, which %s holds too; the graph names a package once, so give the provider another name", p.Name, p.Service, rel, lang.language, id, existing.Path)
				}
			} else {
				byKey[node.Key()] = len(g.Packages)
				g.Packages = append(g.Packages, node)
			}
			g.Packages[api].Deps = append(g.Packages[api].Deps, id)
		}
	}
	g.normalize()
	return nil
}

// EmitWithProviders collects the graph as EmitFromDist does, adds the
// runtime provider packages (AddProviders) and writes it.
func EmitWithProviders(distRoot string, producers map[string]string, copyPath string, providers []Provider, extra ...PackageDir) error {
	g, err := CollectFromDist(distRoot, producers, extra...)
	if err != nil {
		return err
	}
	if err := g.AddProviders(distRoot, providers); err != nil {
		return err
	}
	return writeGraph(distRoot, copyPath, g)
}
