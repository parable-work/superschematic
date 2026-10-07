package generator

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/release"
	ir "github.com/parable-work/superschematic/ir"
)

// TestReleaseGoModulesPinTheRuntimeModules builds the fixture services as
// a release, in a project whose naming file has no [paths], and reads
// every go.mod the build writes: each requires every runtime module it
// requires at the release's pin, and replaces every version of each it
// requires, and of the scalar library and the schema IR, which every
// generated Go module reaches, with that pin (D47, amended). No go.mod
// requires a runtime module at a version only a checkout resolves.
func TestReleaseGoModulesPinTheRuntimeModules(t *testing.T) {
	names := naming.Default()
	rel := release.Release{Version: "1.2.3", ScalarGo: "v0.0.0-20260928143325-10cf493f485e"}
	pins := names.ReleasePins(naming.LocalPaths{}, rel)
	outputRoot := t.TempDir()
	clock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	loadDependency := func(name string) (*ir.Schema, error) {
		return loader.LoadService(filepath.Join(tsFixtures, name))
	}
	for _, dir := range []string{
		filepath.Join(tsFixtures, "fixture-db"),
		filepath.Join(tsFixtures, "fixture-api"),
		filepath.Join(tsFixtures, "fixture-version-graph-db"),
		filepath.Join("testdata", "services", "fixture-env-go"),
	} {
		schema, cfg, err := loader.LoadServiceWithConfig(dir)
		if err != nil {
			t.Fatalf("load %s: %v", dir, err)
		}
		cfg.Outputs["types"] = map[string]any{"go": map[string]any{"enabled": true}}
		if _, ok := cfg.Outputs["sdk"]; ok {
			cfg.Outputs["sdk"] = map[string]any{"go": map[string]any{"enabled": true}}
		}
		if _, err := Run(schema, cfg, Options{
			OutputRoot:     outputRoot,
			Naming:         names,
			Clock:          clock,
			LoadDependency: loadDependency,
			Release:        &rel,
		}); err != nil {
			t.Fatalf("run %s: %v", dir, err)
		}
	}

	var modules []string
	if err := filepath.WalkDir(outputRoot, func(path string, d os.DirEntry, err error) error {
		if err == nil && d.Name() == "go.mod" {
			name, _ := filepath.Rel(outputRoot, path)
			modules = append(modules, filepath.ToSlash(name))
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"types/go/fixture-db/go.mod", "orm/fixture-db/go.mod",
		"types/go/fixture-api/go.mod", "api/fixture-api/go.mod", "sdk/go/fixture-api/go.mod",
		"types/go/fixture-version-graph-db/go.mod", "orm/fixture-version-graph-db/go.mod",
		"api/fixture-env-go/go.mod",
	} {
		if !slices.Contains(modules, want) {
			t.Errorf("the build wrote no %s; it wrote %v", want, modules)
		}
	}

	reached := []string{names.ScalarGoModule, names.SchemaIRGoModule}
	for _, module := range modules {
		data, err := os.ReadFile(filepath.Join(outputRoot, module))
		if err != nil {
			t.Fatal(err)
		}
		requires, replaces := readGoMod(string(data))
		for path, version := range requires {
			if pin, ok := pins[path]; ok && version != pin {
				t.Errorf("%s requires %s %s, not the release's %s", module, path, version, pin)
			}
		}
		for path, pin := range pins {
			_, required := requires[path]
			if !required && !slices.Contains(reached, path) {
				continue
			}
			if got, want := replaces[path], path+" "+pin; got != want {
				t.Errorf("%s replaces %s with %q, want %q:\n%s", module, path, got, want, data)
			}
		}
	}
}

// readGoMod reads a go.mod's requires, path to version, and its replaces,
// the replaced path to the replacement as written.
func readGoMod(data string) (requires, replaces map[string]string) {
	requires, replaces = map[string]string{}, map[string]string{}
	block := ""
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if i := strings.Index(line, "//"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		switch {
		case line == "":
			continue
		case line == ")":
			block = ""
			continue
		case line == "require (" || line == "replace (":
			block = strings.TrimSuffix(line, " (")
			continue
		}
		directive := block
		if directive == "" {
			directive, line, _ = strings.Cut(line, " ")
		}
		switch directive {
		case "require":
			if fields := strings.Fields(line); len(fields) == 2 {
				requires[fields[0]] = fields[1]
			}
		case "replace":
			if from, to, ok := strings.Cut(line, "=>"); ok {
				replaces[strings.Fields(from)[0]] = strings.TrimSpace(to)
			}
		}
	}
	return requires, replaces
}
