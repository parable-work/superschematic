package behaviortypes

import (
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// Each core narrowing types only fields its behavior declares, so a field a
// declaration renames fails here, whether or not a golden composes the
// behavior. Variants is left out: its config names the type's own fields,
// and it declares none.
func TestCoreNarrowingsTypeDeclaredFields(t *testing.T) {
	reg := registry.New(naming.Default())
	document := ir.NewSchema("items", ir.SchemaKindGeneral)
	target := Target{Schema: "items", Base: "Item", Document: document, Instance: &ir.TypeDef{Name: "Item"}}
	for _, name := range NarrowerNames() {
		if name == "Variants" {
			continue
		}
		declaration, ok := reg.Behavior(name)
		if !ok {
			t.Errorf("%s has a narrowing and no declaration", name)
			continue
		}
		if _, err := Narrow(declaration, nil, target); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
