package testpaths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// GoRequirement is one module a go.mod requires.
type GoRequirement struct {
	// Version is the version required.
	Version string

	// Indirect is true for a requirement marked // indirect.
	Indirect bool
}

// GoRequirements reads the require directives of the go.mod at path, in
// either form (a single line or a block), keyed by module path.
func GoRequirements(t testing.TB, path string) map[string]GoRequirement {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	requirements := map[string]GoRequirement{}
	inBlock := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case inBlock && line == ")":
			inBlock = false
			continue
		case line == "require (":
			inBlock = true
			continue
		case !inBlock:
			rest, ok := strings.CutPrefix(line, "require ")
			if !ok {
				continue
			}
			line = rest
		}
		spec, comment, _ := strings.Cut(line, "//")
		fields := strings.Fields(spec)
		if len(fields) != 2 {
			continue
		}
		requirements[fields[0]] = GoRequirement{
			Version:  fields[1],
			Indirect: strings.TrimSpace(comment) == "indirect",
		}
	}
	return requirements
}

// RuntimeGoModules returns the go.mod of every Go module that generated Go
// code requires, relative to the repository root: ir and each runtime
// module under runtime/*/go.
func RuntimeGoModules(t *testing.T) []string {
	t.Helper()
	root := RepoRoot(t)
	matches, err := filepath.Glob(filepath.Join(root, "runtime", "*", "go", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("testpaths: no runtime/*/go/go.mod")
	}
	modules := []string{filepath.Join("ir", "go.mod")}
	for _, match := range matches {
		rel, err := filepath.Rel(root, match)
		if err != nil {
			t.Fatal(err)
		}
		modules = append(modules, rel)
	}
	return modules
}

// RuntimeRequirements returns every module that a module of
// RuntimeGoModules requires, with the requirement of each go.mod that
// requires it, keyed by that go.mod's path.
func RuntimeRequirements(t *testing.T) map[string]map[string]GoRequirement {
	t.Helper()
	root := RepoRoot(t)
	requirements := map[string]map[string]GoRequirement{}
	for _, mod := range RuntimeGoModules(t) {
		for module, req := range GoRequirements(t, filepath.Join(root, mod)) {
			if requirements[module] == nil {
				requirements[module] = map[string]GoRequirement{}
			}
			requirements[module][mod] = req
		}
	}
	return requirements
}
