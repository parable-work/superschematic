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
	ir "github.com/parable-work/superschematic/ir"
)

// chainServices are built in dependency order: each imports a type from the
// one before (base <- common <- db), and fixture-chain-api names
// fixture-chain-db as its authDb and takes Address from fixture-chain-common.
var chainServices = []string{"fixture-chain-base", "fixture-chain-common", "fixture-chain-db", "fixture-chain-api"}

// TestGoModulesRequireTheirGeneratedClosure builds the fixture-chain
// services the way build-all does, with [paths] pointing at this checkout,
// and checks that every generated Go module requires and replaces each
// generated types module it reaches, not only the ones it imports: Go takes
// no replace directive from a dependency's go.mod, so a missing hop is
// fetched from the network and the local build fails. The ORM, API and SDK
// modules then go through go mod tidy, go build and go vet.
func TestGoModulesRequireTheirGeneratedClosure(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	paths := testpaths.Local(t)
	names := naming.Default()
	outputRoot := t.TempDir()
	clock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	loadDependency := func(name string) (*ir.Schema, error) {
		return loader.LoadService(filepath.Join(tsFixtures, name))
	}
	for _, service := range chainServices {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, service))
		if err != nil {
			t.Fatalf("load %s: %v", service, err)
		}
		if _, err := Run(schema, cfg, Options{
			OutputRoot:     outputRoot,
			Paths:          paths,
			Naming:         names,
			Clock:          clock,
			LoadDependency: loadDependency,
		}); err != nil {
			t.Fatalf("run %s: %v", service, err)
		}
	}

	modules := []struct {
		dir     string
		reaches []string
	}{
		{TypesDir(outputRoot, "go", "fixture-chain-db"), []string{"fixture-chain-base", "fixture-chain-common"}},
		{TypesDir(outputRoot, "go", "fixture-chain-api"), []string{"fixture-chain-base", "fixture-chain-common"}},
		{ORMDir(outputRoot, "fixture-chain-db"), []string{"fixture-chain-base", "fixture-chain-common", "fixture-chain-db"}},
		{APIDir(outputRoot, "fixture-chain-api"), []string{"fixture-chain-api", "fixture-chain-base", "fixture-chain-common", "fixture-chain-db"}},
		{SDKDir(outputRoot, "go", "fixture-chain-api"), []string{"fixture-chain-api", "fixture-chain-base", "fixture-chain-common"}},
	}
	for _, module := range modules {
		data, err := os.ReadFile(filepath.Join(module.dir, "go.mod"))
		if err != nil {
			t.Fatal(err)
		}
		gomod := string(data)
		for _, service := range module.reaches {
			typesModule := names.GoTypesModule(service)
			if !regexp.MustCompile(`(?m)^\s*(require\s+)?` + regexp.QuoteMeta(typesModule) + ` v`).MatchString(gomod) {
				t.Errorf("%s/go.mod does not require %s:\n%s", module.dir, typesModule, gomod)
			}
			replace := regexp.MustCompile(`(?m)^replace ` + regexp.QuoteMeta(typesModule) + ` => (\S+)$`).FindStringSubmatch(gomod)
			if replace == nil {
				t.Errorf("%s/go.mod does not replace %s:\n%s", module.dir, typesModule, gomod)
				continue
			}
			if got, want := filepath.Join(module.dir, filepath.FromSlash(replace[1])), TypesDir(outputRoot, "go", service); got != want {
				t.Errorf("%s/go.mod replaces %s with %s, want %s", module.dir, typesModule, got, want)
			}
		}
	}

	for _, dir := range []string{
		ORMDir(outputRoot, "fixture-chain-db"),
		APIDir(outputRoot, "fixture-chain-api"),
		SDKDir(outputRoot, "go", "fixture-chain-api"),
	} {
		for _, args := range [][]string{{"mod", "tidy"}, {"build", "./..."}, {"vet", "./..."}} {
			cmd := exec.Command("go", args...)
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("go %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
			}
		}
	}
}
