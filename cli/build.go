package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/parable-work/superschematic/internal/buildplan"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/loader/tsreader"
	"github.com/parable-work/superschematic/internal/profile"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/sentinel"
	ir "github.com/parable-work/superschematic/ir"
)

// buildFlags holds one build command's flag values.
type buildFlags struct {
	emitIR     bool
	out        string
	profile    bool
	skipFormat bool
	namingPath string
	withDeps   bool
}

// newBuildCmd is the build orchestrator entrypoint: it loads a service
// directory through the format-dispatching loader into the Schema IR, then
// runs the generators selected by the schema kind and the config's outputs
// block. Requested outputs without a registered generator are reported and
// skipped.
func newBuildCmd(a *app) *cobra.Command {
	flags := &buildFlags{}
	cmd := &cobra.Command{
		Use:   "build <service-dir>",
		Short: "Build a schema service: load it into the Schema IR and run the generators",
		Long: `Build reads a schema service directory (schema.config.{ts,json,yaml} plus
src/*.schema.{ts,json,yaml}) through the format-dispatching loader:
TypeScript files go through the Corsa frontend (one compiler program per
service, semantic diagnostics gated as schema errors, a static AST walk);
JSON and YAML files are validated against the schema-file JSON Schema and
decoded directly, since their on-disk shape mirrors the IR.

Generated artifacts are written to the output root (--out), which defaults
to the dist/ directory two levels above the service directory
(<schemas-root>/dist for services under <schemas-root>/services/). Names and
in-tree paths come from <schemas-root>/superschematic.toml or --naming.

Use --emit-ir to print the IR as JSON instead of generating code.

Use --with-deps to also build every schema service the target transitively
depends on (declared dependencies plus authDb), dependencies first. The
closure is resolved from the sibling services under the target's parent
directory with the discovery and ordering build-all uses; siblings outside
the closure are not built. A container image or CI job that needs one
service's generated packages builds them with one command instead of
listing the dependencies by hand.

Examples:
  superschematic build ./schemas/services/shop-db
  superschematic build --with-deps ./schemas/services/shop-api`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBuild(cmd, a, flags, args[0])
		},
	}
	cmd.Flags().BoolVar(&flags.emitIR, "emit-ir", false, "print the Schema IR as JSON to stdout")
	cmd.Flags().BoolVar(&flags.withDeps, "with-deps", false, "also build the target's transitive dependencies (declared dependencies plus authDb), dependencies first")
	cmd.Flags().StringVar(&flags.out, "out", "", "output root for generated artifacts (default <service-dir>/../../dist)")
	cmd.Flags().BoolVar(&flags.profile, "profile", false, "emit build phase timings to stderr")
	cmd.Flags().BoolVar(&flags.skipFormat, "skip-format", false, "skip developer-friendly formatting for generated files")
	cmd.Flags().StringVar(&flags.namingPath, "naming", "", "naming config file (default <service-dir>/../../superschematic.toml)")
	return cmd
}

// targetImportsSiblingSentinels detects a target whose kind sets
// KindSpec.ImportsSiblingSentinels without constructing a compiler program:
// the data-form config states the kind directly; for the TypeScript form,
// schema.config.ts names the kind either as a SchemaKind enum member (the
// core kinds) or, for a kind the enum does not list, as a string literal
// (`kind: "Platform"`), so a textual scan for `SchemaKind.<Kind>` or the
// quoted name covers valid configs. A false positive only triggers an extra
// idempotent sentinel pass.
func targetImportsSiblingSentinels(servicePath string, reg *registry.Registry) bool {
	imports := func(kind string) bool {
		spec, ok := reg.Kind(kind)
		return ok && spec.ImportsSiblingSentinels
	}
	if cfg, err := schemaconfig.ReadFile(servicePath, reg); err == nil {
		return imports(string(cfg.Kind))
	} else if !errors.Is(err, os.ErrNotExist) {
		return false
	}
	data, err := os.ReadFile(filepath.Join(servicePath, "schema.config.ts"))
	if err != nil {
		return false
	}
	for _, kind := range reg.Kinds() {
		if !imports(kind) {
			continue
		}
		for _, form := range []string{"SchemaKind." + kind, `"` + kind + `"`, "'" + kind + "'"} {
			if bytes.Contains(data, []byte(form)) {
				return true
			}
		}
	}
	return false
}

func runBuild(cmd *cobra.Command, a *app, flags *buildFlags, servicePath string) error {
	if info, err := os.Stat(servicePath); err != nil || !info.IsDir() {
		return fmt.Errorf("service directory not found: %s", servicePath)
	}
	if flags.withDeps && flags.emitIR {
		return fmt.Errorf("--emit-ir prints one schema's IR and cannot be combined with --with-deps")
	}

	var prof *profile.Profiler
	if flags.profile {
		prof = profile.New(filepath.Base(servicePath), cmd.ErrOrStderr())
	}

	// The schemas root is the parent of the services directory; the
	// repository root, which [paths] keys resolve against, is its parent.
	schemasRoot := filepath.Join(servicePath, "..", "..")
	repoRoot, err := filepath.Abs(filepath.Join(schemasRoot, ".."))
	if err != nil {
		return fmt.Errorf("resolving repository root: %w", err)
	}
	names, err := resolveNaming(flags.namingPath, schemasRoot)
	if err != nil {
		return err
	}
	reg, err := a.resolveRegistry(names)
	if err != nil {
		return err
	}

	// A service whose kind imports sibling sentinels needs those files on
	// disk before its program is constructed.
	if err := prof.Measure("build.sibling-sentinels", func() error {
		if !targetImportsSiblingSentinels(servicePath, reg) {
			return nil
		}
		absServicePath, err := filepath.Abs(servicePath)
		if err != nil {
			return fmt.Errorf("resolving service path: %w", err)
		}
		err = sentinel.EnsureSiblings(filepath.Dir(absServicePath), sentinel.Options{
			ReadTSConfig: func(servicePath string) (*schemaconfig.SchemaConfig, error) {
				return tsreader.ReadServiceConfig(servicePath, reg)
			},
			Registry: reg,
			Log:      cmd.OutOrStdout(),
		})
		if err != nil {
			return err
		}
		return nil
	}); err != nil {
		return err
	}

	loadOpts := []loader.Option{loader.WithProfiler(prof)}

	outputRoot := flags.out
	if outputRoot == "" {
		outputRoot = filepath.Join(schemasRoot, "dist")
	}
	absOutputRoot, err := filepath.Abs(outputRoot)
	if err != nil {
		return fmt.Errorf("resolving output root: %w", err)
	}
	outputRoot = absOutputRoot

	if flags.withDeps {
		return runBuildWithDeps(cmd, reg, names, flags, servicePath, outputRoot, repoRoot)
	}

	_, err = buildService(buildServiceOptions{
		Naming:      names,
		ServicePath: servicePath,
		OutputRoot:  outputRoot,
		Paths:       names.LocalPaths(repoRoot),
		LoadOptions: loadOpts,
		LoadDependency: func(name string) (*ir.Schema, error) {
			return loader.LoadService(filepath.Join(servicePath, "..", name), loader.WithProfiler(prof), loader.WithNaming(names), loader.WithRegistry(reg))
		},
		Log:        cmd.OutOrStdout(),
		Profile:    prof,
		EmitIR:     flags.emitIR,
		SkipFormat: flags.skipFormat,
		Registry:   reg,
	})
	return err
}

// runBuildWithDeps builds the target and every schema service it
// transitively depends on, dependencies first. Discovery, ordering, the
// schema catalog, the shared TypeScript program, the dependency schema cache
// and the per-service build are build-all's; only the set of services
// differs, so the two commands cannot disagree about what a service needs or
// in which order. Unlike build-all it writes no dependency graph and runs no
// build-all hooks: both describe the whole services root, and a closure is a
// slice of it.
func runBuildWithDeps(cmd *cobra.Command, reg *registry.Registry, names naming.Naming, flags *buildFlags, servicePath, outputRoot, repoRoot string) error {
	absServicePath, err := filepath.Abs(servicePath)
	if err != nil {
		return fmt.Errorf("resolving service path: %w", err)
	}
	servicesRoot := filepath.Dir(absServicePath)

	services, err := buildplan.DiscoverWith(servicesRoot, outputRoot, reg)
	if err != nil {
		return err
	}
	rootName := ""
	for _, service := range services {
		if filepath.Clean(service.Dir) == absServicePath {
			rootName = service.Name
			break
		}
	}
	if rootName == "" {
		return fmt.Errorf("%s is not a schema service under %s", absServicePath, servicesRoot)
	}
	closure, err := buildplan.Closure(services, rootName)
	if err != nil {
		return err
	}

	closureNames := make([]string, 0, len(closure))
	for _, service := range closure {
		closureNames = append(closureNames, service.Name)
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Resolved %d schema services for %s: %s\n", len(closure), rootName, strings.Join(closureNames, ", "))

	// runBuild wrote the target's sibling sentinels when its kind needs
	// them; a closure member of such a kind needs them too.
	for _, service := range closure {
		if spec, ok := reg.Kind(string(service.Config.Kind)); !ok || !spec.ImportsSiblingSentinels {
			continue
		}
		if err := sentinel.EnsureSiblings(servicesRoot, sentinel.Options{
			ReadTSConfig: func(servicePath string) (*schemaconfig.SchemaConfig, error) {
				return tsreader.ReadServiceConfig(servicePath, reg)
			},
			Registry: reg,
			Log:      cmd.OutOrStdout(),
		}); err != nil {
			return err
		}
		break
	}

	// The catalog is build-all's: a document that references another service
	// by name resolves it against every discovered service, not only the
	// closure, because a reference is an identity, not a build edge.
	catalog := make(map[string]registry.SchemaCatalogEntry, len(services))
	serviceByName := make(map[string]buildplan.Service, len(services))
	for _, service := range services {
		catalog[service.Name] = registry.SchemaCatalogEntry{Kind: string(service.Config.Kind), AuthDB: service.Config.AuthDB}
		serviceByName[service.Name] = service
	}

	ctx := buildAllTaskContext{
		outputRoot:     outputRoot,
		repoRoot:       repoRoot,
		loadOpts:       []loader.Option{loader.WithSchemaCatalog(catalog), loader.WithNaming(names), loader.WithRegistry(reg)},
		schemaCache:    newSharedSchemaCache(),
		serviceByName:  serviceByName,
		profileWriter:  cmd.ErrOrStderr(),
		profileEnabled: flags.profile,
		skipFormat:     flags.skipFormat,
		naming:         names,
		registry:       reg,
	}
	if tsServiceDirs := tsServiceDirectories(closure); len(tsServiceDirs) > 0 {
		ctx.tsProgramCache = tsreader.NewProgramCache(tsServiceDirs)
		defer ctx.tsProgramCache.Close()
	}
	for _, service := range closure {
		if err := executeBuildAllTask(cmd, buildAllTask{service: service}, ctx); err != nil {
			return err
		}
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\nBuilt %d schema services for %s\n", len(closure), rootName)
	return nil
}
