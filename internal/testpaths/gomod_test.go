package testpaths

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// A module that ir or a runtime module under runtime/*/go requires directly
// is required at one version by every one of them that requires it, so each
// module's tests run against the version an application linking several of
// them gets (Go's minimal version selection picks the highest).
func TestRuntimeGoModulesAgreeOnSharedRequirements(t *testing.T) {
	for module, byMod := range RuntimeRequirements(t) {
		direct := false
		versions := map[string][]string{}
		for mod, req := range byMod {
			direct = direct || !req.Indirect
			versions[req.Version] = append(versions[req.Version], mod)
		}
		if !direct || len(versions) < 2 {
			continue
		}
		var lines []string
		for version, mods := range versions {
			sort.Strings(mods)
			lines = append(lines, version+": "+strings.Join(mods, ", "))
		}
		sort.Strings(lines)
		t.Errorf("%s is required at %d versions; require the newest in each:\n  %s", module, len(versions), strings.Join(lines, "\n  "))
	}
}

// templateRequireRE is a require line with a literal version in a
// generator's go.mod template.
var templateRequireRE = regexp.MustCompile(`(?m)^\s+(\S+) (v\d+\.\d+\.\d+\S*)$`)

// A module that a generator's go.mod template requires at a literal version
// is required at the version ir and the runtime modules require it at, so a
// generated module builds against the version they are tested with.
func TestGeneratorTemplatesRequireTheRuntimeVersions(t *testing.T) {
	root := RepoRoot(t)
	runtime := RuntimeRequirements(t)
	checked := 0
	err := filepath.WalkDir(filepath.Join(root, "internal", "generator"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == "testdata" {
			return filepath.SkipDir
		}
		if d.IsDir() || filepath.Ext(path) != ".tmpl" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		for _, m := range templateRequireRE.FindAllStringSubmatch(string(data), -1) {
			module, version := m[1], m[2]
			for mod, req := range runtime[module] {
				checked++
				if req.Version != version {
					t.Errorf("%s requires %s %s; %s requires %s", rel, module, version, mod, req.Version)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("no generator template requires a module the runtime modules require: is the pattern stale?")
	}
}
