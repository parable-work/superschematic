package generator

import (
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

var updateNamingGolden = flag.Bool("update", false, "rewrite the naming golden manifests")

const namingFixtureDir = "testdata/naming"

// parableCoordinateRE matches every default coordinate a manifest could
// carry: module roots, npm scope, Python module prefixes, crate prefixes and
// the scalar library. The author string "Parable Platform" is branding, not a
// coordinate, and is out of scope here.
var parableCoordinateRE = regexp.MustCompile(`parable-platform|@parable-platform|@psgen/scalar-lib|parable_types_|parable_scalars|parable-scalar|parable-[a-z0-9-]+-(types|sdk|api)|parable-scalars-core|psgen-http-runtime`)

// TestRunWithFixtureNamingEmitsFixtureNames builds the fixture services with
// a superschematic.toml whose every value differs from the Parable defaults,
// scans every generated file for a default coordinate, and compares the
// emitted manifests (go.mod, package.json, pyproject.toml, Cargo.toml)
// against goldens. Regenerate the goldens with:
//
//	go test ./internal/generator -run TestRunWithFixtureNamingEmitsFixtureNames -update
func TestRunWithFixtureNamingEmitsFixtureNames(t *testing.T) {
	names, err := naming.LoadFile(filepath.Join(namingFixtureDir, naming.FileName))
	if err != nil {
		t.Fatalf("load fixture naming: %v", err)
	}
	// Every string key must be set in the fixture: a key added to Naming
	// without a fixture value would otherwise keep its default and the scan
	// below would pass without exercising it.
	defaults := reflect.ValueOf(naming.Default())
	fixture := reflect.ValueOf(names)
	for i := 0; i < fixture.NumField(); i++ {
		field := fixture.Type().Field(i)
		if field.Type.Kind() != reflect.String {
			continue
		}
		if fixture.Field(i).String() == defaults.Field(i).String() {
			t.Errorf("fixture %s leaves Naming.%s (%s) at its default %q", naming.FileName, field.Name, field.Tag.Get("toml"), defaults.Field(i).String())
		}
	}

	fixedClock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	outputRoot := t.TempDir()
	loadDependency := func(name string) (*ir.Schema, error) {
		return loader.LoadService(filepath.Join(tsFixtures, name))
	}

	// fixture-db enables go and typescript types; widen it to every type
	// language so each manifest template renders.
	dbSchema, dbCfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, "fixture-db"))
	if err != nil {
		t.Fatalf("load fixture-db: %v", err)
	}
	dbCfg.Outputs["types"] = map[string]any{
		"go":         map[string]any{"enabled": true},
		"typescript": map[string]any{"enabled": true},
		"python":     map[string]any{"enabled": true},
		"rust":       map[string]any{"enabled": true},
	}
	if _, err := Run(dbSchema, dbCfg, Options{OutputRoot: outputRoot, Naming: names, Clock: fixedClock}); err != nil {
		t.Fatalf("run fixture-db: %v", err)
	}

	apiSchema, apiCfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, "fixture-api"))
	if err != nil {
		t.Fatalf("load fixture-api: %v", err)
	}
	apiCfg.Outputs["sdk"] = map[string]any{
		"typescript": map[string]any{"enabled": true},
		"go":         map[string]any{"enabled": true},
		"python":     map[string]any{"enabled": true},
		"rust":       map[string]any{"enabled": true},
	}
	if _, err := Run(apiSchema, apiCfg, Options{
		OutputRoot:     outputRoot,
		Naming:         names,
		Clock:          fixedClock,
		LoadDependency: loadDependency,
	}); err != nil {
		t.Fatalf("run fixture-api: %v", err)
	}

	if err := filepath.WalkDir(outputRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		got, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if m := parableCoordinateRE.Find(got); m != nil {
			rel, _ := filepath.Rel(outputRoot, path)
			t.Errorf("%s still carries the default coordinate %q", rel, m)
		}
		return nil
	}); err != nil {
		t.Fatalf("scan generated tree: %v", err)
	}

	manifests := []string{
		"types/go/fixture-db/go.mod",
		"types/typescript/fixture-db/package.json",
		"types/python/fixture-db/pyproject.toml",
		"types/rust/fixture-db/Cargo.toml",
		"orm/fixture-db/go.mod",
		"api/fixture-api/go.mod",
		"types/typescript/fixture-api/package.json",
		"sdk/typescript/fixture-api/package.json",
		"sdk/go/fixture-api/go.mod",
		"sdk/python/fixture-api/pyproject.toml",
		"sdk/rust/fixture-api/Cargo.toml",
	}
	goldenDir := filepath.Join(namingFixtureDir, "golden")
	for _, rel := range manifests {
		got, err := os.ReadFile(filepath.Join(outputRoot, rel))
		if err != nil {
			t.Errorf("read generated %s: %v", rel, err)
			continue
		}
		goldenPath := filepath.Join(goldenDir, rel)
		if *updateNamingGolden {
			if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
				t.Fatalf("create golden dir: %v", err)
			}
			if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
				t.Fatalf("write golden %s: %v", rel, err)
			}
			continue
		}
		want, err := os.ReadFile(goldenPath)
		if err != nil {
			t.Errorf("read golden %s: %v (run with -update to create)", rel, err)
			continue
		}
		if string(got) != string(want) {
			t.Errorf("%s differs from golden (run with -update to accept):\n--- got ---\n%s", rel, got)
		}
	}
}
