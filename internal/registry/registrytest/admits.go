package registrytest

import (
	"encoding/json"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// Admits is a second fixture extension, for the tests of schema references
// (D41): one decorator, @admits({ from: [...] }) from Acme's authoring
// package, whose `from` holds service handles as identities
// (DecoratorSpec.Identities), as D37's `from` does. A handle in Acme's
// @meta, which takes any object, is a reference.
type Admits struct{}

// Name is the extension name and the key of its slot.
func (Admits) Name() string { return "admits" }

// AdmitsExt is the extension's slot on a type: @admits's argument.
type AdmitsExt struct {
	Admits map[string]any `json:"admits,omitempty"`
}

// Register adds @admits.
func (Admits) Register(r *registry.Registry) error {
	return r.RegisterDecorator(registry.DecoratorSpec{
		Name: "admits", Extension: "admits", Packages: []string{Package}, Target: registry.TargetType,
		Args:       json.RawMessage(`{"type":"object","required":["from"],"properties":{"from":{"type":"array"}}}`),
		Identities: []string{"from"},
		Apply: func(n registry.Node, args []any, _ registry.Site) error {
			var a map[string]any
			if err := registry.DecodeArgs(args, &a); err != nil {
				return err
			}
			return ir.UpdateExtension(n.Type, "admits", func(t *AdmitsExt) { t.Admits = a })
		},
	})
}
