package generator

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// stockroomExtension registers a behavior and a kind whose pipeline is two
// generators: shelfList renders behaviors; labels does not unless
// labelsRenders is set, and labelsDisabled switches it off. ran records
// which generators ran.
type stockroomExtension struct {
	ran            *[]string
	labelsRenders  bool
	labelsDisabled bool
}

func (stockroomExtension) Name() string { return "stockroom" }

func (e stockroomExtension) Register(r *registry.Registry) error {
	record := func(name string) func(registry.GenerateContext) error {
		return func(registry.GenerateContext) error {
			*e.ran = append(*e.ran, name)
			return nil
		}
	}
	return errors.Join(
		r.RegisterKind(registry.KindSpec{Name: "Stockroom", Extension: "stockroom", StructRole: ir.RoleEmbeddedStruct, Pipeline: []string{"shelfList", "labels"}}),
		r.RegisterGenerator(registry.GeneratorSpec{Name: "shelfList", Extension: "stockroom", RendersBehaviors: true, Generate: record("shelfList")}),
		r.RegisterGenerator(registry.GeneratorSpec{
			Name: "labels", Extension: "stockroom", RendersBehaviors: e.labelsRenders, Generate: record("labels"),
			Enabled: func(registry.GenerateContext) (bool, string) { return !e.labelsDisabled, "labels" },
		}),
		r.RegisterBehavior(registry.BehaviorSpec{Extension: "stockroom", Declaration: json.RawMessage(`{"name":"stockroom.Counted"}`)}),
	)
}

func stockroomService(behaviors ...ir.BehaviorRef) (*ir.Schema, *schemaconfig.SchemaConfig) {
	schema := ir.NewSchema("stock", ir.SchemaKind("Stockroom"))
	schema.Types["Plain"] = &ir.TypeDef{Name: "Plain", Role: ir.RoleEmbeddedStruct}
	schema.Types["Shelf"] = &ir.TypeDef{Name: "Shelf", Role: ir.RoleEmbeddedStruct, Behaviors: behaviors}
	return schema, &schemaconfig.SchemaConfig{Name: "stock", Kind: ir.SchemaKind("Stockroom")}
}

// Run refuses a schema with behaviors before any generator runs when an
// enabled generator does not render them, and names it; a generator that
// declares RendersBehaviors runs, and a disabled one is not asked.
func TestRunRefusesBehaviorsAGeneratorDoesNotRender(t *testing.T) {
	counted := ir.BehaviorRef{Name: "stockroom.Counted"}

	var ran []string
	reg := extensionRegistry(t, stockroomExtension{ran: &ran})
	schema, cfg := stockroomService(counted)
	_, err := Run(schema, cfg, Options{OutputRoot: t.TempDir(), Registry: reg})
	if want := "generator: labels does not render behaviors yet: type Shelf composes behavior stockroom.Counted"; err == nil || err.Error() != want {
		t.Fatalf("Run: err = %v, want %q", err, want)
	}
	if len(ran) != 0 {
		t.Fatalf("generators ran before the refusal: %v", ran)
	}

	// The same pipeline on a schema without behaviors runs both.
	plain, plainCfg := stockroomService()
	if _, err := Run(plain, plainCfg, Options{OutputRoot: t.TempDir(), Registry: reg}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(ran, ",") != "shelfList,labels" {
		t.Fatalf("ran = %v", ran)
	}

	for _, ext := range []stockroomExtension{{labelsRenders: true}, {labelsDisabled: true}} {
		ran = nil
		ext.ran = &ran
		reg := extensionRegistry(t, ext)
		schema, cfg := stockroomService(counted)
		if _, err := Run(schema, cfg, Options{OutputRoot: t.TempDir(), Registry: reg}); err != nil {
			t.Fatalf("%+v: Run: %v", ext, err)
		}
		if ran[0] != "shelfList" {
			t.Fatalf("%+v: ran = %v", ext, ran)
		}
	}
}
