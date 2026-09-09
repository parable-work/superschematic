package ext

import (
	"encoding/json"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// Shelf is @shelf's argument and what the IR stores under
// FieldDef.Extensions["acme"].shelf.
type Shelf struct {
	Aisle int    `json:"aisle"`
	Bay   string `json:"bay,omitempty"`
}

// fieldExt is the acme slot on a field: the codec both the decorator and
// the generators go through. Adding a second decorator on fields means
// adding a second member here and nothing outside this package.
type fieldExt struct {
	Shelf *Shelf `json:"shelf,omitempty"`
}

// ShelfArgs is @shelf's argument schema. Both frontends validate every use
// against it before Apply runs, so Apply only sees shapes that decode into
// Shelf, and the data form carries it verbatim under
// FieldDef.extensions.acme.shelf in the composed JSON Schema.
var ShelfArgs = json.RawMessage(`{
	"type": "object",
	"required": ["aisle"],
	"additionalProperties": false,
	"properties": {
		"aisle": {"type": "integer", "minimum": 0},
		"bay": {"type": "string", "minLength": 1}
	}
}`)

// registerDecorator adds @shelf: a field decorator from @acme/schema that
// only Catalog schemas may use.
func registerDecorator(r *registry.Registry) error {
	return r.RegisterDecorator(registry.DecoratorSpec{
		Name:      "shelf",
		Extension: Name,
		Packages:  []string{Package},
		Target:    registry.TargetField,
		Kinds:     []string{Kind},
		Args:      ShelfArgs,
		Apply: func(n registry.Node, args []any, _ registry.Site) error {
			var s Shelf
			if err := registry.DecodeArgs(args, &s); err != nil {
				return err
			}
			return ir.UpdateExtension(n.Field, Name, func(f *fieldExt) { f.Shelf = &s })
		},
	})
}

// ShelfOf returns the shelf declared on fd, if any.
func ShelfOf(fd *ir.FieldDef) (*Shelf, bool, error) {
	f, ok, err := ir.GetExtension[fieldExt](fd, Name)
	if err != nil || !ok || f.Shelf == nil {
		return nil, false, err
	}
	return f.Shelf, true, nil
}
