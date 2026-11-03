package memorystoregen_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// TestGoDefinitionsRunOnTheInProcessStore: shop-cache's Go definitions,
// built with shop-cache-common's Go types against the checkout's runtime
// modules, compile, vet, and pass testdata/gotest/definitions_test.go,
// which uses every definition on the runtime's in-process store.
func TestGoDefinitionsRunOnTheInProcessStore(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the compile check in -short mode")
	}
	paths := testpaths.Local(t)
	f := load(t)
	out := t.TempDir()
	for _, name := range []string{"shop-cache-common", store} {
		cfg := *f.configs[name]
		if name == store {
			cfg.Outputs = map[string]any{"types": map[string]any{"go": map[string]any{"enabled": true}}}
		}
		if _, err := generator.Run(f.schemas[name], &cfg, generator.Options{
			OutputRoot:     out,
			Paths:          paths,
			Naming:         naming.Default(),
			LoadDependency: f.dependency,
			DependencyConfig: func(name string) (*schemaconfig.SchemaConfig, bool) {
				cfg, ok := f.configs[name]
				return cfg, ok
			},
		}); err != nil {
			t.Fatalf("build %s: %v", name, err)
		}
	}
	dir := generator.TypesDir(out, "go", store)
	test, err := os.ReadFile(filepath.Join("testdata", "gotest", "definitions_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "definitions_test.go"), test, 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := goCommand(dir, "mod", "tidy"); err != nil {
		t.Skipf("go mod tidy failed (likely offline): %v\n%s", err, out)
	}
	for _, args := range [][]string{{"vet", "./..."}, {"test", "-count=1", "-run", "^TestDefinitions$", "./..."}} {
		if out, err := goCommand(dir, args...); err != nil {
			t.Fatalf("go %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
		}
	}
}

func goCommand(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	// PWD keeps go in dir as the build saw it, through a symlinked
	// temporary directory, which the replaces' relative paths count from.
	cmd.Env = append(os.Environ(), "PWD="+dir, "GOFLAGS=-mod=mod")
	return cmd.CombinedOutput()
}
