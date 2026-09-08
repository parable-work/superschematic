package generator

import (
	"fmt"
	"sort"

	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
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

// CoreReadRegistry is the registry the read-only fallbacks (buildplan's
// discovery, ParseOutputs, ExpectedOutputDirs) use when a caller passes none:
// the core under the default naming with the core's own auth provider
// selected. Those callers read kinds and output keys, which do not depend on
// the provider, and the in-tree default naming names a provider only the
// Parable extension registers (W11 flips the default; this helper then
// equals CoreRegistry(naming.Default())). Nothing generated flows through
// it, so the provider choice cannot reach an output file.
func CoreReadRegistry() *registry.Registry {
	n := naming.Default()
	n.AuthProvider = sessionauth.Name
	return CoreRegistry(n)
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
	for _, gen := range enabled {
		if err := gen.Generate(r.GenerateContext); err != nil {
			return err
		}
	}
	return nil
}

func (r run) measure(phase string, fn func() error) error {
	return r.Options.Profile.Measure("generator."+phase, fn)
}
