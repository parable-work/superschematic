// Package loader is the public face of psgen's frontend: it loads one
// service directory (TypeScript or data form) into an ir.Schema through a
// registry the caller assembled. Extension modules use it to test their
// kinds and decorators against real fixtures; the engine keeps the
// implementation in internal/loader. Every identifier here is an alias or a
// forwarding function, so the two packages cannot drift.
//
// Design: docs/extension-model.md sections 6 and 10.
package loader

import (
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// Option configures LoadService.
type Option = loader.Option

// LoadService loads the service at servicePath (its schema.config.ts or
// schema.config.json plus the schema files it names) into an assembled
// schema; see internal/loader.LoadService.
func LoadService(servicePath string, opts ...Option) (*ir.Schema, error) {
	return loader.LoadService(servicePath, opts...)
}

// LoadServiceWithConfig is LoadService plus the schema.config it read; see
// internal/loader.LoadServiceWithConfig.
func LoadServiceWithConfig(servicePath string, opts ...Option) (*ir.Schema, *registry.SchemaConfig, error) {
	return loader.LoadServiceWithConfig(servicePath, opts...)
}

// WithRegistry names the registry the load dispatches kinds, decorators and
// documents through. Without it the loader uses a core-only registry.
func WithRegistry(reg *registry.Registry) Option { return loader.WithRegistry(reg) }

// WithNaming overrides the naming the load resolves authoring packages with.
func WithNaming(n registry.Naming) Option { return loader.WithNaming(n) }

// WithSchemaCatalog supplies the discovered schema set that document loaders
// resolve cross-service references against (LoadContext.Catalog). Without it
// the resolution check is skipped, the single-service case; build-all always
// supplies it.
func WithSchemaCatalog(catalog map[string]registry.SchemaCatalogEntry) Option {
	return loader.WithSchemaCatalog(catalog)
}
