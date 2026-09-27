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

// fieldExt is the acme slot on a field: the codec both the decorators and
// the generators go through. Each field decorator is one member, so adding
// another means a member here and nothing outside this package.
type fieldExt struct {
	Shelf   *Shelf `json:"shelf,omitempty"`
	FeedKey bool   `json:"feedKey,omitempty"`
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

// registerDecorator adds acme's field decorators from @acme/schema. Each
// is only allowed in Catalog schemas.
func registerDecorator(r *registry.Registry) error {
	if err := r.RegisterDecorator(registry.DecoratorSpec{
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
	}); err != nil {
		return err
	}
	// @feedKey marks a field as part of the key acme's supplier feed matches
	// incoming rows on; several on one type form a composite key. It is the
	// shape of a distribution's per-field directive: a flag the core IR has
	// no field for, stored in the extension's slot. With no Args it takes no
	// argument: TypeScript writes it bare and the data forms as
	// "feedKey": true.
	return r.RegisterDecorator(registry.DecoratorSpec{
		Name:      "feedKey",
		Extension: Name,
		Packages:  []string{Package},
		Target:    registry.TargetField,
		Kinds:     []string{Kind},
		Apply: func(n registry.Node, _ []any, _ registry.Site) error {
			return ir.UpdateExtension(n.Field, Name, func(f *fieldExt) { f.FeedKey = true })
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

// IsFeedKey reports whether fd carries @feedKey.
func IsFeedKey(fd *ir.FieldDef) (bool, error) {
	f, ok, err := ir.GetExtension[fieldExt](fd, Name)
	if err != nil || !ok {
		return false, err
	}
	return f.FeedKey, nil
}
