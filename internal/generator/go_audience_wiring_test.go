package generator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// TestGoAPIWiresTheAudienceQueue builds fixture-audience-queue-db with its
// Go types and fixture-audience-queue-api, as build-all does: the API
// package's wiring.go builds the queue of the @worker of the pages'
// @audience queue (D156, #734), whose messages the ORM's ResolveAudience
// resolves, so the Workers interface has no method for it, and the API
// package and the ORM compile.
func TestGoAPIWiresTheAudienceQueue(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the Go API's audience queue wiring in -short mode")
	}
	const db, api = "fixture-audience-queue-db", "fixture-audience-queue-api"
	repoRoot := testpaths.TempDir(t)
	outputRoot := filepath.Join(repoRoot, "schemas", "dist")
	schemas := map[string]*ir.Schema{}
	configs := map[string]*schemaconfig.SchemaConfig{}
	for _, name := range []string{db, api} {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, name))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		schemas[name], configs[name] = schema, cfg
	}
	for _, service := range []string{db, api} {
		if _, err := Run(schemas[service], configs[service], Options{
			OutputRoot:     outputRoot,
			Paths:          testpaths.Local(t),
			Naming:         naming.Default(),
			Clock:          codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
			RepositoryRoot: repoRoot,
			LoadDependency: func(name string) (*ir.Schema, error) {
				if schema, ok := schemas[name]; ok {
					return schema, nil
				}
				return nil, fmt.Errorf("no fixture %s", name)
			},
			DependencyConfig: func(name string) (*schemaconfig.SchemaConfig, bool) {
				cfg, ok := configs[name]
				return cfg, ok
			},
			LoadDependencyConfig: func(name string) (*schemaconfig.SchemaConfig, error) {
				if cfg, ok := configs[name]; ok {
					return cfg, nil
				}
				return nil, fmt.Errorf("no fixture %s", name)
			},
		}); err != nil {
			t.Fatalf("run %s: %v", service, err)
		}
	}
	apiDir := APIDir(outputRoot, api)
	wiring, err := os.ReadFile(filepath.Join(apiDir, "wiring.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"func ResolveAudienceQueue(deps Deps, db *orm.Database, wiring Wiring) (worker.Queue, error) {",
		"queue := db.AudienceResolvingQueue()",
		"db.ResolveAudience(ctx, message.ChildTable, message.ParentKey)",
	} {
		if !strings.Contains(string(wiring), want) {
			t.Errorf("wiring.go lacks %s:\n%s", want, wiring)
		}
	}
	interfaces, err := os.ReadFile(filepath.Join(apiDir, "interfaces.go"))
	if err == nil && strings.Contains(string(interfaces), "ResolveAudience(ctx") {
		t.Errorf("the Workers interface has a method for the audience queue's worker:\n%s", interfaces)
	}
	env := []string{"CGO_LDFLAGS=" + archiveLDFlags(t)}
	for _, args := range [][]string{{"mod", "tidy"}, {"vet", "."}} {
		goIn(t, apiDir, env, args...)
	}
}
