package cigen

import (
	"errors"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// TestProjectEngine: an engine database whose types compose the work
// queue's behavior, or one the registry does not hold, needs the project's
// engine archive; one whose types compose the core's alone, and one the
// build cannot load, does not (D58).
func TestProjectEngine(t *testing.T) {
	reg := registry.New(naming.Default())
	schemas := map[string]*ir.Schema{
		"core-db":  {Name: "core-db", Kind: ir.SchemaKindDB, Types: map[string]*ir.TypeDef{"Ticket": {Name: "Ticket", Behaviors: []ir.BehaviorRef{{Name: "Workflow"}, {Name: "Timers"}}}}},
		"queue-db": {Name: "queue-db", Kind: ir.SchemaKindDB, Types: map[string]*ir.TypeDef{"Job": {Name: "Job", Behaviors: []ir.BehaviorRef{{Name: "Workflow"}, {Name: "Lease"}}}}},
		"ext-db":   {Name: "ext-db", Kind: ir.SchemaKindDB, Types: map[string]*ir.TypeDef{"Product": {Name: "Product", Behaviors: []ir.BehaviorRef{{Name: "acme.Rating"}}}}},
	}
	c := registry.GenerateContext{Registry: reg, LoadDependency: func(name string) (*ir.Schema, error) {
		if s, ok := schemas[name]; ok {
			return s, nil
		}
		return nil, errors.New("no such service")
	}}
	for db, want := range map[string]bool{"core-db": false, "queue-db": true, "ext-db": true, "missing-db": false} {
		if got := projectEngine(c, db); got != want {
			t.Errorf("projectEngine(%s) = %t, want %t", db, got, want)
		}
	}
	if projectEngine(registry.GenerateContext{Registry: reg}, "queue-db") {
		t.Error("projectEngine with no loader found a project engine")
	}
}
