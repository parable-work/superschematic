package generator

import (
	"os"
	"os/exec"
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

// TestARustServerServesItsTableOperations: a Rust server serves an
// operation marked @tableOperation from its table's repository (D106,
// D116), as a Go server does, so the build warns of none, the trait the
// project implements keeps only the operation the project writes, and the
// crate's src/tables.rs holds a handler for every other. The crate builds
// and passes clippy over the database's Rust types and their
// repositories.
func TestARustServerServesItsTableOperations(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated servers of fixture-tracker-api in -short mode")
	}
	paths := testpaths.Local(t)
	loadDependency := func(name string) (*ir.Schema, error) {
		return loader.LoadService(filepath.Join(tsFixtures, name))
	}
	build := func(language string) (*Result, string) {
		outputRoot := testpaths.TempDir(t)
		var result *Result
		for _, service := range []string{"fixture-tracker-db", "fixture-tracker-api"} {
			schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, service))
			if err != nil {
				t.Fatalf("load %s: %v", service, err)
			}
			if service == "fixture-tracker-api" {
				cfg.Outputs["api"] = map[string]any{"enabled": true, "language": language}
			}
			// The Rust server's handlers run over the Rust types of both.
			if language == APILanguageRust {
				types, _ := cfg.Outputs["types"].(map[string]any)
				if types == nil {
					types = map[string]any{}
					cfg.Outputs["types"] = types
				}
				types["rust"] = map[string]any{"enabled": true}
			}
			result, err = Run(schema, cfg, Options{
				OutputRoot:     outputRoot,
				Paths:          paths,
				Naming:         naming.Default(),
				Clock:          codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
				LoadDependency: loadDependency,
				DependencyConfig: func(name string) (*schemaconfig.SchemaConfig, bool) {
					_, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, name))
					if err != nil {
						return nil, false
					}
					types, _ := cfg.Outputs["types"].(map[string]any)
					if types == nil {
						types = map[string]any{}
						cfg.Outputs["types"] = types
					}
					types["rust"] = map[string]any{"enabled": true}
					return cfg, true
				},
			})
			if err != nil {
				t.Fatalf("run %s as %s: %v", service, language, err)
			}
		}
		return result, APIDir(outputRoot, "fixture-tracker-api")
	}

	result, dir := build(APILanguageRust)
	for _, warning := range result.Warnings {
		if strings.Contains(warning, "@tableOperation") {
			t.Errorf("a Rust build warned of a table operation it serves: %s", warning)
		}
	}
	interfaces, err := os.ReadFile(filepath.Join(dir, "src", "interfaces.rs"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(interfaces), "fn summarize_team(") {
		t.Errorf("the Rust trait lacks summarize_team, the method the project writes")
	}
	for _, method := range []string{"fn list_issues(", "fn update_issue(", "fn delete_comment(", "fn create_team("} {
		if strings.Contains(string(interfaces), method) {
			t.Errorf("the Rust trait holds %s, an operation the router serves", method)
		}
	}
	tables, err := os.ReadFile(filepath.Join(dir, "src", "tables.rs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"pub(crate) async fn issues_list_issues(",
		"pub(crate) async fn notes_update_note(",
		`"by":["owner","sharedWith"],"overridePermission":"notes.admin"`,
		"Tables::new(db, database_types::orm::postgres_definition)",
	} {
		if !strings.Contains(string(tables), want) {
			t.Errorf("src/tables.rs lacks %q", want)
		}
	}
	if cargo, err := exec.LookPath("cargo"); err == nil {
		target := os.Getenv("CARGO_TARGET_DIR")
		if target == "" {
			target = filepath.Join(t.TempDir(), "target")
		}
		clippy := exec.Command(cargo, "clippy", "--quiet", "--", "-D", "warnings")
		clippy.Dir = dir
		clippy.Env = append(os.Environ(), "CARGO_TARGET_DIR="+target)
		if out, err := clippy.CombinedOutput(); err != nil {
			t.Fatalf("cargo clippy in %s: %v\n%s", dir, err, out)
		}
	}

	// The Go server serves every one of them, so it warns of none.
	result, dir = build(APILanguageGo)
	for _, warning := range result.Warnings {
		if strings.Contains(warning, "@tableOperation") {
			t.Errorf("a Go build warned of a table operation it serves: %s", warning)
		}
	}
	goInterfaces, err := os.ReadFile(filepath.Join(dir, "interfaces.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(goInterfaces), "SummarizeTeam(") || strings.Contains(string(goInterfaces), "UpdateIssue(") {
		t.Errorf("the Go interface holds other than the one operation the project writes:\n%s", goInterfaces)
	}
}
