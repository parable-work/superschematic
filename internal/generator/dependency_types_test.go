package generator

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	ir "github.com/parable-work/superschematic/ir"
)

// dependencyTypesFixtures holds shop-orders, whose types are enabled in
// every language, and its dependencies, which generate Go and TypeScript
// types only. shop-orders imports an object type from shop-common, an enum
// from shop-db and only a scalar from shop-ids.
const dependencyTypesFixtures = "testdata/dependency-types/services"

// loadDependencyTypesFixture loads shop-orders and returns it with the
// options that load its dependencies and read their configs from the
// fixture tree. widen, when set, enables every type language in each
// dependency's config.
func loadDependencyTypesFixture(t *testing.T, widen bool) (*ir.Schema, *schemaconfig.SchemaConfig, Options) {
	t.Helper()
	schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(dependencyTypesFixtures, "shop-orders"))
	if err != nil {
		t.Fatalf("load shop-orders: %v", err)
	}
	opts := Options{
		OutputRoot: t.TempDir(),
		Naming:     naming.Default(),
		LoadDependency: func(name string) (*ir.Schema, error) {
			return loader.LoadService(filepath.Join(dependencyTypesFixtures, name))
		},
		DependencyConfig: func(name string) (*schemaconfig.SchemaConfig, bool) {
			_, depCfg, err := loader.LoadServiceWithConfig(filepath.Join(dependencyTypesFixtures, name))
			if err != nil {
				return nil, false
			}
			if widen {
				depCfg.Outputs["types"] = map[string]any{
					LangGo:         map[string]any{"enabled": true},
					LangTypeScript: map[string]any{"enabled": true},
					LangPython:     map[string]any{"enabled": true},
					LangRust:       map[string]any{"enabled": true},
				}
			}
			return depCfg, true
		},
	}
	return schema, cfg, opts
}

// A type library that imports a dependency whose library in that language
// is off fails the run before anything is written, one line per
// dependency. shop-ids is declared and imported from, but only for a scalar,
// which every language regenerates locally, so it needs no Rust or Python
// types.
func TestRunRefusesTypesWhoseDependencyTypesAreOff(t *testing.T) {
	schema, cfg, opts := loadDependencyTypesFixture(t, false)
	_, err := Run(schema, cfg, opts)
	want := "shop-orders generates Python and Rust types, which use shop-common's Python and Rust types; enable outputs.types.python and outputs.types.rust in shop-common\n" +
		"shop-orders generates Python and Rust types, which use shop-db's Python and Rust types; enable outputs.types.python and outputs.types.rust in shop-db"
	if err == nil || err.Error() != want {
		t.Fatalf("Run error = %v, want %q", err, want)
	}
	entries, err := os.ReadDir(opts.OutputRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("the refused run wrote %v", entries)
	}
}

// Without dependency configs (a single build) the run cannot check its
// dependencies, so it says what it did not check and generates.
func TestRunLogsUncheckedDependencyTypes(t *testing.T) {
	schema, cfg, opts := loadDependencyTypesFixture(t, false)
	opts.DependencyConfig = nil
	var log bytes.Buffer
	opts.Log = &log
	if _, err := Run(schema, cfg, opts); err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := "  - not checked: shop-common and shop-db must enable outputs.types.go, outputs.types.python, outputs.types.rust and outputs.types.typescript, since shop-orders's types import theirs; build --with-deps and build-all check it\n"
	if !strings.Contains(log.String(), want) {
		t.Errorf("log does not say what it did not check:\n%s", log.String())
	}
}

// TypeDependencies is the check's view of what the type generators import;
// each generator decides it on its own. Every dependency package a
// generated manifest or module names must be one TypeDependencies reports,
// or the check would pass a package that cannot build.
func TestTypeDependenciesCoverTheGeneratedTypePackages(t *testing.T) {
	schema, cfg, opts := loadDependencyTypesFixture(t, true)
	if _, err := Run(schema, cfg, opts); err != nil {
		t.Fatalf("Run with every dependency language enabled: %v", err)
	}
	deps := map[string]*ir.Schema{}
	for _, dep := range cfg.Dependencies {
		depSchema, err := opts.LoadDependency(dep.Name)
		if err != nil {
			t.Fatal(err)
		}
		deps[dep.Name] = depSchema
	}
	want := codegen.TypeDependencies(schema, deps)
	if !slices.Equal(want, []string{"shop-common", "shop-db"}) {
		t.Fatalf("TypeDependencies = %v, want [shop-common shop-db]", want)
	}

	n := naming.Default()
	read := func(rel string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(opts.OutputRoot, rel))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	goMod := read("types/go/shop-orders/go.mod")
	// go.mod replaces every declared dependency; what it needs is what it
	// requires.
	goRequires, _, _ := strings.Cut(goMod, "\nreplace")
	packageJSON := read("types/typescript/shop-orders/package.json")
	cargo := read("types/rust/shop-orders/Cargo.toml")
	pyTypes := read(filepath.Join("types/python/shop-orders", n.PythonTypesModule("shop_orders"), "types.py"))

	for _, dep := range []string{"shop-common", "shop-db", "shop-ids"} {
		expected := slices.Contains(want, dep)
		pyModule := n.PythonTypesModule(strings.ReplaceAll(dep, "-", "_"))
		for lang, found := range map[string]bool{
			LangGo:         strings.Contains(goRequires, n.GoTypesModule(dep)+" "),
			LangTypeScript: strings.Contains(packageJSON, `"`+n.NpmTypesPackage(dep)+`"`),
			LangRust:       strings.Contains(cargo, n.RustTypesCrate(dep)+` = { path = "../`+dep+`" }`),
			LangPython:     regexp.MustCompile(`from ` + regexp.QuoteMeta(pyModule) + ` import`).MatchString(pyTypes),
		} {
			if found && !expected {
				t.Errorf("the %s types import %s, which TypeDependencies does not report", lang, dep)
			}
			if !found && expected {
				t.Errorf("the %s types do not import %s; the test no longer shows what it should", lang, dep)
			}
		}
	}
}
