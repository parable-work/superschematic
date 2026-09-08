// Package schemadeps is the dependency graph of the generated packages:
// build-all scans the manifests under the output root (package.json, go.mod,
// pyproject.toml, Cargo.toml) for packages under the naming's scopes and
// writes <dist>/.deps.json. Tooling that pins consumers to a closure of
// generated packages reads it back with Read and Closure; the Parable
// extension's pin subcommand is one such consumer (PARABLE-975).
package schemadeps

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	// GraphVersion is the schema version of .deps.json.
	GraphVersion = 1
	// DepsFileName is the graph artifact under the schema dist root.
	DepsFileName = ".deps.json"
)

// Graph is the machine-readable publishable package dependency graph.
type Graph struct {
	Version  int       `json:"version"`
	Packages []Package `json:"packages"`
}

// Package is one generated publishable artifact.
type Package struct {
	ID       string   `json:"id"`
	Language string   `json:"language"`
	Kind     string   `json:"kind"`
	Name     string   `json:"name"`
	Path     string   `json:"path"`
	Deps     []string `json:"deps"`
}

// Key returns language/id for map lookups.
func (p Package) Key() string {
	return p.Language + "/" + p.ID
}

// Write writes the graph as indented JSON to path.
func Write(path string, g *Graph) error {
	if g == nil {
		return fmt.Errorf("schemadeps: nil graph")
	}
	g.normalize()
	data, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return WriteFileAtomic(path, data, 0o644)
}

// Read loads a graph from path.
func Read(path string) (*Graph, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var g Graph
	if err := json.Unmarshal(data, &g); err != nil {
		return nil, fmt.Errorf("schemadeps: parse %s: %w", path, err)
	}
	g.normalize()
	return &g, nil
}

// ByLanguage returns packages for one language keyed by id.
func (g *Graph) ByLanguage(language string) map[string]Package {
	out := make(map[string]Package)
	for _, p := range g.Packages {
		if p.Language == language {
			out[p.ID] = p
		}
	}
	return out
}

// Closure returns root ids plus transitive publishable deps within language.
func (g *Graph) Closure(language string, roots []string) ([]string, error) {
	byID := g.ByLanguage(language)
	seen := make(map[string]bool)
	var order []string
	var visit func(id string) error
	visit = func(id string) error {
		if seen[id] {
			return nil
		}
		pkg, ok := byID[id]
		if !ok {
			return fmt.Errorf("schemadeps: unknown package %q for language %q", id, language)
		}
		seen[id] = true
		for _, dep := range pkg.Deps {
			if err := visit(dep); err != nil {
				return err
			}
		}
		order = append(order, id)
		return nil
	}
	for _, root := range roots {
		if err := visit(root); err != nil {
			return nil, err
		}
	}
	return order, nil
}

func (g *Graph) normalize() {
	if g.Version == 0 {
		g.Version = GraphVersion
	}
	for i := range g.Packages {
		sort.Strings(g.Packages[i].Deps)
		g.Packages[i].Deps = uniqueStrings(g.Packages[i].Deps)
	}
	sort.Slice(g.Packages, func(i, j int) bool {
		if g.Packages[i].Language != g.Packages[j].Language {
			return g.Packages[i].Language < g.Packages[j].Language
		}
		return g.Packages[i].ID < g.Packages[j].ID
	})
}

func uniqueStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// DepsPath returns dist/.deps.json for a schema dist root.
func DepsPath(distRoot string) string {
	return filepath.Join(distRoot, DepsFileName)
}

// WriteFileAtomic writes data to a temp file beside path and renames it in,
// so a reader never sees a partial file. Write uses it for .deps.json; pin
// tooling that rewrites consumer manifests uses it for the same reason.
func WriteFileAtomic(path string, data []byte, mode os.FileMode) error {
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer func() {
		_ = os.Remove(tempName)
	}()
	if err := temp.Chmod(mode); err != nil {
		return errors.Join(err, temp.Close())
	}
	if _, err := temp.Write(data); err != nil {
		return errors.Join(err, temp.Close())
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempName, path)
}
