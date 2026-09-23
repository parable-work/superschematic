package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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
	"github.com/parable-work/superschematic/schemadeps"
)

// buildAllFlags holds one build-all command's flag values.
type buildAllFlags struct {
	out        string
	profile    bool
	cache      bool
	cacheRoot  string
	parallel   bool
	isolatedTS bool
	skipFormat bool
	namingPath string
	depsCopy   string
}

func newBuildAllCmd(a *app) *cobra.Command {
	flags := &buildAllFlags{}
	cmd := &cobra.Command{
		Use:   "build-all <services-root>",
		Short: "Build all schema services in one process",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBuildAll(cmd, a, flags, args[0])
		},
	}
	cmd.Flags().StringVar(&flags.out, "out", "", "output root for generated artifacts (default <services-root>/../dist)")
	cmd.Flags().BoolVar(&flags.profile, "profile", false, "emit build phase timings to stderr")
	cmd.Flags().BoolVar(&flags.cache, "cache", false, "share schema outputs across worktrees via a content-addressed cache")
	cmd.Flags().StringVar(&flags.cacheRoot, "cache-root", "", "schema output cache root (default: SUPERSCHEMATIC_BUILD_CACHE_DIR, then [cache] root, then the XDG cache directory)")
	cmd.Flags().BoolVar(&flags.parallel, "parallel", false, "build independent schemas concurrently within each dependency phase")
	cmd.Flags().BoolVar(&flags.isolatedTS, "isolated-ts-programs", false, "use one TypeScript compiler program per schema service instead of the build-all shared program")
	cmd.Flags().BoolVar(&flags.skipFormat, "skip-format", false, "skip developer-friendly formatting for generated files")
	cmd.Flags().StringVar(&flags.namingPath, "naming", "", "naming config file (default <services-root>/../superschematic.toml)")
	cmd.Flags().StringVar(&flags.depsCopy, "deps-copy", "", "also write the dependency graph to this path (default: [deps] copy in the naming file, relative to the repository root)")
	return cmd
}

type buildAllTask struct {
	service    buildplan.Service
	inputHash  string
	outputRels []string
}

type sharedSchemaCache struct {
	mu      sync.Mutex
	schemas map[string]*ir.Schema
}

func newSharedSchemaCache() *sharedSchemaCache {
	return &sharedSchemaCache{schemas: make(map[string]*ir.Schema)}
}

func (c *sharedSchemaCache) get(name string) (*ir.Schema, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	schema, ok := c.schemas[name]
	return schema, ok
}

func (c *sharedSchemaCache) set(name string, schema *ir.Schema) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.schemas[name] = schema
}

func runBuildAll(cmd *cobra.Command, a *app, flags *buildAllFlags, servicesRootArg string) error {
	servicesRoot, err := filepath.Abs(servicesRootArg)
	if err != nil {
		return fmt.Errorf("resolving services root: %w", err)
	}
	if info, err := os.Stat(servicesRoot); err != nil || !info.IsDir() {
		return fmt.Errorf("services root not found: %s", servicesRoot)
	}

	outputRoot := flags.out
	if outputRoot == "" {
		outputRoot = filepath.Join(servicesRoot, "..", "dist")
	}
	outputRoot, err = filepath.Abs(outputRoot)
	if err != nil {
		return fmt.Errorf("resolving output root: %w", err)
	}
	repoRoot := filepath.Clean(filepath.Join(servicesRoot, "..", ".."))
	buildcache.SchemasDir = filepath.Base(filepath.Clean(filepath.Join(servicesRoot, "..")))

	// Resolved before any schema loads: the loader and generators take it as
	// an option; schemadeps (EmitFromDist, below) reads the active value.
	activeNaming, err := resolveNaming(flags.namingPath, filepath.Join(servicesRoot, ".."))
	if err != nil {
		return err
	}
	reg, err := a.resolveRegistry(activeNaming)
	if err != nil {
		return err
	}
	depsCopy := activeNaming.DepsCopyPath(repoRoot)
	if flags.depsCopy != "" {
		if depsCopy, err = filepath.Abs(flags.depsCopy); err != nil {
			return fmt.Errorf("resolving --deps-copy: %w", err)
		}
	}

	services, err := buildplan.DiscoverWith(servicesRoot, outputRoot, reg)
	if err != nil {
		return err
	}
	if len(services) == 0 {
		return fmt.Errorf("no schema services found")
	}

	names := make([]string, 0, len(services))
	for _, service := range services {
		names = append(names, service.Name)
	}
	mode := "sequential"
	if flags.parallel {
		mode = "parallel"
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Discovered %d schema services: %s (mode: %s)\n", len(services), strings.Join(names, ", "), mode)

	if err := sentinel.EnsureSiblings(servicesRoot, sentinel.Options{
		ReadTSConfig: func(servicePath string) (*schemaconfig.SchemaConfig, error) {
			return tsreader.ReadServiceConfig(servicePath, reg)
		},
		Registry: reg,
		Log:      cmd.OutOrStdout(),
	}); err != nil {
		return err
	}

	// The discovery pass doubles as the schema catalog: entity schema
	// references in deploy documents resolve against it without adding build-order edges.
	catalog := make(map[string]registry.SchemaCatalogEntry, len(services))
	for _, service := range services {
		catalog[service.Name] = registry.SchemaCatalogEntry{Kind: string(service.Config.Kind), AuthDB: service.Config.AuthDB}
	}
	loadOpts := []loader.Option{loader.WithSchemaCatalog(catalog), loader.WithNaming(activeNaming), loader.WithRegistry(reg)}

	cacheRoot := ""
	if flags.cache {
		cacheRoot = flags.cacheRoot
		if cacheRoot == "" {
			cacheRoot = buildcache.DefaultRoot(activeNaming.Cache.Root)
		}
	} else if err := os.RemoveAll(filepath.Join(outputRoot, ".build-stamps")); err != nil {
		return err
	}

	profileTotals := newProfileTotals()
	errWriter := cmd.ErrOrStderr()
	if flags.profile {
		errWriter = io.MultiWriter(cmd.ErrOrStderr(), profileTotals)
	}

	tasks, hasher, hashes, err := makeBuildAllTasks(services, repoRoot, cacheRoot, activeNaming)
	if err != nil {
		return err
	}

	resolved := make(map[string]bool)
	if cacheRoot != "" {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  Cache: %s\n", cacheRoot)
		for _, task := range tasks {
			ok, action := resolveBuildAllTask(task, cacheRoot, repoRoot, cmd.ErrOrStderr())
			if ok {
				resolved[task.service.Name] = true
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  OK: %s (%s)\n", task.service.Name, action)
			}
		}
	}

	remaining := make([]buildAllTask, 0, len(tasks))
	remainingServices := make([]buildplan.Service, 0, len(tasks))
	for _, task := range tasks {
		if resolved[task.service.Name] {
			continue
		}
		remaining = append(remaining, task)
		remainingServices = append(remainingServices, task.service)
		if cacheRoot != "" {
			for _, dir := range task.service.OutputDirs {
				if err := buildcache.CleanOutputDir(dir); err != nil {
					return fmt.Errorf("cleaning output dir for %s: %w", task.service.Name, err)
				}
			}
		}
	}
	if len(remaining) == 0 {
		if flags.profile {
			profileTotals.print(cmd.OutOrStdout())
		}
		if err := emitSchemaDeps(cmd, outputRoot, depsCopy, services); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\nAll %d schema services built successfully\n", len(services))
		return nil
	}

	taskByName := make(map[string]buildAllTask, len(remaining))
	for _, task := range remaining {
		taskByName[task.service.Name] = task
	}

	schemaCache := newSharedSchemaCache()
	serviceByName := make(map[string]buildplan.Service, len(services))
	var tsProgramCache *tsreader.ProgramCache
	for _, service := range services {
		serviceByName[service.Name] = service
	}
	if !flags.isolatedTS {
		tsServiceDirs := tsServiceDirectories(services)
		if len(tsServiceDirs) > 0 {
			tsProgramCache = tsreader.NewProgramCache(tsServiceDirs)
			defer tsProgramCache.Close()
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  Shared TypeScript program: %d service(s)\n", len(tsServiceDirs))
		}
	}

	if !flags.parallel {
		for _, task := range remaining {
			if err := executeBuildAllTask(cmd, task, buildAllTaskContext{
				outputRoot:     outputRoot,
				repoRoot:       repoRoot,
				cacheRoot:      cacheRoot,
				loadOpts:       loadOpts,
				schemaCache:    schemaCache,
				tsProgramCache: tsProgramCache,
				serviceByName:  serviceByName,
				profileWriter:  errWriter,
				profileEnabled: flags.profile,
				skipFormat:     flags.skipFormat,
				naming:         activeNaming,
				registry:       reg,
				hasher:         hasher,
				hashes:         hashes,
			}); err != nil {
				return err
			}
		}
	} else {
		phases, err := buildplan.GroupIntoPhases(remainingServices, resolved)
		if err != nil {
			return err
		}
		for i, phase := range phases {
			phaseNames := make([]string, 0, len(phase))
			for _, service := range phase {
				phaseNames = append(phaseNames, service.Name)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  Phase %d/%d: %s\n", i+1, len(phases), strings.Join(phaseNames, ", "))
			if err := executeBuildAllPhase(cmd, phase, taskByName, buildAllTaskContext{
				outputRoot:     outputRoot,
				repoRoot:       repoRoot,
				cacheRoot:      cacheRoot,
				loadOpts:       loadOpts,
				schemaCache:    schemaCache,
				tsProgramCache: tsProgramCache,
				serviceByName:  serviceByName,
				profileWriter:  errWriter,
				profileEnabled: flags.profile,
				skipFormat:     flags.skipFormat,
				naming:         activeNaming,
				registry:       reg,
				hasher:         hasher,
				hashes:         hashes,
			}); err != nil {
				return err
			}
		}
	}

	if flags.profile {
		profileTotals.print(cmd.OutOrStdout())
	}
	if err := emitSchemaDeps(cmd, outputRoot, depsCopy, services); err != nil {
		return err
	}
	// Every service has built, so hooks that need all of them at once (the
	// chart's merged values) run from here.
	hookContext := registry.BuildAllContext{
		ServiceNames: names,
		SchemaFor:    schemaCache.get,
		RepoRoot:     repoRoot,
		OutputRoot:   outputRoot,
		Naming:       activeNaming,
		Log:          cmd.OutOrStdout(),
	}
	if err := runBuildAllHooks(cmd.Context(), reg.BuildAllHooks(), hookContext); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\nAll %d schema services built successfully\n", len(services))
	return nil
}

// runBuildAllHooks runs the hooks in registration order and stops at the
// first failure. The error names the hook: once extensions register hooks,
// "creating <dir>: permission denied" alone does not say whose hook failed.
func runBuildAllHooks(ctx context.Context, hooks []registry.BuildAllHook, bc registry.BuildAllContext) error {
	for _, hook := range hooks {
		if err := hook.Run(ctx, bc); err != nil {
			return fmt.Errorf("build-all hook %s: %w", hook.Name, err)
		}
	}
	return nil
}

// emitSchemaDeps writes the package graph to <out>/.deps.json and, when
// copyPath is set, to that copy. Every discovered service's expected output
// directories say which service produced which package: that is each
// package's service field, and a package under no service's directory fails
// the build rather than entering the graph unowned.
func emitSchemaDeps(cmd *cobra.Command, outputRoot, copyPath string, services []buildplan.Service) error {
	producers := make(map[string]string)
	for _, service := range services {
		for _, dir := range service.OutputDirs {
			rel, err := filepath.Rel(outputRoot, dir)
			if err != nil {
				return err
			}
			producers[filepath.ToSlash(rel)] = service.Name
		}
	}
	if err := schemadeps.EmitFromDist(outputRoot, producers, copyPath); err != nil {
		return fmt.Errorf("emitting %s: %w", schemadeps.DepsFileName, err)
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  Wrote %s\n", schemadeps.DepsPath(outputRoot))
	if copyPath != "" {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  Wrote %s\n", copyPath)
	}
	return nil
}

func makeBuildAllTasks(services []buildplan.Service, repoRoot string, cacheRoot string, names naming.Naming) ([]buildAllTask, *buildcache.InputHasher, map[string]string, error) {
	hashes := map[string]string{}
	var hasher *buildcache.InputHasher
	if cacheRoot != "" {
		hasher = buildcache.NewInputHasher(services, repoRoot, names)
		hashes = hasher.HashAll(services)
	}
	tasks := make([]buildAllTask, 0, len(services))
	for _, service := range services {
		rels := make([]string, 0, len(service.OutputDirs))
		for _, dir := range service.OutputDirs {
			rel, err := filepath.Rel(repoRoot, dir)
			if err != nil {
				return nil, nil, nil, err
			}
			rels = append(rels, filepath.ToSlash(rel))
		}
		tasks = append(tasks, buildAllTask{
			service:    service,
			inputHash:  hashes[service.Name],
			outputRels: rels,
		})
	}
	return tasks, hasher, hashes, nil
}

func resolveBuildAllTask(task buildAllTask, cacheRoot string, repoRoot string, errOut io.Writer) (bool, string) {
	name := task.service.Name
	entry := buildcache.FindEntry(cacheRoot, "schemas", name, task.inputHash)
	if buildcache.ReadStamp(repoRoot, name) == task.inputHash && stampedOutputsExist(entry, repoRoot, task.service.OutputDirs) {
		action := "up to date"
		if entry == "" {
			if err := buildcache.StoreEntry(cacheRoot, "schemas", name, task.inputHash, repoRoot, task.outputRels); err != nil {
				_, _ = fmt.Fprintf(errOut, "    cache store failed for %s: %v\n", name, err)
			} else {
				action += ", cache backfilled"
			}
		}
		return true, action
	}

	if entry == "" {
		return false, ""
	}
	if _, err := buildcache.RestoreEntry(entry, repoRoot); err != nil {
		_, _ = fmt.Fprintf(errOut, "    cache restore failed for %s: %v; rebuilding\n", name, err)
		_ = buildcache.DropEntry(cacheRoot, "schemas", name, task.inputHash)
		return false, ""
	}
	if err := buildcache.WriteStamp(repoRoot, name, task.inputHash); err != nil {
		_, _ = fmt.Fprintf(errOut, "    stamp write failed for %s: %v; rebuilding\n", name, err)
		return false, ""
	}
	return true, "restored from cache"
}

func stampedOutputsExist(entry string, repoRoot string, expectedDirs []string) bool {
	if entry == "" {
		return outputDirsExist(expectedDirs)
	}
	rels, err := buildcache.EntryOutputRels(entry)
	if err != nil {
		return false
	}
	dirs := make([]string, 0, len(rels))
	for _, rel := range rels {
		dirs = append(dirs, filepath.Join(repoRoot, filepath.FromSlash(rel)))
	}
	return outputDirsExist(dirs)
}

func outputDirsExist(dirs []string) bool {
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return false
		}
		hasGeneratedOutput := false
		for _, entry := range entries {
			if entry.Name() != "node_modules" && entry.Name() != "target" {
				hasGeneratedOutput = true
				break
			}
		}
		if !hasGeneratedOutput {
			return false
		}
	}
	return true
}

type buildAllTaskContext struct {
	outputRoot     string
	repoRoot       string
	cacheRoot      string
	loadOpts       []loader.Option
	schemaCache    *sharedSchemaCache
	tsProgramCache *tsreader.ProgramCache
	serviceByName  map[string]buildplan.Service
	profileWriter  io.Writer
	profileEnabled bool
	skipFormat     bool
	naming         naming.Naming
	registry       *registry.Registry

	// hasher and hashes support the post-build input-hash recompute: the
	// build writes the authoring-import depfile, and the stored cache key
	// must reflect it (see buildcache/authoring.go). hashes is read-only
	// after construction, so concurrent phase tasks may share it.
	hasher *buildcache.InputHasher
	hashes map[string]string
}

func executeBuildAllPhase(cmd *cobra.Command, phase []buildplan.Service, taskByName map[string]buildAllTask, ctx buildAllTaskContext) error {
	var wg sync.WaitGroup
	errCh := make(chan error, len(phase))
	for _, service := range phase {
		task := taskByName[service.Name]
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := executeBuildAllTask(cmd, task, ctx); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			return err
		}
	}
	return nil
}

func executeBuildAllTask(cmd *cobra.Command, task buildAllTask, ctx buildAllTaskContext) error {
	service := task.service
	var prof *profile.Profiler
	if ctx.profileEnabled {
		prof = profile.New(service.Name, ctx.profileWriter)
	}
	result, err := buildService(buildServiceOptions{
		ServicePath: service.Dir,
		OutputRoot:  ctx.outputRoot,
		Paths:       ctx.naming.LocalPaths(ctx.repoRoot),
		LoadOptions: buildAllTaskLoadOptions(ctx),
		LoadDependency: func(name string) (*ir.Schema, error) {
			return loadBuildAllDependency(name, ctx, prof)
		},
		Log:        cmd.OutOrStdout(),
		Profile:    prof,
		SkipFormat: ctx.skipFormat,
		Naming:     ctx.naming,
		Registry:   ctx.registry,
	})
	if err != nil {
		return fmt.Errorf("%s: %w", service.Name, err)
	}
	ctx.schemaCache.set(service.Name, result.Schema)
	if ctx.cacheRoot != "" {
		// The build wrote the authoring-import depfile; the stored key and
		// stamp must include it (see buildcache/authoring.go). Storing under
		// the pre-build hash would let a worktree with different import
		// contents false-hit this entry.
		inputHash := task.inputHash
		if ctx.hasher != nil {
			inputHash = ctx.hasher.Recompute(service, ctx.hashes)
		}
		if err := buildcache.StoreEntry(ctx.cacheRoot, "schemas", service.Name, inputHash, ctx.repoRoot, task.outputRels); err != nil {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "    cache store failed for %s: %v\n", service.Name, err)
		}
		if err := buildcache.PruneEntries(ctx.cacheRoot, "schemas", service.Name, 5); err != nil {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "    cache prune failed for %s: %v\n", service.Name, err)
		}
		if err := buildcache.WriteStamp(ctx.repoRoot, service.Name, inputHash); err != nil {
			return fmt.Errorf("%s: write stamp: %w", service.Name, err)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  OK: %s (built, cached)\n", service.Name)
		return nil
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  OK: %s (built)\n", service.Name)
	return nil
}

func loadBuildAllDependency(name string, ctx buildAllTaskContext, prof *profile.Profiler) (*ir.Schema, error) {
	if schema, ok := ctx.schemaCache.get(name); ok {
		return schema, nil
	}
	service, ok := ctx.serviceByName[name]
	if !ok {
		return nil, fmt.Errorf("schema service %s not found", name)
	}
	loadOpts := append([]loader.Option(nil), ctx.loadOpts...)
	if ctx.tsProgramCache != nil {
		loadOpts = append(loadOpts, loader.WithTSProgramCache(ctx.tsProgramCache))
	}
	loadOpts = append(loadOpts, loader.WithProfiler(prof))
	schema, _, err := loader.LoadServiceWithConfig(service.Dir, loadOpts...)
	if err != nil {
		return nil, err
	}
	ctx.schemaCache.set(name, schema)
	return schema, nil
}

func buildAllTaskLoadOptions(ctx buildAllTaskContext) []loader.Option {
	loadOpts := append([]loader.Option(nil), ctx.loadOpts...)
	if ctx.tsProgramCache != nil {
		loadOpts = append(loadOpts, loader.WithTSProgramCache(ctx.tsProgramCache))
	}
	return loadOpts
}

func tsServiceDirectories(services []buildplan.Service) []string {
	var dirs []string
	for _, service := range services {
		if filepath.Base(service.ConfigPath) == "schema.config.ts" {
			dirs = append(dirs, service.Dir)
		}
	}
	sort.Strings(dirs)
	return dirs
}

type profileTotals struct {
	mu     sync.Mutex
	totals map[string]profileTotal
}

type profileTotal struct {
	duration int
	count    int
}

func newProfileTotals() *profileTotals {
	return &profileTotals{totals: make(map[string]profileTotal)}
}

func (p *profileTotals) Write(data []byte) (int, error) {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) != 4 || parts[0] != "superschematic-profile" {
			continue
		}
		phase := strings.TrimPrefix(parts[2], "phase=")
		durationText := strings.TrimPrefix(parts[3], "duration_ms=")
		duration, err := strconv.Atoi(durationText)
		if err != nil || phase == parts[2] {
			continue
		}
		p.mu.Lock()
		total := p.totals[phase]
		total.duration += duration
		total.count++
		p.totals[phase] = total
		p.mu.Unlock()
	}
	return len(data), nil
}

func (p *profileTotals) print(w io.Writer) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.totals) == 0 {
		return
	}
	type item struct {
		phase string
		total profileTotal
	}
	items := make([]item, 0, len(p.totals))
	for phase, total := range p.totals {
		items = append(items, item{phase: phase, total: total})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].total.duration == items[j].total.duration {
			return items[i].phase < items[j].phase
		}
		return items[i].total.duration > items[j].total.duration
	})
	_, _ = fmt.Fprintln(w, "\nProfile summary (top cumulative phases):")
	limit := 15
	if len(items) < limit {
		limit = len(items)
	}
	for _, item := range items[:limit] {
		_, _ = fmt.Fprintf(w, "  %s: %dms across %d samples\n", item.phase, item.total.duration, item.total.count)
	}
}
