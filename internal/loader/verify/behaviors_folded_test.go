package verify

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// The behaviors D103 folds into schema features are refused in every kind
// and every form a type composes one in (on the type, by implements, on a
// field), each naming what replaces it, though no registry declares them
// since the engine went (D103, #587).
func TestFoldedBehaviorsAreRefused(t *testing.T) {
	core := registry.New(naming.Naming{})
	replacements := map[string]string{
		"Links":    "Relation<Target>",
		"Search":   "@searchField",
		"Queue":    "@queue",
		"Branches": "@versioned",
	}
	names := make([]string, 0, len(FoldedBehaviors))
	for name := range FoldedBehaviors {
		names = append(names, name)
	}
	slices.Sort(names)
	if want := []string{"Branches", "Links", "Queue", "Search"}; !slices.Equal(names, want) {
		t.Fatalf("FoldedBehaviors = %v, want %v", names, want)
	}
	configs := map[string]string{
		"Links":    `{"links":{"project":{"schema":"projects"}}}`,
		"Search":   `{"fields":["sku"]}`,
		"Queue":    `{"claim":{"from":["queued"],"to":"running"}}`,
		"Branches": `{"kinds":{"step":{"type":"Step"}}}`,
	}
	for _, name := range names {
		if _, ok := core.Behavior(name); ok {
			t.Errorf("the core registry registers %s, which went with the engine", name)
		}
		forms := map[string]func() *ir.Schema{
			"on a General type": func() *ir.Schema {
				return behaviorSchema(ir.BehaviorRef{Name: name, Config: json.RawMessage(configs[name])})
			},
			"on an API type": func() *ir.Schema {
				schema := behaviorSchema(ir.BehaviorRef{Name: name, Config: json.RawMessage(configs[name])})
				schema.Kind = ir.SchemaKindAPI
				return schema
			},
			"on a DB table": func() *ir.Schema {
				schema := behaviorSchema(ir.BehaviorRef{Name: name, Config: json.RawMessage(configs[name])})
				schema.Kind = ir.SchemaKindDB
				schema.Types["Item"].Role = ir.RoleDBTable
				return schema
			},
			"by implements": func() *ir.Schema {
				schema := behaviorSchema(ir.BehaviorRef{Name: name, Config: json.RawMessage(configs[name]), Implements: true})
				schema.Kind = ir.SchemaKindDB
				schema.Types["Item"].Role = ir.RoleDBTable
				return schema
			},
			"on a field": func() *ir.Schema {
				schema := behaviorSchema(ir.BehaviorRef{Name: name, Config: json.RawMessage(configs[name]), Field: "shelf"})
				schema.Kind = ir.SchemaKindDB
				schema.Types["Item"].Role = ir.RoleDBTable
				return schema
			},
		}
		for form, schema := range forms {
			got := errorStrings(CheckBehaviors(schema(), Input{Registry: core}))
			want := "src/item.schema.json: type Item composes " + name + ", which is no longer a behavior (D103): " + FoldedBehaviors[name]
			if !slices.Equal(got, []string{want}) {
				t.Errorf("%s %s: errors = %v, want [%s]", name, form, got, want)
			}
			if !strings.Contains(FoldedBehaviors[name], replacements[name]) {
				t.Errorf("%s's refusal does not name %s: %s", name, replacements[name], FoldedBehaviors[name])
			}
		}
	}

	// A type that composes two folds is told of each, once, and its other
	// behaviors are not checked against a config that may name a link.
	workflow := ir.BehaviorRef{Name: "Workflow", Config: json.RawMessage(`{"states":["queued","running"],"transitions":[{"from":"queued","to":"running"}]}`)}
	links := ir.BehaviorRef{Name: "Links", Config: json.RawMessage(configs["Links"])}
	queue := ir.BehaviorRef{Name: "Queue", Config: json.RawMessage(configs["Queue"])}
	numbering := ir.BehaviorRef{Name: "Numbering", Config: json.RawMessage(`{"format":"N-{seq}","scope":{"link":"project"}}`)}
	got := errorStrings(Run(behaviorSchema(workflow, links, queue, numbering), Input{Registry: core}))
	if len(got) != 2 || !strings.Contains(got[0], "composes Links") || !strings.Contains(got[1], "composes Queue") {
		t.Errorf("Links and Queue on one type: errors = %v, want one for each", got)
	}
	if !IsFoldedBehavior("Links") || IsFoldedBehavior("Lease") {
		t.Error("IsFoldedBehavior does not tell Links, folded, from Lease, a behavior")
	}
}
