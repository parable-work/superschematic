package cli

import (
	"encoding/json"
	"fmt"
	"io"
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
	ServicePath    string
	OutputRoot     string
	ScalarLibPath  string
	LoadOptions    []loader.Option
	LoadDependency func(name string) (*ir.Schema, error)
	Log            io.Writer
	Profile        *profile.Profiler
	EmitIR         bool
	SkipFormat     bool
	Naming         naming.Naming
	Registry       *registry.Registry
}

type buildServiceResult struct {
	Schema          *ir.Schema
	Config          *schemaconfig.SchemaConfig
	GeneratorResult *generator.Result
}

func buildService(opts buildServiceOptions) (*buildServiceResult, error) {
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
		changed, err := sentinel.EmitService(opts.ServicePath, cfg, opts.Registry)
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

	outputRoot := opts.OutputRoot
	if outputRoot == "" {
		outputRoot = filepath.Join(opts.ServicePath, "..", "..", "dist")
	}
	absOutputRoot, err := filepath.Abs(outputRoot)
	if err != nil {
		return nil, fmt.Errorf("resolving output root: %w", err)
	}

	var result *generator.Result
	if err := prof.Measure("build.generate", func() error {
		var err error
		result, err = generator.Run(schema, cfg, generator.Options{
			OutputRoot:     absOutputRoot,
			ServicePath:    opts.ServicePath,
			ScalarLibPath:  opts.ScalarLibPath,
			LoadDependency: opts.LoadDependency,
			Log:            opts.Log,
			Profile:        prof,
			SkipFormat:     opts.SkipFormat,
			Naming:         opts.Naming,
			Registry:       opts.Registry,
		})
		return err
	}); err != nil {
		return nil, err
	}

	// Persist the sidecar documents' crawled module graph for cache
	// invalidation (EDR-0087 amendment 2). Runs for single-schema builds
	// too, so a direct `psgen build` refreshes the depfile.
	if err := prof.Measure("build.authoring-imports", func() error {
		hasDocs := len(schema.Documents) > 0
		return buildcache.WriteAuthoringImports(absOutputRoot, schema.Name, opts.ServicePath, schema.AuthoringImports, hasDocs)
	}); err != nil {
		return nil, err
	}

	_, _ = fmt.Fprintf(opts.Log, "Build complete: %d outputs generated, %d skipped\n",
		len(result.Outputs), len(result.Skipped))
	return &buildServiceResult{Schema: schema, Config: cfg, GeneratorResult: result}, nil
}
