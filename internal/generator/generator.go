package generator

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// The run configuration, its result and the registry live in
// internal/registry so the loader can share them; these aliases keep every
// existing caller compiling.
type (
	Options  = registry.Options
	Result   = registry.Result
	Registry = registry.Registry
)

// CoreRegistry returns a finalized registry holding the core kinds and
// generators and nothing else: what tests want in one call. Core registration
// is static, so a failure here is a programming error and panics; that
// includes a Naming whose AuthProvider is not the core session provider,
// which no core-only registry can finalize. Building one takes a few
// microseconds (about sixty allocations), so callers build it per call; a
// process-wide cache would have to be keyed by Naming, which holds maps and
// is not comparable, and would return the wrong names when --naming differs.
func CoreRegistry(n naming.Naming) *registry.Registry {
	reg, err := assembleCore(n)
	if err != nil {
		panic("generator: core registry: " + err.Error())
	}
	return reg
}

// assembleCore runs New, RegisterCore, Finalize with no extensions.
func assembleCore(n naming.Naming) (*registry.Registry, error) {
	reg := registry.New(n)
	if err := RegisterCore(reg); err != nil {
		return nil, err
	}
	if err := reg.Finalize(); err != nil {
		return nil, err
	}
	return reg, nil
}

// run is the generator package's view of one GenerateContext: the methods in
// dispatch*.go hang off it. It holds no state of its own; the per-run memos
// live behind the context's LoadDependency and APIOutput closures so every
// generator in a pipeline shares them.
type run struct {
	registry.GenerateContext
}

// Run resolves the schema's kind in the registry and executes the kind's
// generator pipeline, then the presence-driven document generators.
func Run(schema *ir.Schema, cfg *schemaconfig.SchemaConfig, opts Options) (*Result, error) {
	if opts.Clock == nil {
		opts.Clock = codegen.DefaultClock()
	}
	opts.Naming = opts.Naming.OrDefault()
	if opts.OutputRoot == "" {
		return nil, fmt.Errorf("generator: output root is required")
	}
	if opts.Registry == nil {
		// The core alone; an error rather than a panic because the naming is
		// the caller's input (a superschematic.toml selecting an extension's
		// auth provider fails here when no extension is linked).
		core, err := assembleCore(opts.Naming)
		if err != nil {
			return nil, fmt.Errorf("generator: no registry given and the core alone cannot serve the naming: %w", err)
		}
		opts.Registry = core
	}
	reg := opts.Registry

	outputs, err := registry.ParseOutputs(cfg.Outputs, reg)
	if err != nil {
		return nil, fmt.Errorf("schema config for %s: %w", cfg.Name, err)
	}

	kind, ok := reg.Kind(string(schema.Kind))
	if !ok {
		return nil, fmt.Errorf("generator: unknown schema kind %q", schema.Kind)
	}
	if err := refuseORMWithoutGoTypes(kind.Name, outputs, reg); err != nil {
		return nil, fmt.Errorf("schema config for %s: %w", cfg.Name, err)
	}

	r, _ := newRun(schema, cfg, outputs, opts, reg)

	pipeline := reg.Pipeline(kind.Name)
	if len(pipeline) == 0 {
		r.Logf("  - No generator outputs for schema kind %s\n", schema.Kind)
	} else if err := r.measure("kind."+kind.Name, func() error {
		return r.runPipeline(pipeline)
	}); err != nil {
		return nil, err
	}

	// Document generators are presence-driven: a registered document that
	// the loader attached to the schema runs its Generate regardless of the
	// kind and outputs switches (extension-model section 7.1). They run in
	// Registry.Documents() order, which is sorted by name, not registration
	// order; the order only reaches the Log stream and the profile, since
	// Result.Outputs is a map and Skipped is sorted below.
	if err := r.runDocuments(); err != nil {
		return nil, err
	}

	sort.Strings(r.Result.Skipped)
	return r.Result, nil
}

// runDocuments executes Generate for every registered document present in
// Schema.Documents.
func (r run) runDocuments() error {
	for _, spec := range r.Registry.Documents() {
		if spec.Generate == nil {
			continue
		}
		doc, ok := r.Schema.Documents[spec.Name]
		if !ok {
			continue
		}
		if err := r.measure("document."+spec.Name, func() error {
			return spec.Generate(r.GenerateContext, doc)
		}); err != nil {
			return err
		}
	}
	return nil
}

// newRun builds the shared context for one generator run and the memo its
// LoadDependency and APIOutput closures capture.
func newRun(schema *ir.Schema, cfg *schemaconfig.SchemaConfig, outputs *registry.Outputs, opts Options, reg *registry.Registry) (run, *runMemo) {
	memo := &runMemo{}
	r := run{GenerateContext: registry.GenerateContext{
		Schema:         schema,
		Config:         cfg,
		Outputs:        outputs,
		Options:        opts,
		Registry:       reg,
		LoadDependency: memo.loadDependency,
		APIOutput:      memo.buildAPIOutput,
		EnvConfig:      memo.buildEnvConfig,
		Result:         &registry.Result{Outputs: make(map[string]string)},
	}}
	memo.r = r
	return r, memo
}

// runPipeline decides which generators run, rejects a pipeline in which two
// of them claim the same output directory, then executes them in order.
func (r run) runPipeline(pipeline []registry.GeneratorSpec) error {
	owners := map[string]string{}
	enabled := make([]registry.GeneratorSpec, 0, len(pipeline))
	for _, gen := range pipeline {
		if gen.Enabled != nil {
			ok, reason := gen.Enabled(r.GenerateContext)
			if !ok {
				if reason != "" {
					r.Skip(reason)
				}
				continue
			}
		}
		if gen.Dirs != nil {
			for _, dir := range gen.Dirs(r.GenerateContext) {
				if prev, dup := owners[dir]; dup && prev != gen.Name {
					return fmt.Errorf("generator: %s and %s both write %s", prev, gen.Name, dir)
				}
				owners[dir] = gen.Name
			}
		}
		enabled = append(enabled, gen)
	}
	if err := refuseBehaviors(r.Schema, enabled); err != nil {
		return err
	}
	if err := r.refuseMissingDependencyTypes(enabled); err != nil {
		return err
	}
	for _, gen := range enabled {
		if err := gen.Generate(r.GenerateContext); err != nil {
			return err
		}
	}
	return nil
}

// refuseBehaviors fails, before any generator runs, when a type of the
// schema composes a behavior and an enabled generator does not render
// behaviors (GeneratorSpec.RendersBehaviors): that generator would write
// the type without the fields and operations its behaviors add. The error
// names the generator, the type and the behavior. build --emit-ir, format
// and json-schema run no generator and accept the schema (D16).
func refuseBehaviors(schema *ir.Schema, enabled []registry.GeneratorSpec) error {
	typeName, behavior, found := schema.FindBehavior()
	if !found {
		return nil
	}
	for _, gen := range enabled {
		if !gen.RendersBehaviors {
			return fmt.Errorf("generator: %s does not render behaviors yet: type %s composes behavior %s", gen.Name, typeName, behavior)
		}
	}
	return nil
}

// refuseMissingDependencyTypes fails, before any generator runs, when a
// type library the types generator writes imports a dependency that does
// not generate its own types in that language: the Go module would require,
// the TypeScript package depend on and the Rust crate path-depend on a
// package the build never writes, and the Python package would bind the
// imported types to Any and drop their validation. The error names every
// such language and dependency. A dependency whose config the build does
// not have (every one, for a single build without Options.DependencyConfig)
// is logged as not checked instead.
func (r run) refuseMissingDependencyTypes(enabled []registry.GeneratorSpec) error {
	langs := r.Outputs.EnabledTypeLanguages()
	if len(langs) == 0 || len(r.Schema.Imports) == 0 {
		return nil
	}
	if !slices.ContainsFunc(enabled, func(gen registry.GeneratorSpec) bool { return gen.Name == typesGenerator }) {
		return nil
	}
	deps, err := r.loadDependencySchemas()
	if err != nil {
		return err
	}

	var problems []error
	var unchecked []string
	for _, dep := range codegen.TypeDependencies(r.Schema, deps) {
		var cfg *schemaconfig.SchemaConfig
		ok := false
		if r.Options.DependencyConfig != nil {
			cfg, ok = r.Options.DependencyConfig(dep)
		}
		if !ok {
			unchecked = append(unchecked, dep)
			continue
		}
		depOutputs, err := registry.ParseOutputs(cfg.Outputs, r.Registry)
		if err != nil {
			return fmt.Errorf("schema config for %s: %w", dep, err)
		}
		var names, switches []string
		for _, lang := range langs {
			if !depOutputs.TypesEnabled(lang) {
				names = append(names, registry.LanguageName(lang))
				switches = append(switches, "outputs.types."+lang)
			}
		}
		if len(names) > 0 {
			problems = append(problems, fmt.Errorf("%s generates %s types, which use %s's %s types; enable %s in %s",
				r.Config.Name, joinAnd(names), dep, joinAnd(names), joinAnd(switches), dep))
		}
	}

	if len(unchecked) > 0 {
		switches := make([]string, 0, len(langs))
		for _, lang := range langs {
			switches = append(switches, "outputs.types."+lang)
		}
		r.Logf("  - not checked: %s must enable %s, since %s's types import theirs; build --with-deps and build-all check it\n",
			joinAnd(unchecked), joinAnd(switches), r.Config.Name)
	}
	return errors.Join(problems...)
}

// joinAnd joins items as "a", "a and b" or "a, b and c".
func joinAnd(items []string) string {
	if len(items) <= 1 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

func (r run) measure(phase string, fn func() error) error {
	return r.Options.Profile.Measure("generator."+phase, fn)
}
