package ext

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/parable-work/superschematic/registry"
)

// DocumentName keys Schema.Documents and the `documents` section of a
// data-form schema file.
const DocumentName = "catalog.config"

// DocumentFile is the sidecar the loader looks for next to schema.config.*.
const DocumentFile = "catalog.config.yaml"

// CatalogConfig is the decoded sidecar.
type CatalogConfig struct {
	Region   string `json:"region"`
	Currency string `json:"currency"`
	Aisles   int    `json:"aisles,omitempty"`
}

// DocumentSchema is the JSON Schema of catalog.config.yaml. The loader
// validates the file against it before the document reaches the IR.
var DocumentSchema = json.RawMessage(`{
	"type": "object",
	"required": ["region", "currency"],
	"additionalProperties": false,
	"properties": {
		"region": {"type": "string", "minLength": 1},
		"currency": {"type": "string", "pattern": "^[A-Z]{3}$"},
		"aisles": {"type": "integer", "minimum": 1}
	}
}`)

// registerDocument adds the catalog.config sidecar. Only Catalog schemas may
// carry it; the file next to a DB schema is a load error.
func registerDocument(r *registry.Registry) error {
	return r.RegisterDocument(registry.DocumentSpec{
		Name:      DocumentName,
		Extension: Name,
		File:      DocumentFile,
		Kinds:     []string{Kind},
		Schema:    DocumentSchema,
		Loader: func(_ context.Context, lc registry.LoadContext) (json.RawMessage, []string, error) {
			doc, err := lc.DecodeData(DocumentFile, DocumentSchema)
			return doc, nil, err
		},
		Dirs: func(c registry.GenerateContext) []string {
			return []string{CatalogDir(c.Options.OutputRoot, c.Config.Name)}
		},
		Generate: generateCatalogConfig,
	})
}

// generateCatalogConfig writes config.json next to catalog.json, with the
// aisle count checked against the shelves the schema declares. The check is
// what a static schema cannot express: it needs both the document and the
// IR.
func generateCatalogConfig(c registry.GenerateContext, raw json.RawMessage) error {
	var doc CatalogConfig
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("%s: %w", DocumentFile, err)
	}
	if doc.Aisles > 0 {
		for _, tname := range sortedTypeNames(c.Schema) {
			for _, fd := range c.Schema.Types[tname].Fields {
				shelf, ok, err := ShelfOf(fd)
				if err != nil {
					return err
				}
				if ok && shelf.Aisle >= doc.Aisles {
					return fmt.Errorf("%s: %s.%s is on aisle %d but the catalog has %d aisles", DocumentFile, tname, fd.Name, shelf.Aisle, doc.Aisles)
				}
			}
		}
	}
	dir := CatalogDir(c.Options.OutputRoot, c.Config.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), append(b, '\n'), 0o644); err != nil {
		return err
	}
	c.Done(DocumentName, dir)
	return nil
}
