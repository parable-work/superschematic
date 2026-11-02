package generator

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// A table that implements a behavior stays a table (D103), as one whose
// fields are behaviors does: the core's generators render it, and a
// generator that does not, an extension's, is refused before any generator
// runs, naming the generator, the table, the use and the behavior, for the
// database's own schema and for an API that imports the table.
func TestGeneratorsRefuseAnImplementedBehaviorTheyDoNotRender(t *testing.T) {
	db, err := loader.LoadService(filepath.Join("..", "loader", "tsreader", "testdata", "services", "fixture-timers-db"), loader.WithRegistry(CoreRegistry(naming.Default())))
	if err != nil {
		t.Fatal(err)
	}
	reg := CoreRegistry(naming.Default())
	var rendering []registry.GeneratorSpec
	for _, name := range []string{typesGenerator, "sql", ormGenerator, "api", "sdks", "envConfig", enginedocGenerator, projectengineGenerator} {
		spec, ok := reg.Generator(name)
		if !ok || !spec.RendersFieldBehaviors {
			t.Fatalf("generator %s: registered %v, renders a table's behaviors %v", name, ok, spec.RendersFieldBehaviors)
		}
		rendering = append(rendering, spec)
	}
	if err := refuseBehaviors(db, rendering); err != nil {
		t.Fatalf("refuseBehaviors with the generators that render a table's behaviors: %v", err)
	}
	extension := registry.GeneratorSpec{Name: "acme-docs"}
	want := "generator: acme-docs does not render behaviors yet: type Invoice implements behavior Timers (D103)"
	if err := refuseBehaviors(db, append(rendering, extension)); err == nil || err.Error() != want {
		t.Errorf("refuseBehaviors with an extension's generator: %v, want %q", err, want)
	}

	deps := map[string]*ir.Schema{"fixture-timers-db": db}
	load := func(name string) (*ir.Schema, error) {
		if dep := deps[name]; dep != nil {
			return dep, nil
		}
		return nil, errors.New("no such service")
	}
	schema := ir.NewSchema("tickets-api", ir.SchemaKindAPI)
	schema.Imports = []ir.Import{{Package: "@schemas/fixture-timers-db", Types: []string{"Ticket"}}}
	cfg := &schemaconfig.SchemaConfig{Name: "tickets-api", Kind: ir.SchemaKindAPI,
		Dependencies: []schemaconfig.ServiceDependency{{Name: "fixture-timers-db", Kind: ir.SchemaKindDB}}}
	r := run{registry.GenerateContext{Schema: schema, Config: cfg, Registry: reg, LoadDependency: load, Options: registry.Options{LoadDependency: load}}}
	want = "generator: acme-docs does not render behaviors yet: tickets-api imports fixture-timers-db.Ticket, which implements behavior Timers (D103)"
	if err := r.refuseImportedEngineTypes([]registry.GeneratorSpec{extension}); err == nil || err.Error() != want {
		t.Errorf("refuseImportedEngineTypes with an extension's generator: %v, want %q", err, want)
	}
}
