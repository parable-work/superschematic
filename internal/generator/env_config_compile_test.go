package generator

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// TestEnvConfigModuleCompilesInTree builds a General service with Go types
// and an @envVars class the way build does, with [paths] pointing at this
// checkout, and checks that the standalone env-var loader's go.mod replaces
// the scalar library and the schema IR the types module requires: Go reads
// replace directives only from the main module, so the types module's own
// do not apply and go mod tidy fetches a tag that does not exist. The loader
// then goes through go mod tidy, go build and go vet.
func TestEnvConfigModuleCompilesInTree(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	paths := testpaths.Local(t)
	names := naming.Default()
	outputRoot := t.TempDir()
	schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join("testdata", "services", "fixture-env-go"))
	if err != nil {
		t.Fatalf("load fixture-env-go: %v", err)
	}
	if _, err := Run(schema, cfg, Options{
		OutputRoot: outputRoot,
		Paths:      paths,
		Naming:     names,
		Clock:      codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	}); err != nil {
		t.Fatalf("run fixture-env-go: %v", err)
	}

	dir := APIDir(outputRoot, "fixture-env-go")
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	gomod := string(data)
	for module, want := range map[string]string{
		names.ScalarGoModule:                  paths.ScalarGo,
		names.SchemaIRGoModule:                paths.SchemaIR,
		names.GoTypesModule("fixture-env-go"): TypesDir(outputRoot, "go", "fixture-env-go"),
	} {
		replace := regexp.MustCompile(`(?m)^replace ` + regexp.QuoteMeta(module) + ` => (\S+)$`).FindStringSubmatch(gomod)
		if replace == nil {
			t.Errorf("%s/go.mod does not replace %s:\n%s", dir, module, gomod)
			continue
		}
		if got := filepath.Join(dir, filepath.FromSlash(replace[1])); got != want {
			t.Errorf("%s/go.mod replaces %s with %s, want %s", dir, module, got, want)
		}
	}

	for _, args := range [][]string{{"mod", "tidy"}, {"build", "./..."}, {"vet", "./..."}} {
		cmd := exec.Command("go", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
		}
	}
}
