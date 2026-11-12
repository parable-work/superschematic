package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// TestARustServerKeepsItsTableOperationsWithAWarning: a Rust server writes
// no handler for a @tableOperation yet (D106), so the build warns once for
// the API, naming each, and every one stays a method of the trait the
// project implements, where a Go build of the same API warns of none and
// keeps only the operation the project writes.
func TestARustServerKeepsItsTableOperationsWithAWarning(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated servers of fixture-tracker-api in -short mode")
	}
	paths := testpaths.Local(t)
	loadDependency := func(name string) (*ir.Schema, error) {
		return loader.LoadService(filepath.Join(tsFixtures, name))
	}
	build := func(language string) (*Result, string) {
		outputRoot := t.TempDir()
		var result *Result
		for _, service := range []string{"fixture-tracker-db", "fixture-tracker-api"} {
			schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, service))
			if err != nil {
				t.Fatalf("load %s: %v", service, err)
			}
			if service == "fixture-tracker-api" {
				cfg.Outputs["api"] = map[string]any{"enabled": true, "language": language}
			}
			result, err = Run(schema, cfg, Options{
				OutputRoot:     outputRoot,
				Paths:          paths,
				Naming:         naming.Default(),
				Clock:          codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
				LoadDependency: loadDependency,
			})
			if err != nil {
				t.Fatalf("run %s as %s: %v", service, language, err)
			}
		}
		return result, APIDir(outputRoot, "fixture-tracker-api")
	}

	result, dir := build(APILanguageRust)
	var warnings []string
	for _, warning := range result.Warnings {
		if strings.Contains(warning, "@tableOperation") {
			warnings = append(warnings, warning)
		}
	}
	if len(warnings) != 1 {
		t.Fatalf("a Rust build warned %d times of its table operations, want once: %q", len(warnings), warnings)
	}
	for _, want := range []string{
		"fixture-tracker-api: the Rust server writes no handler for a @tableOperation yet (D106): ",
		"Teams.listTeams, Teams.getTeam, Teams.createTeam, ",
		"Issues.updateIssue, Issues.deleteIssue, ",
		" stay methods of the implementation's interface, to be written by hand, with the same route and types",
	} {
		if !strings.Contains(warnings[0], want) {
			t.Errorf("the warning lacks %q:\n%s", want, warnings[0])
		}
	}
	if strings.Contains(warnings[0], "summarizeTeam") {
		t.Errorf("the warning names an operation that is no @tableOperation:\n%s", warnings[0])
	}
	interfaces, err := os.ReadFile(filepath.Join(dir, "src", "interfaces.rs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"fn list_issues(", "fn update_issue(", "fn delete_comment(", "fn summarize_team("} {
		if !strings.Contains(string(interfaces), method) {
			t.Errorf("the Rust trait lacks %s, a method the project writes", method)
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
