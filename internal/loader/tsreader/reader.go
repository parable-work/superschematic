package tsreader

import (
	"fmt"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader/verify"
	"github.com/parable-work/superschematic/internal/profile"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// languagePrimitiveNames are the host-language primitives usable directly as
// field types; they resolve without a scalar or type definition.
var languagePrimitiveNames = []string{"string", "number", "boolean"}

// Option adjusts one TypeScript frontend load.
type Option func(*options)

type options struct {
	profile      *profile.Profiler
	programCache *ProgramCache
	registry     *registry.Registry
}

// WithRegistry supplies the registry the walker dispatches decorators
// through and reads kind rules from. Without it the frontend uses a core
// registry built from the default naming.
func WithRegistry(reg *registry.Registry) Option {
	return func(o *options) {
		o.registry = reg
	}
}

// coreRegistry is the frontend's fallback when no registry is supplied:
// core kinds and decorators under the default naming. It is not finalized;
// the loader resolves no pipelines.
func coreRegistry() *registry.Registry {
	return registry.New(naming.Default())
}

// WithProfiler enables phase timing for the TypeScript frontend.
func WithProfiler(prof *profile.Profiler) Option {
	return func(o *options) {
		o.profile = prof
	}
}

// WithProgramCache enables the build-all shared TypeScript program.
func WithProgramCache(cache *ProgramCache) Option {
	return func(o *options) {
		o.programCache = cache
	}
}

// LoadService is the TypeScript frontend entrypoint: it loads one service
// directory (schema.config.{ts,json,yaml} + src/*.schema.ts) and returns the
// v2 Schema IR plus the context the format-agnostic verification pass needs
// (dependency kinds, import sites with locations, compiler-resolved @source
// targets). The caller -- the format-dispatching loader -- runs the pass.
//
// The pipeline is: create one Corsa program per service from its
// tsconfig.json; gate on compiler diagnostics (a type error in a schema file
// IS a schema error); statically read the config; walk every schema file's
// AST into IR; then run the IR-level validation. All schema mistakes come
// back as a SchemaErrorList with file:line:col locations.
func LoadService(servicePath string) (*ir.Schema, *verify.Input, error) {
	schema, _, in, err := LoadServiceWithConfig(servicePath)
	return schema, in, err
}

// ReadServiceConfig statically reads just the service configuration from a
// TypeScript-form service directory: program construction plus the
// defineConfig read, with no diagnostics gating and no schema walk. Sentinel
// emission uses it to learn a sibling service's name and kind without paying
// for (or being blocked by) the sibling's full load.
func ReadServiceConfig(servicePath string, reg *registry.Registry) (*SchemaConfig, error) {
	if reg == nil {
		reg = coreRegistry()
	}
	sp, err := newServiceProgram(servicePath, nil, nil)
	if err != nil {
		return nil, err
	}
	defer sp.close()

	bootstrap := &walker{
		checker:     sp.checker(),
		packages:    newPackageIndex(),
		sp:          sp,
		servicePath: sp.servicePath,
		reg:         reg,
	}
	return readConfig(bootstrap, sp.servicePath, sp.configFile)
}

// LoadServiceWithConfig is LoadService plus the statically-read service
// configuration; the generators need the config's outputs block.
func LoadServiceWithConfig(servicePath string, opts ...Option) (*ir.Schema, *SchemaConfig, *verify.Input, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	if o.registry == nil {
		o.registry = coreRegistry()
	}
	if o.programCache != nil {
		unlock := o.programCache.lockLoad()
		defer unlock()
	}

	var sp *serviceProgram
	if err := o.profile.Measure("tsreader.program", func() error {
		var err error
		sp, err = newServiceProgram(servicePath, o.profile, o.programCache)
		return err
	}); err != nil {
		return nil, nil, nil, err
	}
	defer sp.close()

	var diagErrs SchemaErrorList
	if err := o.profile.Measure("tsreader.diagnostics", func() error {
		diagErrs = sp.diagnostics()
		return nil
	}); err != nil {
		return nil, nil, nil, err
	}
	if len(diagErrs) > 0 {
		return nil, nil, nil, diagErrs
	}

	packages := newPackageIndex()

	// The config read shares the walker's evaluator; kind is unknown until
	// the config is read, so the bootstrap walker carries no schema.
	bootstrap := &walker{
		checker:     sp.checker(),
		packages:    packages,
		sp:          sp,
		servicePath: sp.servicePath,
		reg:         o.registry,
	}
	var cfg *SchemaConfig
	if err := o.profile.Measure("tsreader.config", func() error {
		var err error
		cfg, err = readConfig(bootstrap, sp.servicePath, sp.configFile)
		return err
	}); err != nil {
		return nil, nil, nil, err
	}

	schema := ir.NewSchema(cfg.Name, cfg.Kind)
	w := newWalker(sp, cfg, schema, packages, o.registry)
	if err := o.profile.Measure("tsreader.walk", func() error {
		for _, file := range sp.schemaFiles {
			w.walkFile(file)
		}
		w.finishImports()
		return nil
	}); err != nil {
		return nil, nil, nil, err
	}

	if len(w.errs) > 0 {
		return nil, nil, nil, w.errs
	}

	externals := make(map[string]bool, len(w.externals)+len(languagePrimitiveNames))
	for name := range w.externals {
		externals[name] = true
	}
	for _, name := range languagePrimitiveNames {
		externals[name] = true
	}
	var errs SchemaErrorList
	if err := o.profile.Measure("tsreader.validate-ir", func() error {
		if validationErrs := schema.Validate(ir.WithKnownExternals(externals)); len(validationErrs) > 0 {
			for _, e := range validationErrs {
				errs = append(errs, &SchemaError{Msg: fmt.Sprintf("%s: %s", cfg.Name, e.Error())})
			}
		}
		return nil
	}); err != nil {
		return nil, nil, nil, err
	}
	if len(errs) > 0 {
		return nil, nil, nil, errs
	}

	deps := make(map[string]ir.SchemaKind, len(cfg.Dependencies))
	for _, dep := range cfg.Dependencies {
		deps[dep.Name] = dep.Kind
	}
	in := &verify.Input{
		Dependencies:  deps,
		ImportSites:   w.importSites,
		ExternalTypes: w.externalTypes,
		ExternalEnums: w.externalEnums,
	}
	return schema, cfg, in, nil
}
