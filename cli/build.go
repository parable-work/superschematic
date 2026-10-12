package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/parable-work/superschematic/internal/buildcache"
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
	scaffold   bool
	// apiLanguage, when set, replaces the target's outputs.api.language
	// for this build (--api-language).
	apiLanguage string
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

Use --api-language to build the target's API server in another language
(GO, RUST or TYPESCRIPT) than its config names, for example to build a Rust
server of a service whose committed config builds a Go one. It applies to the
target only, never to the dependencies --with-deps builds, and the target's
config must enable outputs.api and the types of that language. Pair it with
--out so the two servers do not share an output root.

Use --with-deps to also build every schema service the target transitively
depends on (declared dependencies, authDb and calls), dependencies first. The
closure is resolved from the sibling services under the target's parent
directory with the discovery and ordering build-all uses; siblings outside
the closure are not built. A container image or CI job that needs one
service's generated packages builds them with one command instead of
listing the dependencies by hand.

Use --scaffold to write the implementation of each Go or TypeScript API
built whose package is missing: a package at the naming file's
[implementation_paths] template of its language (go/{service} and
typescript/{service} from the parent of the schemas root by default)
whose New, or create, has the generated Constructor's type and whose
methods answer 501 until implemented. It never writes into a package that
exists.

Examples:
  superschematic build ./schemas/services/shop-db
  superschematic build --with-deps ./schemas/services/shop-api
  superschematic build --with-deps --api-language RUST --out ./schemas/dist-rust ./schemas/services/shop-api`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBuild(cmd, a, flags, args[0])
		},
	}
	cmd.Flags().BoolVar(&flags.emitIR, "emit-ir", false, "print the Schema IR as JSON to stdout")
	cmd.Flags().BoolVar(&flags.withDeps, "with-deps", false, "also build the target's transitive dependencies (declared dependencies, authDb and calls), dependencies first")
	cmd.Flags().BoolVar(&flags.scaffold, "scaffold", false, "write the implementation scaffold of each Go or TypeScript API built whose package is missing, at the [implementation_paths] template of its language")
	cmd.Flags().StringVar(&flags.out, "out", "", "output root for generated artifacts (default <service-dir>/../../dist)")
	cmd.Flags().BoolVar(&flags.profile, "profile", false, "emit build phase timings to stderr")
	cmd.Flags().BoolVar(&flags.skipFormat, "skip-format", false, "skip developer-friendly formatting for generated files")
	cmd.Flags().StringVar(&flags.namingPath, "naming", "", "naming config file (default <service-dir>/../../superschematic.toml)")
	cmd.Flags().StringVar(&flags.apiLanguage, "api-language", "", "build the target's API server in this language (GO, RUST or TYPESCRIPT) instead of its config's outputs.api.language")
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

// configImportPattern matches the import declarations of a schema.config.ts:
// `import ... from "<specifier>"` and `import "<specifier>"`.
var configImportPattern = regexp.MustCompile(`(?m)^\s*import\b[^'"]*['"]([^'"]+)['"]`)

// configImportsSentinels reports whether the target's schema.config.ts
// imports anything but the config package, which the config import rule
// allows only for other services' sentinels (D34). It scans the text, so
// it needs no compiler program; a false positive only triggers an extra
// idempotent sentinel sweep.
func configImportsSentinels(servicePath string, n naming.Naming) bool {
	data, err := os.ReadFile(filepath.Join(servicePath, "schema.config.ts"))
	if err != nil {
		return false
	}
	for _, match := range configImportPattern.FindAllStringSubmatch(string(data), -1) {
		if n.DeclaringPackage(match[1]) != sentinel.ConfigPackage {
			return true
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
	if flags.apiLanguage != "" {
		language, err := apiLanguageFlag(flags.apiLanguage)
		if err != nil {
			return err
		}
		flags.apiLanguage = language
	}

	var prof *profile.Profiler
	if flags.profile {
		prof = profile.New(filepath.Base(servicePath), cmd.ErrOrStderr())
	}

	// The schemas root is the parent of the services directory; the
	// repository root, which [paths] keys resolve against, is its parent.
	schemasRoot, err := filepath.Abs(filepath.Join(servicePath, "..", ".."))
	if err != nil {
		return fmt.Errorf("resolving schemas root: %w", err)
	}
	repoRoot := filepath.Dir(schemasRoot)
	buildcache.SetSchemasRoot(schemasRoot)
	names, err := resolveNaming(flags.namingPath, schemasRoot)
	if err != nil {
		return err
	}
	reg, err := a.resolveRegistry(names)
	if err != nil {
		return err
	}

	// Sibling sentinels must be on disk before a program that imports them
	// is constructed: for a target whose kind imports them, a target whose
	// config imports them (D34), and --with-deps, whose discovery reads every
	// sibling's config.
	if err := prof.Measure("build.sibling-sentinels", func() error {
		if !flags.withDeps && !targetImportsSiblingSentinels(servicePath, reg) && !configImportsSentinels(servicePath, names) {
			return nil
		}
		absServicePath, err := filepath.Abs(servicePath)
		if err != nil {
			return fmt.Errorf("resolving service path: %w", err)
		}
		return buildplan.EnsureSentinels(filepath.Dir(absServicePath), reg, cmd.OutOrStdout())
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
		return runBuildWithDeps(cmd, reg, names, flags, servicePath, outputRoot, schemasRoot)
	}
	implementationRoot := ""
	if flags.scaffold {
		implementationRoot = repoRoot
	}

	// The load reads an API's authDb through the loader the generators
	// use, so the build reads it once.
	loadDependency := memoizedSchemas(func(name string) (*ir.Schema, error) {
		return loader.LoadService(filepath.Join(servicePath, "..", name), loader.WithProfiler(prof), loader.WithNaming(names), loader.WithRegistry(reg))
	})
	loadOpts = append(loadOpts, loader.WithDependencyLoader(loadDependency))

	_, err = buildService(buildServiceOptions{
		Naming:         names,
		ServicePath:    servicePath,
		OutputRoot:     outputRoot,
		SchemasRoot:    schemasRoot,
		Paths:          names.LocalPaths(repoRoot),
		LoadOptions:    loadOpts,
		LoadDependency: loadDependency,
		LoadDependencyConfig: func(name string) (*schemaconfig.SchemaConfig, error) {
			return buildplan.ReadConfig(filepath.Join(servicePath, "..", name), reg)
		},
		Log:         cmd.OutOrStdout(),
		Profile:     prof,
		EmitIR:      flags.emitIR,
		SkipFormat:  flags.skipFormat,
		Registry:    reg,
		APILanguage: flags.apiLanguage,

		ImplementationRoot: implementationRoot,
	})
	return err
}

// memoizedSchemas returns load, reading each schema once: the later calls
// for a name return the first call's schema, or its error.
func memoizedSchemas(load func(name string) (*ir.Schema, error)) func(name string) (*ir.Schema, error) {
	type loaded struct {
		schema *ir.Schema
		err    error
	}
	var mu sync.Mutex
	seen := map[string]loaded{}
	return func(name string) (*ir.Schema, error) {
		mu.Lock()
		defer mu.Unlock()
		if l, ok := seen[name]; ok {
			return l.schema, l.err
		}
		schema, err := load(name)
		seen[name] = loaded{schema, err}
		return schema, err
	}
}

// apiLanguageFlag returns the --api-language value as the config spells it,
// accepting any case, or an error naming the languages it accepts.
func apiLanguageFlag(value string) (string, error) {
	language := strings.ToUpper(strings.TrimSpace(value))
	switch language {
	case registry.APILanguageGo, registry.APILanguageRust, registry.APILanguageTypeScript:
		return language, nil
	}
	return "", fmt.Errorf("--api-language %q: want %s, %s or %s", value, registry.APILanguageGo, registry.APILanguageRust, registry.APILanguageTypeScript)
}

// runBuildWithDeps builds the target and every schema service it
// transitively depends on, dependencies first. Discovery, ordering, the
// schema catalog, the shared TypeScript program, the dependency schema cache
// and the per-service build are build-all's; only the set of services
// differs, so the two commands cannot disagree about what a service needs or
// in which order. Unlike build-all it writes no dependency graph and runs no
// build-all hooks: both describe the whole services root, and a closure is a
// slice of it.
func runBuildWithDeps(cmd *cobra.Command, reg *registry.Registry, names naming.Naming, flags *buildFlags, servicePath, outputRoot, schemasRoot string) error {
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

	opts := closureBuild{
		outputRoot:  outputRoot,
		schemasRoot: schemasRoot,
		profile:     flags.profile,
		skipFormat:  flags.skipFormat,
		apiLanguage: map[string]string{rootName: flags.apiLanguage},
	}
	if flags.scaffold {
		opts.scaffoldRoot = filepath.Dir(schemasRoot)
	}
	return buildClosure(cmd, reg, names, services, closure, rootName, opts)
}

// closureBuild is how buildClosure builds.
type closureBuild struct {
	outputRoot  string
	schemasRoot string
	profile     bool
	skipFormat  bool
	// scaffoldRoot is the repository root under --scaffold, else empty.
	scaffoldRoot string
	// apiLanguage replaces a service's outputs.api.language, by service.
	apiLanguage map[string]string
}

// buildClosure builds closure, a slice of the discovered services in
// their order, for root: build --with-deps's closure, or stack dev's.
// Discovery, ordering, the schema catalog, the shared TypeScript program,
// the dependency schema cache and the per-service build are build-all's.
func buildClosure(cmd *cobra.Command, reg *registry.Registry, names naming.Naming, services, closure []buildplan.Service, root string, opts closureBuild) error {
	closureNames := make([]string, 0, len(closure))
	for _, service := range closure {
		closureNames = append(closureNames, service.Name)
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Resolved %d schema services for %s: %s\n", len(closure), root, strings.Join(closureNames, ", "))

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
		outputRoot:     opts.outputRoot,
		schemasRoot:    opts.schemasRoot,
		repoRoot:       filepath.Dir(opts.schemasRoot),
		loadOpts:       []loader.Option{loader.WithSchemaCatalog(catalog), loader.WithNaming(names), loader.WithRegistry(reg)},
		schemaCache:    newSharedSchemaCache(),
		serviceByName:  serviceByName,
		profileWriter:  cmd.ErrOrStderr(),
		profileEnabled: opts.profile,
		skipFormat:     opts.skipFormat,
		naming:         names,
		registry:       reg,
		loaded:         newLoadedServices(),
		scaffoldRoot:   opts.scaffoldRoot,
	}
	tsServiceDirs, err := tsServiceDirectories(closure)
	if err != nil {
		return err
	}
	if len(tsServiceDirs) > 0 {
		ctx.tsProgramCache = tsreader.NewProgramCache(tsServiceDirs)
		defer ctx.tsProgramCache.Close()
	}
	// The build orders outputs: each API's server builds after the SDKs
	// of the APIs it calls (docs/stack-model.md, section 3.3).
	for _, step := range buildplan.Steps(closure) {
		task := buildAllTask{service: step.Service, apiLanguage: opts.apiLanguage[step.Service.Name]}
		if err := executeBuildAllStep(cmd, task, step.Stage, ctx); err != nil {
			return err
		}
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\nBuilt %d schema services for %s\n", len(closure), root)
	return nil
}
