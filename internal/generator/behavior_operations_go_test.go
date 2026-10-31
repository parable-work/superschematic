package generator

import (
	"errors"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// A Go API whose database has a table whose fields are behaviors builds
// (D103): its Deps hold that database's ORM, which requires the rules
// module, so the API's go.mod requires and replaces the rules module too,
// with [paths] rules_go in a checkout, or pins it to the release that
// generates it. An API over a database with no such table reaches no rules
// module.
func TestAGoAPIOverATableWhoseFieldsAreBehaviorsRequiresTheRulesModule(t *testing.T) {
	db := fieldBehaviorsDB(t)
	plain := ir.NewSchema("plain-db", ir.SchemaKindDB)
	load := func(name string) (*ir.Schema, error) {
		switch name {
		case "fixture-workflow-fields-db":
			return db, nil
		case "plain-db":
			return plain, nil
		}
		return nil, errors.New("no such service")
	}
	reg := CoreRegistry(naming.Default())
	rulesModule := naming.Default().RulesGoModule
	for dbName, rules := range map[string]bool{"fixture-workflow-fields-db": true, "plain-db": false} {
		api := ir.NewSchema("desk-api", ir.SchemaKindAPI)
		api.AuthDB = dbName
		cfg := &schemaconfig.SchemaConfig{Name: "desk-api", Kind: ir.SchemaKindAPI}
		r, _ := newRun(api, cfg, &registry.Outputs{}, Options{OutputRoot: t.TempDir(), Registry: reg, LoadDependency: load}, reg)
		output := &apigen.APIOutput{SchemaName: "desk-api", Naming: naming.Default()}
		deps, _, err := r.goDeps(output)
		if err != nil {
			t.Fatalf("%s: goDeps: %v", dbName, err)
		}
		if deps.Database != dbName || deps.Rules != rules {
			t.Fatalf("%s: Deps = %+v, want its ORM, Rules %t", dbName, deps, rules)
		}
		if err := output.SetDeps(deps); err != nil {
			t.Fatal(err)
		}

		// In a checkout the rules module is replaced by [paths] rules_go,
		// relative to the API module.
		paths := naming.LocalPaths{RulesGo: "/repo/runtime/rules/go"}
		if err := apigen.SetReplacePaths(output, paths, "/repo/schemas/dist/api/desk-api"); err != nil {
			t.Fatal(err)
		}
		want := ""
		if rules {
			want = "../../../../runtime/rules/go"
		}
		if output.RulesReplacePath != want {
			t.Errorf("%s: the rules module's replace path = %q, want %q", dbName, output.RulesReplacePath, want)
		}

		// From a release it is pinned to the release's version.
		apigen.SetReleasePins(output, naming.Pins{rulesModule: "v1.2.3"})
		pinned := output.Pins.Version(rulesModule, "") == "v1.2.3"
		if pinned != rules {
			t.Errorf("%s: the rules module pinned = %t, want %t", dbName, pinned, rules)
		}
	}
}
