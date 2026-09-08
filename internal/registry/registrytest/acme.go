// Package registrytest holds the fixture extension the psgen tests register
// against: "acme", modelled on the acme-schematic worked example in
// docs/extension-model.md section 10. It exercises every registration seam
// an extension has (a kind, decorators on every target, a document, a
// generator) without touching a core file, which is the property the
// extension model promises.
package registrytest

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// Package is the npm authoring package the fixture's TypeScript decorators
// are imported from.
const Package = "@acme/schematic"

// Acme is the fixture extension.
type Acme struct{}

// Name is the extension name: the key under every "extensions" slot.
func (Acme) Name() string { return "acme" }

// Shelf is @shelf's argument.
type Shelf struct {
	Aisle int    `json:"aisle"`
	Bay   string `json:"bay,omitempty"`
}

// FieldExt is what acme stores on a field.
type FieldExt struct {
	Shelf *Shelf `json:"shelf,omitempty"`
}

// TypeExt is what acme stores on a type.
type TypeExt struct {
	Tagged bool           `json:"tagged,omitempty"`
	Meta   map[string]any `json:"meta,omitempty"`
}

// OpExt is what acme stores on an operation set or an operation.
type OpExt struct {
	Audited bool `json:"audited,omitempty"`
}

// CatalogConfig is the fixture's sidecar document.
type CatalogConfig struct {
	Shelves int      `json:"shelves,omitempty"`
	Names   []string `json:"names,omitempty"`
}

// ShelfArgs is @shelf's argument schema; the JSON Schema composition
// carries it verbatim under FieldDef.extensions.acme.shelf.
var ShelfArgs = json.RawMessage(`{"type":"object","required":["aisle"],"properties":{"aisle":{"type":"integer","minimum":0},"bay":{"type":"string"}},"additionalProperties":false}`)

// OutDir is where the catalog generator writes for a service.
func OutDir(outputRoot, service string) string {
	return filepath.Join(outputRoot, "catalog", service)
}

// Register adds the Catalog kind, the four decorators, the catalogConfig
// document and the catalog generator.
func (Acme) Register(r *registry.Registry) error {
	pkgs := []string{Package}
	return errors.Join(
		r.RegisterKind(registry.KindSpec{
			Name:       "Catalog",
			Extension:  "acme",
			StructRole: ir.RoleEmbeddedStruct,
			Pipeline:   []string{"catalog"},
		}),
		r.RegisterDecorator(registry.DecoratorSpec{
			Name: "shelf", Extension: "acme", Packages: pkgs, Target: registry.TargetField,
			Kinds: []string{"Catalog"}, Args: ShelfArgs,
			Apply: func(n registry.Node, args []any, _ registry.Site) error {
				var s Shelf
				if err := registry.DecodeArgs(args, &s); err != nil {
					return err
				}
				return ir.UpdateExtension(n.Field, "acme", func(f *FieldExt) { f.Shelf = &s })
			},
		}),
		r.RegisterDecorator(registry.DecoratorSpec{
			Name: "tagged", Extension: "acme", Packages: pkgs, Target: registry.TargetType,
			Apply: func(n registry.Node, _ []any, _ registry.Site) error {
				return ir.UpdateExtension(n.Type, "acme", func(t *TypeExt) { t.Tagged = true })
			},
		}),
		r.RegisterDecorator(registry.DecoratorSpec{
			Name: "meta", Extension: "acme", Packages: pkgs, Target: registry.TargetType,
			Args: json.RawMessage(`{"type":"object"}`),
			Apply: func(n registry.Node, args []any, _ registry.Site) error {
				var m map[string]any
				if err := registry.DecodeArgs(args, &m); err != nil {
					return err
				}
				return ir.UpdateExtension(n.Type, "acme", func(t *TypeExt) { t.Meta = m })
			},
		}),
		r.RegisterDecorator(registry.DecoratorSpec{
			Name: "audited", Extension: "acme", Packages: pkgs, Target: registry.TargetOperationSet,
			Apply: func(n registry.Node, _ []any, _ registry.Site) error {
				return ir.UpdateExtension(n.OperationSet, "acme", func(o *OpExt) { o.Audited = true })
			},
		}),
		r.RegisterDecorator(registry.DecoratorSpec{
			Name: "audited", Extension: "acme", Packages: pkgs, Target: registry.TargetOperation,
			Apply: func(n registry.Node, _ []any, _ registry.Site) error {
				return ir.UpdateExtension(n.Field, "acme", func(o *OpExt) { o.Audited = true })
			},
		}),
		r.RegisterDocument(registry.DocumentSpec{
			Name: "catalogConfig", Extension: "acme",
			Schema: json.RawMessage(`{"type":"object","properties":{"shelves":{"type":"integer"},"names":{"type":"array","items":{"type":"string"}}},"additionalProperties":false}`),
		}),
		r.RegisterGenerator(registry.GeneratorSpec{
			Name: "catalog", Extension: "acme", Kinds: []string{"Catalog"}, OutputKey: "catalog",
			OutputSchema: json.RawMessage(`{"type":"object","properties":{"enabled":{"type":"boolean"}},"additionalProperties":false}`),
			Dirs: func(c registry.GenerateContext) []string {
				return []string{OutDir(c.Options.OutputRoot, c.Config.Name)}
			},
			Enabled: func(c registry.GenerateContext) (bool, string) {
				var o struct {
					Enabled bool `json:"enabled"`
				}
				_ = registry.DecodeOutput(c.Outputs, "catalog", &o)
				if !o.Enabled {
					return false, "catalog: outputs.catalog.enabled is false"
				}
				return true, ""
			},
			Generate: generateCatalog,
		}),
	)
}

// Catalog is the generator's output file shape.
type Catalog struct {
	Service string           `json:"service"`
	Shelves map[string]Shelf `json:"shelves"`
	Tagged  []string         `json:"tagged,omitempty"`
}

func generateCatalog(c registry.GenerateContext) error {
	out := Catalog{Service: c.Config.Name, Shelves: map[string]Shelf{}}
	for tname, td := range c.Schema.Types {
		if t, ok, err := ir.GetExtension[TypeExt](td, "acme"); err != nil {
			return fmt.Errorf("type %s: %w", tname, err)
		} else if ok && t.Tagged {
			out.Tagged = append(out.Tagged, tname)
		}
		for _, fd := range td.Fields {
			f, ok, err := ir.GetExtension[FieldExt](fd, "acme")
			if err != nil {
				return fmt.Errorf("type %s field %s: %w", tname, fd.Name, err)
			}
			if ok && f.Shelf != nil {
				out.Shelves[tname+"."+fd.Name] = *f.Shelf
			}
		}
	}
	sort.Strings(out.Tagged)
	dir := OutDir(c.Options.OutputRoot, c.Config.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "catalog.json")
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		return err
	}
	c.Result.Outputs["catalog"] = dir
	return nil
}
