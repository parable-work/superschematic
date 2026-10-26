package memorystoregen_test

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/memorystoregen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	ir "github.com/parable-work/superschematic/ir"
)

// update rewrites the goldens under testdata/golden:
//
//	go test ./internal/generator/memorystoregen -update
var update = flag.Bool("update", false, "rewrite the golden files")

// The fixtures: shop-cache, the MemoryStore service, and shop-cache-common,
// the General service whose StockLevel a channel carries and whose Region
// a part names.
const (
	fixtures = "testdata/services"
	store    = "shop-cache"
	golden   = "testdata/golden"
)

// fixture is the fixtures, loaded.
type fixture struct {
	schemas map[string]*ir.Schema
	configs map[string]*schemaconfig.SchemaConfig
}

// load reads both fixtures, shop-cache with its dependency through the
// fixtures.
func load(t *testing.T) fixture {
	t.Helper()
	f := fixture{schemas: map[string]*ir.Schema{}, configs: map[string]*schemaconfig.SchemaConfig{}}
	for _, name := range []string{"shop-cache-common", store} {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(fixtures, name), loader.WithDependencyLoader(f.dependency))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		f.schemas[name], f.configs[name] = schema, cfg
	}
	return f
}

func (f fixture) dependency(name string) (*ir.Schema, error) {
	if schema, ok := f.schemas[name]; ok {
		return schema, nil
	}
	return nil, fmt.Errorf("no fixture %s", name)
}

// Build runs shop-cache's build with outputs.types enabling langs, into a
// new output root it returns, with the error the build returned.
func Build(t *testing.T, langs ...string) (string, error) {
	t.Helper()
	f := load(t)
	cfg := *f.configs[store]
	types := map[string]any{}
	for _, lang := range langs {
		types[lang] = map[string]any{"enabled": true}
	}
	cfg.Outputs = map[string]any{}
	if len(langs) > 0 {
		cfg.Outputs["types"] = types
	}
	out := t.TempDir()
	_, err := generator.Run(f.schemas[store], &cfg, generator.Options{
		OutputRoot:     out,
		Naming:         naming.Default(),
		LoadDependency: f.dependency,
		DependencyConfig: func(name string) (*schemaconfig.SchemaConfig, bool) {
			cfg, ok := f.configs[name]
			return cfg, ok
		},
	})
	return out, err
}

// CheckGolden compares every file under got with the tree at
// testdata/golden/<name>, or rewrites that tree with -update.
func CheckGolden(t *testing.T, got, name string) {
	t.Helper()
	want := filepath.Join(golden, name)
	gotFiles := tree(t, got)
	if *update {
		if err := os.RemoveAll(want); err != nil {
			t.Fatal(err)
		}
		for rel, contents := range gotFiles {
			path := filepath.Join(want, rel)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	wantFiles := tree(t, want)
	var rels []string
	for rel := range gotFiles {
		rels = append(rels, rel)
	}
	for rel := range wantFiles {
		if _, ok := gotFiles[rel]; !ok {
			rels = append(rels, rel)
		}
	}
	sort.Strings(rels)
	for _, rel := range rels {
		g, gok := gotFiles[rel]
		w, wok := wantFiles[rel]
		switch {
		case !wok:
			t.Errorf("%s: written, but no golden has it; rerun with -update", rel)
		case !gok:
			t.Errorf("%s: the golden has it, but the build wrote none", rel)
		case g != w:
			t.Errorf("%s differs from %s; rerun with -update and review the diff\ngot:\n%s", rel, filepath.Join(want, rel), g)
		}
	}
}

// tree reads every file under dir, by its slash path relative to dir.
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		files[filepath.ToSlash(rel)] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// definitionsGolden builds the fixture's definitions in lang and checks
// them against testdata/golden/<lang>. While the language's entry point
// returns ErrNotBuilt, it skips.
func definitionsGolden(t *testing.T, lang string) {
	t.Helper()
	out, err := Build(t, lang)
	if errors.Is(err, memorystoregen.ErrNotBuilt) {
		t.Skipf("the %s definitions are not built yet: %v", lang, err)
	}
	if err != nil {
		t.Fatal(err)
	}
	CheckGolden(t, generator.TypesDir(out, lang, store), lang)
}

// TestInventoryGolden: shop-cache's build writes inventory.json at
// memorystore/shop-cache, every declaration with its name pattern, its
// parts and their formats, its payload and its service or that it is a
// signal, its TTL or maximum and its file, the reserved rate-limit
// declaration among them.
func TestInventoryGolden(t *testing.T) {
	out, err := Build(t)
	if err != nil {
		t.Fatal(err)
	}
	CheckGolden(t, memorystoregen.Dir(out, store), "inventory")
}

// TestGoDefinitionsGolden: shop-cache's Go definitions, at
// types/go/shop-cache.
func TestGoDefinitionsGolden(t *testing.T) { definitionsGolden(t, "go") }

// TestTypeScriptDefinitionsGolden: shop-cache's TypeScript definitions, at
// types/typescript/shop-cache.
func TestTypeScriptDefinitionsGolden(t *testing.T) { definitionsGolden(t, "typescript") }

// TestRustDefinitionsGolden: shop-cache's Rust definitions, at
// types/rust/shop-cache.
func TestRustDefinitionsGolden(t *testing.T) { definitionsGolden(t, "rust") }

// TestTheDispatcherCallsEachLanguage: a language outputs.types enables
// reaches its entry point, whose error names the store and the language;
// Python, which the kind does not write, is refused.
func TestTheDispatcherCallsEachLanguage(t *testing.T) {
	for _, lang := range memorystoregen.Languages {
		_, err := Build(t, lang)
		if err != nil && !errors.Is(err, memorystoregen.ErrNotBuilt) {
			t.Errorf("%s: %v", lang, err)
		}
		if err != nil && !strings.Contains(err.Error(), "shop-cache") {
			t.Errorf("%s: the error names no store: %v", lang, err)
		}
	}
	if _, err := Build(t, "python"); err == nil || !strings.Contains(err.Error(), "outputs.types.python") {
		t.Errorf("python: err = %v", err)
	}
}
