package ext

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// Kind is the schema kind the extension adds. A Catalog schema declares the
// products a shop stocks and where; its classes are embedded structs.
const Kind = "Catalog"

// CatalogOutputSchema is the JSON Schema of outputs.catalog in
// schema.config. ParseOutputs validates the block against it.
var CatalogOutputSchema = json.RawMessage(`{
	"type": "object",
	"additionalProperties": false,
	"properties": {"enabled": {"type": "boolean"}}
}`)

// registerKind adds the Catalog kind and the catalog generator its pipeline
// names. The pipeline reuses the core "types" generator: a Catalog schema
// gets TypeScript, Go, Python or Rust types like any other, then the
// catalog file.
func registerKind(r *registry.Registry) error {
	if err := r.RegisterKind(registry.KindSpec{
		Name:       Kind,
		Extension:  Name,
		StructRole: ir.RoleEmbeddedStruct,
		Pipeline:   []string{"types", "catalog"},
		// A Catalog may reference General types (a shared Money type, say)
		// but not tables or API projections.
		AllowedReferences: map[string]bool{"General": true},
	}); err != nil {
		return err
	}
	return r.RegisterGenerator(registry.GeneratorSpec{
		Name:         "catalog",
		Extension:    Name,
		Kinds:        []string{Kind},
		OutputKey:    "catalog",
		OutputSchema: CatalogOutputSchema,
		Dirs: func(c registry.GenerateContext) []string {
			return []string{CatalogDir(c.Options.OutputRoot, c.Config.Name)}
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
	})
}

// CatalogDir is where the catalog generator writes for a service.
func CatalogDir(outputRoot, service string) string {
	return filepath.Join(outputRoot, "acme", "catalog", service)
}

// Catalog is the generator's output: every shelved field of the service.
type Catalog struct {
	Service string           `json:"service"`
	Shelves map[string]Shelf `json:"shelves"`
}

// generateCatalog writes catalog.json: one entry per @shelf field, keyed
// Type.field.
func generateCatalog(c registry.GenerateContext) error {
	out := Catalog{Service: c.Config.Name, Shelves: map[string]Shelf{}}
	for _, tname := range sortedTypeNames(c.Schema) {
		for _, fd := range c.Schema.Types[tname].Fields {
			shelf, ok, err := ShelfOf(fd)
			if err != nil {
				return fmt.Errorf("type %s field %s: %w", tname, fd.Name, err)
			}
			if ok {
				out.Shelves[tname+"."+fd.Name] = *shelf
			}
		}
	}
	dir := CatalogDir(c.Options.OutputRoot, c.Config.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "catalog.json"), append(b, '\n'), 0o644); err != nil {
		return err
	}
	c.Done("catalog", dir)
	return nil
}

func sortedTypeNames(schema *ir.Schema) []string {
	names := make([]string, 0, len(schema.Types))
	for name := range schema.Types {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
