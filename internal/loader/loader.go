// Package loader is the format-dispatching service loader: it reads one
// schema service directory (schema.config.{ts,json,yaml} plus
// src/*.schema.{ts,json,yaml}) through the per-format frontends, merges the
// per-file results into a single Schema IR, and runs the format-agnostic
// validation pass on the assembled schema.
package loader

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader/jsonreader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/loader/schemafile"
	"github.com/parable-work/superschematic/internal/loader/tsreader"
	"github.com/parable-work/superschematic/internal/loader/verify"
	"github.com/parable-work/superschematic/internal/loader/yamlreader"
	"github.com/parable-work/superschematic/internal/profile"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// WarningWriter receives the verification pass's warning diagnostics
// (errors fail the load instead). Overridable for tests.
var WarningWriter io.Writer = os.Stderr

// languagePrimitiveNames are the host-language primitives usable directly as
// field types; they resolve without a scalar or type definition.
var languagePrimitiveNames = []string{"string", "number", "boolean"}

// LoadService loads one service directory and returns the v2 Schema IR.
//
// Format dispatch is by file extension: *.schema.ts files go through the
// TypeScript frontend (one Corsa program per service), *.schema.json through
// the JSON reader, and *.schema.yaml / *.schema.yml through the YAML reader.
// One format per service is the convention, but formats can coexist; JSON
// and YAML definitions can reference TypeScript-defined types within the
// service, not the other way around (the TypeScript compiler cannot see
// data-form definitions).
func LoadService(servicePath string, opts ...Option) (*ir.Schema, error) {
	schema, _, err := LoadServiceWithConfig(servicePath, opts...)
	return schema, err
}

// Option adjusts one load. Options carry environment context the service
// directory itself cannot know (e.g. the discovered schema catalog).
type Option func(*loadOptions)

type loadOptions struct {
	profile        *profile.Profiler
	tsProgramCache *tsreader.ProgramCache
	schemaCatalog  map[string]registry.SchemaCatalogEntry
	naming         naming.Naming
	registry       *registry.Registry
}

// WithNaming supplies the superschematic.toml naming; the verification pass
// uses its npm scope to tell service imports from third-party ones. Without
// the option the defaults apply.
func WithNaming(n naming.Naming) Option {
	return func(o *loadOptions) {
		o.naming = n
	}
}

// WithRegistry supplies the registry the frontends consult: the schema kinds
// and their rules, the decorators (core and extension), the extension and
// document names the data forms may carry. The CLI passes the one registry
// it also hands the generators. Without the option the loader builds a core
// registry from the naming: today's kinds and decorators, nothing more.
func WithRegistry(reg *registry.Registry) Option {
	return func(o *loadOptions) {
		o.registry = reg
	}
}

// registry returns the configured registry or the core fallback.
func (o *loadOptions) registryOrCore() *registry.Registry {
	if o.registry == nil {
		o.registry = registry.New(o.naming.OrDefault())
	}
	return o.registry
}

// WithProfiler enables phase timing for a load.
func WithProfiler(prof *profile.Profiler) Option {
	return func(o *loadOptions) {
		o.profile = prof
	}
}

// WithSchemaCatalog supplies the discovered schema set (name -> kind/authDb)
// so entity schema references in deploy documents resolve against real
// schemas (EDR-0087 amendment 2). Identity resolution only -- the catalog
// adds no build-order edges. Without the option the resolution check is
// skipped (single-schema builds); build-all always supplies it, so CI
// enforces.
func WithSchemaCatalog(catalog map[string]registry.SchemaCatalogEntry) Option {
	return func(o *loadOptions) {
		o.schemaCatalog = catalog
	}
}

// WithTSProgramCache enables the build-all shared TypeScript program.
func WithTSProgramCache(cache *tsreader.ProgramCache) Option {
	return func(o *loadOptions) {
		o.tsProgramCache = cache
	}
}

// LoadServiceWithConfig is LoadService plus the service configuration; the
// generators need the config's outputs block and service metadata.
func LoadServiceWithConfig(servicePath string, opts ...Option) (*ir.Schema, *schemaconfig.SchemaConfig, error) {
	var o loadOptions
	for _, opt := range opts {
		opt(&o)
	}
	reg := o.registryOrCore()
	if info, err := os.Stat(servicePath); err != nil || !info.IsDir() {
		return nil, nil, fmt.Errorf("service directory not found: %s", servicePath)
	}

	var dataFiles, tsFiles []string
	if err := o.profile.Measure("loader.classify", func() error {
		var err error
		dataFiles, tsFiles, err = classifySchemaFiles(servicePath)
		return err
	}); err != nil {
		return nil, nil, err
	}
	_, tsConfigErr := os.Stat(filepath.Join(servicePath, "schema.config.ts"))
	hasTSFrontend := len(tsFiles) > 0 || tsConfigErr == nil

	var schema *ir.Schema
	var cfg *schemaconfig.SchemaConfig
	var vin verify.Input
	if hasTSFrontend {
		// The TypeScript frontend owns its program, config read, walk, and
		// validation; cross-service imports land in schema.Imports.
		var tsInput *verify.Input
		if err := o.profile.Measure("loader.tsreader", func() error {
			var err error
			schema, cfg, tsInput, err = tsreader.LoadServiceWithConfig(
				servicePath,
				tsreader.WithProfiler(o.profile),
				tsreader.WithProgramCache(o.tsProgramCache),
				tsreader.WithRegistry(reg),
			)
			return err
		}); err != nil {
			return nil, nil, err
		}
		vin = *tsInput
		vin.Naming = o.naming
		vin.Registry = reg

		if len(dataFiles) == 0 {
			if err := hydrateScalarsFromRegistry(schema, reg.Scalars()); err != nil {
				return nil, nil, err
			}
			if err := o.profile.Measure("loader.composite-defaults", func() error {
				return loadCompositeDefaults(servicePath, schema, vin.ExternalEnums)
			}); err != nil {
				return nil, nil, err
			}
			if err := loadDocuments(servicePath, schema, cfg, &o, reg); err != nil {
				return nil, nil, err
			}
			var verified *ir.Schema
			err := o.profile.Measure("loader.verify", func() error {
				var err error
				verified, err = runVerify(schema, vin)
				return err
			})
			return verified, cfg, err
		}
	} else {
		if err := o.profile.Measure("loader.read-config", func() error {
			var err error
			cfg, err = schemaconfig.ReadFile(servicePath, reg)
			return err
		}); err != nil {
			return nil, nil, err
		}
		schema = ir.NewSchema(cfg.Name, cfg.Kind)
		vin.Dependencies = make(map[string]ir.SchemaKind, len(cfg.Dependencies))
		for _, dep := range cfg.Dependencies {
			vin.Dependencies[dep.Name] = dep.Kind
		}
		vin.Naming = o.naming
		vin.Registry = reg
	}

	var errs []error
	if err := o.profile.Measure("loader.data-merge", func() error {
		for _, rel := range dataFiles {
			abs := filepath.Join(servicePath, filepath.FromSlash(rel))
			var doc *schemafile.Document
			var err error
			if strings.HasSuffix(rel, ".schema.json") {
				doc, err = jsonreader.ReadFileWith(abs, rel, reg)
			} else {
				doc, err = yamlreader.ReadFileWith(abs, rel, reg)
			}
			if err != nil {
				errs = append(errs, err)
				continue
			}
			// The data formats carry no per-statement locations; import sites
			// anchor at the declaring file.
			for _, imp := range doc.Imports {
				vin.ImportSites = append(vin.ImportSites, verify.ImportSite{Package: imp.Package, File: rel})
			}
			errs = append(errs, schemafile.MergeWith(doc, schema, rel, reg)...)
		}
		return nil
	}); err != nil {
		return nil, nil, err
	}
	if len(errs) > 0 {
		return nil, nil, errors.Join(errs...)
	}
	if err := hydrateScalarsFromRegistry(schema, reg.Scalars()); err != nil {
		return nil, nil, err
	}
	if err := o.profile.Measure("loader.composite-defaults", func() error {
		return loadCompositeDefaults(servicePath, schema, vin.ExternalEnums)
	}); err != nil {
		return nil, nil, err
	}

	// The data formats have no compiler-resolved import system: symbols
	// named by the imports blocks are the known externals for validation.
	externals := schemafile.ImportedTypeNames(schema)
	for _, name := range languagePrimitiveNames {
		externals[name] = true
	}
	if err := o.profile.Measure("loader.validate-ir", func() error {
		if validationErrs := schema.Validate(ir.WithKnownExternals(externals)); len(validationErrs) > 0 {
			for _, e := range validationErrs {
				errs = append(errs, fmt.Errorf("%s: %s", schema.Name, e.Error()))
			}
		}
		return nil
	}); err != nil {
		return nil, nil, err
	}
	if len(errs) > 0 {
		return nil, nil, errors.Join(errs...)
	}

	if err := loadDocuments(servicePath, schema, cfg, &o, reg); err != nil {
		return nil, nil, err
	}

	var verified *ir.Schema
	err := o.profile.Measure("loader.verify", func() error {
		var err error
		verified, err = runVerify(schema, vin)
		return err
	})
	return verified, cfg, err
}

// runVerify executes the format-agnostic verification pass on the assembled
// schema: warnings print to [WarningWriter], errors fail the load.
func runVerify(schema *ir.Schema, vin verify.Input) (*ir.Schema, error) {
	res := verify.Run(schema, vin)
	for _, warning := range res.Warnings {
		_, _ = fmt.Fprintf(WarningWriter, "warning: %s\n", warning.Error())
	}
	if err := res.Err(); err != nil {
		return nil, err
	}
	return schema, nil
}

// hydrateScalarsFromRegistry fills every ScalarDef the schema references
// from the scalar catalog: description, primitive, constraints, custom
// hooks and the per-language type mappings. A name the catalog does not
// know is an error when the schema declares nothing about it beyond its
// identity (a TypeScript brand or a bare data-form entry, which can only
// have meant a catalog scalar); a data-form scalar that carries its own
// constraints is left as written.
func hydrateScalarsFromRegistry(schema *ir.Schema, catalog registry.ScalarCatalog) error {
	if schema == nil {
		return nil
	}
	names := make([]string, 0, len(schema.Scalars))
	for name := range schema.Scalars {
		names = append(names, name)
	}
	sort.Strings(names)
	var unknown []string
	for _, name := range names {
		scalar := schema.Scalars[name]
		if scalar == nil {
			continue
		}
		metadata, ok := catalog.Scalar(scalar.Name)
		if !ok {
			if isCatalogReference(scalar) {
				unknown = append(unknown, scalar.Name)
			}
			continue
		}

		scalar.Description = metadata.Description
		scalar.LanguagePrimitive = languagePrimitiveFromScalarMetadata(metadata.Primitive)
		scalar.Primitive = metadata.Primitive
		scalar.MaxLength = metadata.MaxLength
		scalar.MinLength = metadata.MinLength
		scalar.Maximum = metadata.Maximum
		scalar.Minimum = metadata.Minimum
		scalar.Pattern = metadata.Pattern
		scalar.Format = metadata.Format
		scalar.HasCustomNormalize = metadata.HasCustomNormalize
		scalar.HasCustomParse = metadata.HasCustomParse
		scalar.HasCustomValidate = metadata.HasCustomValidate

		if metadata.SQLType == "" && metadata.JSONSchemaType == "" && metadata.Symbol == "" {
			continue
		}
		if scalar.TypeMappings == nil {
			scalar.TypeMappings = make(map[string]string, 6)
		}
		if metadata.Symbol != "" {
			scalar.TypeMappings["go"] = metadata.Symbol
		}
		if scalar.LanguagePrimitive == ir.LanguageObject && metadata.GoType != "" {
			scalar.TypeMappings["typescript"] = metadata.GoType
		}
		if metadata.SQLType != "" {
			scalar.TypeMappings["sql"] = metadata.SQLType
		}
		if metadata.JSONSchemaType != "" {
			scalar.TypeMappings["json_schema"] = metadata.JSONSchemaType
		}
	}
	if len(unknown) > 0 {
		return fmt.Errorf("unknown scalar %s: not in the scalar registry (%d scalars registered); the schema names it without defining it, so it must come from a registered scalar package or extension", strings.Join(unknown, ", "), len(catalog.Names()))
	}
	return nil
}

// isCatalogReference reports whether def carries nothing but its identity:
// the shape the TypeScript frontend records for a scalar brand and the
// minimal data-form entry. Such a def can only be hydrated from the catalog.
func isCatalogReference(def *ir.ScalarDef) bool {
	return def.Description == "" && def.Primitive == "" && def.Pattern == "" && def.Format == "" &&
		def.MaxLength == 0 && def.MinLength == 0 && def.Minimum == nil && def.Maximum == nil &&
		len(def.ReservedWords) == 0 && def.Example == "" && def.FileUpload == nil && def.ImageConstraints == nil &&
		!def.HasCustomNormalize && !def.HasCustomValidate && !def.HasCustomParse
}

func languagePrimitiveFromScalarMetadata(primitive string) ir.LanguagePrimitive {
	switch strings.ToLower(strings.TrimSpace(primitive)) {
	case "string", "str":
		return ir.LanguageString
	case "number", "float", "float64", "int", "int32", "int64", "integer":
		return ir.LanguageNumber
	case "bool", "boolean":
		return ir.LanguageBoolean
	case "type", "object", "json", "jsonb":
		return ir.LanguageObject
	default:
		return ir.LanguageObject
	}
}

// classifySchemaFiles walks src/ and returns the JSON/YAML schema files and
// the TypeScript schema files (each sorted, slash-normalized, relative to
// the service directory).
func classifySchemaFiles(servicePath string) (dataFiles, tsFiles []string, err error) {
	srcDir := filepath.Join(servicePath, "src")
	walkErr := filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(servicePath, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		switch {
		case strings.HasSuffix(rel, ".schema.ts"):
			tsFiles = append(tsFiles, rel)
		case strings.HasSuffix(rel, ".schema.json"),
			strings.HasSuffix(rel, ".schema.yaml"),
			strings.HasSuffix(rel, ".schema.yml"):
			dataFiles = append(dataFiles, rel)
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, fs.ErrNotExist) {
		return nil, nil, fmt.Errorf("walking %s: %w", srcDir, walkErr)
	}
	sort.Strings(dataFiles)
	sort.Strings(tsFiles)
	return dataFiles, tsFiles, nil
}
