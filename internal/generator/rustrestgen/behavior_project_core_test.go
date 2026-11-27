package rustrestgen

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/rulescore"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// The core a generated Rust server builds (D103's step 5): the stock
// core, superschematic_rules_behaviors::core, for a layout of the core's
// behaviors; the stock core with the declarative behaviors the layout
// declares (core_declaring); and the project's rules crate's core() when
// the database's tables compose a behavior a class declares with its
// crate, which the API crate depends on by path.

// votesDeclaration is test.Votes', a declarative behavior whose
// declaration states its rules (runtime/engine/testdata/rules/declarations).
func votesDeclaration(t *testing.T) json.RawMessage {
	t.Helper()
	data, err := os.ReadFile("../../../runtime/engine/testdata/rules/declarations/test.Votes.behavior.json")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// declaringLayout is fixture-workflow-db's layout with test.Votes among its
// declarations, as a layout of tables that compose it holds it.
func declaringLayout(t *testing.T, layout string) string {
	t.Helper()
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(layout), &doc); err != nil {
		t.Fatal(err)
	}
	declarations, err := json.Marshal([]json.RawMessage{votesDeclaration(t)})
	if err != nil {
		t.Fatal(err)
	}
	doc["declarations"] = declarations
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// TestRustServersBuildTheProjectsCore: src/behaviors.rs builds the stock
// core for a layout of the core's behaviors alone, the stock core with
// the layout's declarations when it holds some, and the project's rules
// crate's core, which Cargo.toml depends on, when the Deps name one, with
// the layout's declarations pushed onto it.
func TestRustServersBuildTheProjectsCore(t *testing.T) {
	_, db, reg := loadBehaviorFixture(t)
	layout := behaviorLayout(t, db, reg)
	for _, tc := range []struct {
		name     string
		deps     func() DepsInfo
		want     []string
		manifest []string
	}{
		{"the core's behaviors", func() DepsInfo { return behaviorDeps(layout) }, []string{"CORE.get_or_init(superschematic_rules_behaviors::core)"}, nil},
		{"declarations", func() DepsInfo { return behaviorDeps(declaringLayout(t, layout)) }, []string{
			"superschematic_rules_behaviors::core_declaring(&layout().declarations)",
			"and the declarative ones RULES_LAYOUT declares",
		}, nil},
		{"a project's rules crate", func() DepsInfo {
			deps := behaviorDeps(declaringLayout(t, layout))
			deps.Behaviors.ProjectCrate = "schemas-fixture-workflow-db-rules"
			deps.Behaviors.ProjectCratePath = "../../rules/rust/fixture-workflow-db"
			return deps
		}, []string{
			"let mut core = schemas_fixture_workflow_db_rules::core();",
			"superschematic_rules_behaviors::declaratives(&layout().declarations)",
			"core.push(behavior);",
			"schemas-fixture-workflow-db-rules, the project's rules crate, registers",
		}, []string{`schemas-fixture-workflow-db-rules = { path = "../../rules/rust/fixture-workflow-db" }`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := generateBehaviorAPI(t, db)
			if err := output.SetDeps(tc.deps()); err != nil {
				t.Fatalf("set deps: %v", err)
			}
			dir := t.TempDir()
			if err := WriteAPI(output, dir); err != nil {
				t.Fatal(err)
			}
			read := func(name string) string {
				data, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil {
					t.Fatal(err)
				}
				return string(data)
			}
			behaviors := read("src/behaviors.rs")
			for _, want := range tc.want {
				if !strings.Contains(behaviors, want) {
					t.Errorf("src/behaviors.rs lacks %q", want)
				}
			}
			manifest := read("Cargo.toml")
			for _, want := range tc.manifest {
				if !strings.Contains(manifest, want) {
					t.Errorf("Cargo.toml lacks %q:\n%s", want, manifest)
				}
			}
			if tc.manifest == nil && strings.Contains(manifest, "-rules = {") {
				t.Errorf("Cargo.toml depends on a project's rules crate:\n%s", manifest)
			}
		})
	}
}

// counterDeclaration is test.Counter's, a behavior a project's class
// declares with its crate (internal/generator/rulescore/testdata/counter).
const counterDeclaration = `{
  "name": "test.Counter",
  "description": "A count on the row that bump raises.",
  "fields": [{ "name": "count", "description": "How many bumps.", "type": "number" }],
  "operations": [{
    "name": "bump", "description": "Raises the count by one.",
    "paramsSchema": { "type": "object", "additionalProperties": false },
    "resultSchema": { "type": "object", "properties": { "count": { "type": "integer" } } },
    "writes": true
  }]
}`

// TestRustServersOfTheProjectsCoreBuild lints fixture-workflow-api's crate
// with clippy, warnings denied, twice: over a layout that declares
// test.Votes, a declarative behavior, which the stock core runs; and over
// the project's rules crate rulescore writes for a database whose table
// composes test.Counter, a behavior a class declares with its crate, which
// the API crate's core is, the layout's declarations pushed onto it.
func TestRustServersOfTheProjectsCoreBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cargo build in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust API build")
	}
	t.Run("declarations", func(t *testing.T) {
		crate := workflowFixture.buildWith(t, false, func(_, _ string, deps *DepsInfo) {
			deps.Behaviors.Layout = declaringLayout(t, deps.Behaviors.Layout)
		})
		cargoInBehaviorCrate(t, cargo, crate, "clippy", "--quiet", "--all-targets", "--", "-D", "warnings")
	})
	t.Run("a project's rules crate", func(t *testing.T) {
		crate := workflowFixture.buildWith(t, false, func(root, apiDir string, deps *DepsInfo) {
			reg := registry.New(naming.Default())
			if err := reg.RegisterBehavior(registry.BehaviorSpec{
				Extension:   "test",
				Rules:       registry.BehaviorRust{Crate: "test-counter-rules", Path: "counter", Register: "test_counter_rules::Counter"},
				Declaration: json.RawMessage(counterDeclaration),
			}); err != nil {
				t.Fatal(err)
			}
			testdata, err := filepath.Abs("../rulescore/testdata")
			if err != nil {
				t.Fatal(err)
			}
			behaviorsCrate, err := filepath.Abs("../../../runtime/engine/rust/rules-behaviors")
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(root, "rules", "rust", behaviorDB)
			schema := &ir.Schema{Name: behaviorDB, Kind: ir.SchemaKindDB, Types: map[string]*ir.TypeDef{
				"Tally": {Name: "Tally", Role: ir.RoleDBTable, Behaviors: []ir.BehaviorRef{{Name: "test.Counter", Implements: true}}},
			}}
			project, why, err := rulescore.Build(schema, rulescore.Options{
				SchemaName:     behaviorDB,
				Registry:       reg,
				Naming:         naming.Default(),
				RepositoryRoot: testdata,
				Paths:          naming.LocalPaths{RulesBehaviorsRust: behaviorsCrate},
				Dir:            dir,
				RustVersion:    "1.99.0",
			})
			if err != nil || project == nil {
				t.Fatalf("rulescore.Build = %v, %q, %v", project, why, err)
			}
			if err := project.Write(dir); err != nil {
				t.Fatal(err)
			}
			deps.Behaviors.Layout = declaringLayout(t, deps.Behaviors.Layout)
			deps.Behaviors.ProjectCrate = project.Name
			if deps.Behaviors.ProjectCratePath, err = naming.RelPath(apiDir, dir); err != nil {
				t.Fatal(err)
			}
		})
		cargoInBehaviorCrate(t, cargo, crate, "clippy", "--quiet", "--all-targets", "--", "-D", "warnings")
	})
}
