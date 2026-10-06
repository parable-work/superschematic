package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"

	"github.com/parable-work/superschematic/internal/buildcache"
	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/profile"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/sentinel"
	ir "github.com/parable-work/superschematic/ir"
)

type buildServiceOptions struct {
	ServicePath string
	OutputRoot  string
	// SchemasRoot is the schemas root the command resolved. The
	// authoring-import depfile goes under it, not under OutputRoot.
	SchemasRoot    string
	Paths          naming.LocalPaths
	LoadOptions    []loader.Option
	LoadDependency func(name string) (*ir.Schema, error)
	Log            io.Writer
	Profile        *profile.Profiler
	EmitIR         bool
	SkipFormat     bool
	Naming         naming.Naming
	Registry       *registry.Registry

	// DependencyConfig is generator.Options.DependencyConfig: build-all and
	// build --with-deps set it, a single build leaves it nil.
	DependencyConfig func(name string) (*schemaconfig.SchemaConfig, bool)

	// LoadDependencyConfig is generator.Options.LoadDependencyConfig: every
	// build sets it.
	LoadDependencyConfig func(name string) (*schemaconfig.SchemaConfig, error)

	// APILanguage, when set, replaces the loaded config's
	// outputs.api.language (build --api-language).
	APILanguage string

	// Stage runs part of the service's generators (generator.Options.Stage):
	// build-all and build --with-deps split a service whose API calls one
	// built after it into a base and a server stage.
	Stage registry.BuildStage

	// Loaded is what the base stage loaded, which the server stage builds
	// on instead of loading the service again.
	Loaded *buildServiceResult

	// ImplementationRoot is generator.Options.ImplementationRoot: the
	// repository root under build --scaffold, else empty.
	ImplementationRoot string
}

type buildServiceResult struct {
	Schema          *ir.Schema
	Config          *schemaconfig.SchemaConfig
	GeneratorResult *generator.Result
}

// withAPILanguage returns a copy of cfg whose outputs.api.language is
// language, leaving cfg as it was. The config must enable outputs.api; the
// generator then checks that it enables the types the language's server
// needs, as it checks a committed language.
func withAPILanguage(cfg *schemaconfig.SchemaConfig, language string) (*schemaconfig.SchemaConfig, error) {
	api, _ := cfg.Outputs["api"].(map[string]any)
	if enabled, _ := api["enabled"].(bool); !enabled {
		return nil, fmt.Errorf("--api-language %s: %s does not enable outputs.api", language, cfg.Name)
	}
	overridden := *cfg
	overridden.Outputs = maps.Clone(cfg.Outputs)
	apiOutput := maps.Clone(api)
	apiOutput["language"] = language
	overridden.Outputs["api"] = apiOutput
	return &overridden, nil
}

func buildService(opts buildServiceOptions) (*buildServiceResult, error) {
	if opts.Loaded != nil {
		return generateService(opts, opts.Loaded.Schema, opts.Loaded.Config)
	}
	prof := opts.Profile
	loadOpts := append([]loader.Option(nil), opts.LoadOptions...)
	loadOpts = append(loadOpts, loader.WithProfiler(prof), loader.WithNaming(opts.Naming), loader.WithRegistry(opts.Registry))

	var schema *ir.Schema
	var cfg *schemaconfig.SchemaConfig
	if err := prof.Measure("build.load", func() error {
		var err error
		schema, cfg, err = loader.LoadServiceWithConfig(opts.ServicePath, loadOpts...)
		return err
	}); err != nil {
		return nil, err
	}
	if opts.APILanguage != "" {
		overridden, err := withAPILanguage(cfg, opts.APILanguage)
		if err != nil {
			return nil, err
		}
		cfg = overridden
	}

	if opts.EmitIR {
		out, err := json.MarshalIndent(schema, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("marshaling IR: %w", err)
		}
		_, _ = fmt.Fprintln(opts.Log, string(out))
		return &buildServiceResult{Schema: schema, Config: cfg}, nil
	}

	_, _ = fmt.Fprintf(opts.Log, "Loaded schema %s (kind %s): %d types, %d enums, %d scalars, %d operation sets\n",
		schema.Name, schema.Kind, len(schema.Types), len(schema.Enums), len(schema.Scalars), len(schema.OperationSets))

	if err := prof.Measure("build.service-sentinel", func() error {
		if _, err := os.Stat(filepath.Join(opts.ServicePath, "tsconfig.json")); err != nil || sentinel.Skips(opts.Registry, cfg.Kind) {
			return nil
		}
		changed, err := sentinel.EmitService(opts.ServicePath, cfg, opts.Registry, sentinel.ConfigTypeOf(schema))
		if err != nil {
			return fmt.Errorf("emitting service sentinel: %w", err)
		}
		if changed {
			_, _ = fmt.Fprintf(opts.Log, "  + sentinel written (%s)\n", sentinel.GeneratedFile)
		} else {
			_, _ = fmt.Fprintln(opts.Log, "  - sentinel up to date")
		}
		return nil
	}); err != nil {
		return nil, err
	}

	return generateService(opts, schema, cfg)
}

// generateService runs the generators of opts.Stage on a loaded service.
// The base and the whole stage also write the authoring-import depfile;
// the server stage leaves it to the base stage before it.
func generateService(opts buildServiceOptions, schema *ir.Schema, cfg *schemaconfig.SchemaConfig) (*buildServiceResult, error) {
	prof := opts.Profile
	outputRoot := opts.OutputRoot
	if outputRoot == "" {
		outputRoot = filepath.Join(opts.ServicePath, "..", "..", "dist")
	}
	absOutputRoot, err := filepath.Abs(outputRoot)
	if err != nil {
		return nil, fmt.Errorf("resolving output root: %w", err)
	}

	// The repository root is the parent of the schemas root, as the
	// commands resolve it.
	repositoryRoot := ""
	if opts.SchemasRoot != "" {
		repositoryRoot = filepath.Dir(opts.SchemasRoot)
	}

	var result *generator.Result
	if err := prof.Measure("build.generate", func() error {
		var err error
		result, err = generator.Run(schema, cfg, generator.Options{
			OutputRoot:     absOutputRoot,
			ServicePath:    opts.ServicePath,
			Paths:          opts.Paths,
			LoadDependency: opts.LoadDependency,
			Log:            opts.Log,
			Profile:        prof,
			SkipFormat:     opts.SkipFormat,
			Naming:         opts.Naming,
			Registry:       opts.Registry,

			DependencyConfig:     opts.DependencyConfig,
			LoadDependencyConfig: opts.LoadDependencyConfig,
			Stage:                opts.Stage,
			ImplementationRoot:   opts.ImplementationRoot,
			RepositoryRoot:       repositoryRoot,
		})
		return err
	}); err != nil {
		return nil, err
	}

	if opts.Stage == registry.StageServer {
		_, _ = fmt.Fprintf(opts.Log, "Server stage complete: %d outputs generated, %d skipped\n",
			len(result.Outputs), len(result.Skipped))
		return &buildServiceResult{Schema: schema, Config: cfg, GeneratorResult: result}, nil
	}

	// Persist the sidecar documents' crawled module graph for cache
	// invalidation. Runs for single-schema builds
	// too, so a direct `superschematic build` refreshes the depfile.
	if err := prof.Measure("build.authoring-imports", func() error {
		hasDocs := len(schema.Documents) > 0
		return buildcache.WriteAuthoringImports(opts.SchemasRoot, schema.Name, opts.ServicePath, schema.AuthoringImports, hasDocs)
	}); err != nil {
		return nil, err
	}
	// The same for the services the schema's decorator arguments name
	// (D41), which only a load finds.
	if err := prof.Measure("build.schema-references", func() error {
		return buildcache.WriteSchemaReferences(opts.SchemasRoot, schema.Name, schema.References, schema.IdentitySentinels)
	}); err != nil {
		return nil, err
	}

	label := "Build"
	if opts.Stage == registry.StageBase {
		label = "Base stage"
	}
	_, _ = fmt.Fprintf(opts.Log, "%s complete: %d outputs generated, %d skipped\n",
		label, len(result.Outputs), len(result.Skipped))
	return &buildServiceResult{Schema: schema, Config: cfg, GeneratorResult: result}, nil
}
