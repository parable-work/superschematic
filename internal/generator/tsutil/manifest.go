package tsutil

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// ManifestFile is a package's manifest.
const ManifestFile = "package.json"

// WorkspaceSpec is the range a package.json names a member of the Bun
// workspace by.
const WorkspaceSpec = "workspace:*"

// ManifestTables are the members of a package.json whose entries are
// dependencies.
var ManifestTables = []string{"dependencies", "devDependencies", "optionalDependencies", "peerDependencies"}

// Dependency is a package a package.json depends on, and the range it
// names it by.
type Dependency struct {
	Name string
	Spec string
}

// MissingDependencies returns each of deps the package.json in dir names
// in none of its dependency tables, in the order of deps (D100): a
// package a scaffold depends on that the package, which the build wrote
// once and no longer writes, has lost or never had. A directory with no
// package.json misses them all.
func MissingDependencies(dir string, deps []Dependency) ([]Dependency, error) {
	path := filepath.Join(dir, ManifestFile)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return slices.Clone(deps), nil
	}
	if err != nil {
		return nil, err
	}
	var manifest map[string]json.RawMessage
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	listed := map[string]bool{}
	for _, table := range ManifestTables {
		raw, ok := manifest[table]
		if !ok {
			continue
		}
		var entries map[string]json.RawMessage
		if err := json.Unmarshal(raw, &entries); err != nil {
			return nil, fmt.Errorf("%s: %s: %w", path, table, err)
		}
		for name := range entries {
			listed[name] = true
		}
	}
	var missing []Dependency
	for _, dep := range deps {
		if !listed[dep.Name] {
			missing = append(missing, dep)
		}
	}
	return missing, nil
}

// DependencyLines writes deps as a package.json's dependencies hold them,
// a line each, indented four spaces: the lines a message says to add.
func DependencyLines(deps []Dependency) string {
	lines := make([]string, len(deps))
	for i, dep := range deps {
		name, _ := json.Marshal(dep.Name)
		spec, _ := json.Marshal(dep.Spec)
		lines[i] = fmt.Sprintf("    %s: %s", name, spec)
	}
	return strings.Join(lines, ",\n")
}
