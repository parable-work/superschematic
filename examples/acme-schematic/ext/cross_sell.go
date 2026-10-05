package ext

import (
	"encoding/json"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// CrossSell is @crossSell's argument: the type whose listing a type is
// offered beside. With is a class named as a value: the TypeScript schema
// writes the class (with: Product), and the IR and the data forms write
// {"class": "Product"}.
type CrossSell struct {
	With registry.ClassRef `json:"with"`
}

// typeExt is the acme slot on a type, the codec @crossSell and the catalog
// generator go through.
type typeExt struct {
	CrossSell *CrossSell `json:"crossSell,omitempty"`
}

// CrossSellArgs is @crossSell's argument schema. with is a class
// (registry.ClassRefSchema), so a name in a string fails in every form, and
// the loader fails a class the schema neither declares nor imports.
var CrossSellArgs = json.RawMessage(`{
	"type": "object",
	"required": ["with"],
	"additionalProperties": false,
	"properties": {"with": ` + string(registry.ClassRefSchema) + `}
}`)

// registerCrossSell adds @crossSell from @acme/schema, a type decorator of
// Catalog schemas whose argument names a class. Nothing in the core names
// it: the argument evaluator reads a class as it reads any other value.
func registerCrossSell(r *registry.Registry) error {
	return r.RegisterDecorator(registry.DecoratorSpec{
		Name:      "crossSell",
		Extension: Name,
		Packages:  []string{Package},
		Target:    registry.TargetType,
		Kinds:     []string{Kind},
		Args:      CrossSellArgs,
		Apply: func(n registry.Node, args []any, _ registry.Site) error {
			var c CrossSell
			if err := registry.DecodeArgs(args, &c); err != nil {
				return err
			}
			return ir.UpdateExtension(n.Type, Name, func(t *typeExt) { t.CrossSell = &c })
		},
	})
}

// CrossSellOf returns the name of the class td is offered beside, if any.
func CrossSellOf(td *ir.TypeDef) (string, bool, error) {
	t, ok, err := ir.GetExtension[typeExt](td, Name)
	if err != nil || !ok || t.CrossSell == nil {
		return "", false, err
	}
	return t.CrossSell.With.Class, true, nil
}
