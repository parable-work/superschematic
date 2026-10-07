package naming

import (
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/release"
)

// Pins are the runtime Go modules a generated go.mod takes from the module
// proxy, by module path, each with the version it takes (D47, amended). A
// runtime module requires its siblings at versions only a checkout's
// replace resolves, so a go.mod that reaches a pinned module replaces
// every version of it with its pin.
type Pins map[string]string

// Pin is one of Pins: a module path and the version it is pinned at.
type Pin struct {
	Module  string
	Version string
}

// ReleasePins are the runtime Go modules the code that release rel
// generates takes from the module proxy: each that no [paths] key names a
// checkout of, superschematic's at the release's tag, and superscalar's Go
// binding at the version the release links and builds its static archives
// from.
// A module the naming file renames is no module of the release, and is not
// pinned. A binary built from a checkout names no release, and pins none.
func (n Naming) ReleasePins(paths LocalPaths, rel release.Release) Pins {
	if rel.Version == "" {
		return nil
	}
	n, d := n.OrDefault(), Default()
	pins := Pins{}
	for _, m := range []struct{ module, published, dir string }{
		{n.SchemaIRGoModule, d.SchemaIRGoModule, paths.SchemaIR},
		{n.SchemaRuntimeGoModule, d.SchemaRuntimeGoModule, paths.SchemaRuntimeGo},
		{n.HTTPRuntimeGoModule, d.HTTPRuntimeGoModule, paths.HTTPRuntimeGo},
		{n.VersionGraphGoModule, d.VersionGraphGoModule, paths.VersionGraphGo},
	} {
		if m.dir == "" && m.module == m.published {
			pins[m.module] = rel.ModuleVersion()
		}
	}
	if paths.ScalarGo == "" && rel.ScalarGo != "" && n.ScalarGoModule == d.ScalarGoModule {
		pins[n.ScalarGoModule] = rel.ScalarGo
	}
	return pins
}

// Of are the pins of modules, those a generated go.mod reaches; a module
// with no pin is left out.
func (p Pins) Of(modules ...string) Pins {
	var out Pins
	for _, module := range modules {
		if version, ok := p[module]; ok {
			if out == nil {
				out = Pins{}
			}
			out[module] = version
		}
	}
	return out
}

// Version is the version a generated go.mod requires module at: its pin,
// else unpinned, the version a checkout's replace resolves.
func (p Pins) Version(module, unpinned string) string {
	if version, ok := p[module]; ok {
		return version
	}
	return unpinned
}

// List is the pins sorted by module path, the replaces a go.mod writes.
func (p Pins) List() []Pin {
	out := make([]Pin, 0, len(p))
	for module, version := range p {
		out = append(out, Pin{module, version})
	}
	slices.SortFunc(out, func(a, b Pin) int { return strings.Compare(a.Module, b.Module) })
	return out
}
