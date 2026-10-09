package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/parable-work/superschematic/internal/buildplan"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/stackgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/sqlmigrate"
	"github.com/parable-work/superschematic/internal/stack"
	"github.com/parable-work/superschematic/internal/stack/local"
	ir "github.com/parable-work/superschematic/ir"
)

// stackCommands are the constructors of the `stack` group's subcommands
// (docs/stack-model.md, section 11.1). A file of this package adds its own
// with one call, in an init function:
//
//	func init() { registerStackCommands(newStackPlanCmd, newStackDeployCmd) }
//
// Targets and provisioners add no commands: they plug into these.
var stackCommands []func(*app) *cobra.Command

// registerStackCommands adds subcommands to the `stack` group.
func registerStackCommands(constructors ...func(*app) *cobra.Command) {
	stackCommands = append(stackCommands, constructors...)
}

func init() { registerStackCommands(newStackDevCmd) }

// newStackCmd is the `stack` group: the commands that run and deploy a
// stack's environments.
func newStackCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stack",
		Short: "Run and deploy the environments of a stack",
		Long: `The stack commands run and deploy the environments a Stack service declares
(docs/stack-model.md). Each builds the stack and the services it reaches, reads
the environment the build resolved from <output-root>/stack/<stack>/<environment>/
environment.json, and applies it through the provisioner its target names.`,
	}
	for _, newCmd := range stackCommands {
		cmd.AddCommand(newCmd(a))
	}
	return cmd
}

// stackProject is a stack service and the schemas root around it, as the
// stack commands open it: its naming, its registry, and every service
// discovery finds beside it.
type stackProject struct {
	// dir is the stack service's directory; servicesRoot its parent, and
	// schemasRoot the parent of that.
	dir          string
	servicesRoot string
	schemasRoot  string
	// outputRoot is where the build writes, --out or <schemas-root>/dist.
	outputRoot string

	names    naming.Naming
	reg      *registry.Registry
	services []buildplan.Service
	// stack is the stack service; its name is the stack's.
	stack buildplan.Service
}

// defaultServicesRoot is where a stack command looks for the stack service
// when it is given none.
const defaultServicesRoot = "schemas/services"

// openStackProject opens the stack service at dir, or, when dir is empty,
// the one Stack service of the working directory or of ./schemas/services.
func openStackProject(cmd *cobra.Command, a *app, dir, out, namingPath string) (*stackProject, error) {
	if dir == "" {
		found, err := findStackService(a, namingPath)
		if err != nil {
			return nil, err
		}
		dir = found
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("stack service directory not found: %s", dir)
	}
	p := &stackProject{dir: abs, servicesRoot: filepath.Dir(abs), schemasRoot: filepath.Dir(filepath.Dir(abs))}
	if p.names, err = resolveNaming(namingPath, p.schemasRoot); err != nil {
		return nil, err
	}
	if p.reg, err = a.resolveRegistry(p.names); err != nil {
		return nil, err
	}
	p.outputRoot = out
	if p.outputRoot == "" {
		p.outputRoot = filepath.Join(p.schemasRoot, "dist")
	}
	if p.outputRoot, err = filepath.Abs(p.outputRoot); err != nil {
		return nil, err
	}
	// A config may import a sibling's sentinel (D34), and discovery reads
	// every config.
	if err := buildplan.EnsureSentinels(p.servicesRoot, p.reg, cmd.OutOrStdout()); err != nil {
		return nil, err
	}
	if p.services, err = buildplan.DiscoverWith(p.servicesRoot, p.outputRoot, p.reg); err != nil {
		return nil, err
	}
	for _, service := range p.services {
		if filepath.Clean(service.Dir) == abs {
			p.stack = service
		}
	}
	switch {
	case p.stack.Name == "":
		return nil, fmt.Errorf("%s is not a schema service under %s", abs, p.servicesRoot)
	case p.stack.Config.Kind != ir.SchemaKindStack:
		return nil, fmt.Errorf("%s is a %s service; the stack commands take a %s service", p.stack.Name, p.stack.Config.Kind, ir.SchemaKindStack)
	}
	return p, nil
}

// findStackService finds the stack service a stack command runs on when it
// names none: the working directory when it is a Stack service, else the
// one Stack service under ./schemas/services.
func findStackService(a *app, namingPath string) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	open := func(schemasRoot string) (*registry.Registry, error) {
		names, err := resolveNaming(namingPath, schemasRoot)
		if err != nil {
			return nil, err
		}
		return a.resolveRegistry(names)
	}
	if reg, err := open(filepath.Dir(filepath.Dir(cwd))); err == nil {
		if cfg, err := buildplan.ReadConfig(cwd, reg); err == nil && cfg.Kind == ir.SchemaKindStack {
			return cwd, nil
		}
	}
	servicesRoot := filepath.Join(cwd, defaultServicesRoot)
	if info, err := os.Stat(servicesRoot); err != nil || !info.IsDir() {
		return "", fmt.Errorf("the working directory is not a %s service and has no %s; name the stack service directory", ir.SchemaKindStack, defaultServicesRoot)
	}
	reg, err := open(filepath.Dir(servicesRoot))
	if err != nil {
		return "", err
	}
	services, err := buildplan.DiscoverWith(servicesRoot, filepath.Join(filepath.Dir(servicesRoot), "dist"), reg)
	if err != nil {
		return "", err
	}
	var stacks []string
	for _, service := range services {
		if service.Config.Kind == ir.SchemaKindStack {
			stacks = append(stacks, service.Dir)
		}
	}
	switch len(stacks) {
	case 1:
		return stacks[0], nil
	case 0:
		return "", fmt.Errorf("no %s service under %s; name the stack service directory", ir.SchemaKindStack, servicesRoot)
	}
	return "", fmt.Errorf("several %s services under %s (%s); name the one to run", ir.SchemaKindStack, servicesRoot, strings.Join(stacks, ", "))
}

// build builds the stack service and every service it reaches, each with
// its dependencies, as build --with-deps builds one service: the stack's
// references (its deploy and expose entries, its declared deployables'
// services, its settings' handles) and their closures. The stack builds
// last, after every service whose IR and outputs its environments and
// server entrypoints read.
func (p *stackProject) build(cmd *cobra.Command) error {
	catalog := make(map[string]registry.SchemaCatalogEntry, len(p.services))
	for _, service := range p.services {
		catalog[service.Name] = registry.SchemaCatalogEntry{Kind: string(service.Config.Kind), AuthDB: service.Config.AuthDB}
	}
	schema, err := loader.LoadService(p.dir, loader.WithSchemaCatalog(catalog), loader.WithNaming(p.names), loader.WithRegistry(p.reg))
	if err != nil {
		return err
	}
	roots := []string{p.stack.Name}
	for _, ref := range ir.StackReferences(schema) {
		if !slices.Contains(roots, ref.Name) {
			roots = append(roots, ref.Name)
		}
	}
	reached := map[string]bool{}
	for _, root := range roots {
		closure, err := buildplan.Closure(p.services, root)
		if err != nil {
			return err
		}
		for _, service := range closure {
			reached[service.Name] = true
		}
	}
	var closure []buildplan.Service
	for _, service := range p.services {
		if reached[service.Name] && service.Name != p.stack.Name {
			closure = append(closure, service)
		}
	}
	closure = append(closure, p.stack)
	return buildClosure(cmd, p.reg, p.names, p.services, closure, p.stack.Name, closureBuild{
		outputRoot:  p.outputRoot,
		schemasRoot: p.schemasRoot,
	})
}

// environments reads every environment the build resolved for the stack,
// in name order.
func (p *stackProject) environments() ([]*ir.ResolvedEnvironment, error) {
	dir := stackgen.OutDir(p.outputRoot, p.stack.Name)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("stack %s: no resolved environments in %s; build the stack first: %w", p.stack.Name, dir, err)
	}
	var envs []*ir.ResolvedEnvironment
	for _, entry := range entries {
		if entry.IsDir() {
			env, err := p.environment(entry.Name())
			if err != nil {
				return nil, err
			}
			envs = append(envs, env)
		}
	}
	return envs, nil
}

// environment reads one environment the build resolved for the stack.
func (p *stackProject) environment(name string) (*ir.ResolvedEnvironment, error) {
	path := stack.EnvironmentPath(p.outputRoot, p.stack.Name, name)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("stack %s has no environment %s (no %s)", p.stack.Name, name, path)
	}
	if err != nil {
		return nil, err
	}
	return stack.Unmarshal(data)
}

// programDir is where a provisioner renders an environment's program:
// `<output-root>/program/<stack>/<environment>`.
func programDir(outputRoot, stackName, environment string) string {
	return filepath.Join(outputRoot, "program", stackName, environment)
}

// stackDevFlags holds `stack dev`'s flag values.
type stackDevFlags struct {
	environment    string
	out            string
	namingPath     string
	removeDatabase bool
}

func newStackDevCmd(a *app) *cobra.Command {
	flags := &stackDevFlags{}
	cmd := &cobra.Command{
		Use:   "dev [<stack-service-dir>]",
		Short: "Run a local environment of a stack until Ctrl-C",
		Long: `Dev builds the stack service and every service it reaches, each with its
dependencies, then runs the stack's environment on the local target
(docs/stack-model.md, section 8.3): a Postgres container with a database per
DB schema, each migrated to the schema's model with superschematic-migrate,
and each server built from its entrypoint module at
<output-root>/server/<stack>/<server> and run as a process with its resolved
config, callees first, each waited on until it answers /readyz. Each job is
built from its entrypoint module the same way and runs on its schedule,
never two runs of one job at once, until Ctrl-C; superschematic stack run
runs one once, from another terminal. Every process's output is printed
with its name in front. Dev stays in the foreground until Ctrl-C, then
stops the servers and the container, which keeps its data for the next
run.

The environment is --environment, or the stack's one environment on the local
target. A secret a server reads comes from
<schemas-root>/.superschematic/local/<stack>/<environment>/secrets.env, a line
<Type>.<FIELD>=<value> per secret; the .superschematic directory ignores
itself in git, and no secret reaches the output root. The keys that sign the
calls between servers are generated beside it.

Without a directory, dev runs the working directory when it is a Stack
service, else the one Stack service under ./schemas/services.

Examples:
  superschematic stack dev ./schemas/services/shop-stack
  superschematic stack dev --environment Dev --remove-database`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := ""
			if len(args) == 1 {
				dir = args[0]
			}
			return runStackDev(cmd, a, flags, dir)
		},
	}
	cmd.Flags().StringVarP(&flags.environment, "environment", "e", "", "the environment to run (default the stack's one environment on the local target)")
	cmd.Flags().StringVar(&flags.out, "out", "", "output root for generated artifacts (default <schemas-root>/dist)")
	cmd.Flags().StringVar(&flags.namingPath, "naming", "", "naming config file (default <stack-service-dir>/../../superschematic.toml)")
	cmd.Flags().BoolVar(&flags.removeDatabase, "remove-database", false, "on exit, remove the Postgres container and its data instead of stopping it")
	return cmd
}

// localEnvironment picks the environment stack dev runs: the one named, or
// the stack's one environment on the local target.
func (p *stackProject) localEnvironment(name string) (*ir.ResolvedEnvironment, error) {
	if name != "" {
		env, err := p.environment(name)
		if err != nil {
			return nil, err
		}
		if env.Target != local.Target {
			return nil, fmt.Errorf("environment %s of stack %s is on target %s; stack dev runs an environment on the %s target", name, p.stack.Name, env.Target, local.Target)
		}
		return env, nil
	}
	envs, err := p.environments()
	if err != nil {
		return nil, err
	}
	var found []*ir.ResolvedEnvironment
	var names []string
	for _, env := range envs {
		if env.Target == local.Target {
			found = append(found, env)
			names = append(names, env.Environment)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return nil, fmt.Errorf("stack %s has no environment on the %s target; declare one, @environment({ target: %q })", p.stack.Name, local.Target, local.Target)
	}
	return nil, fmt.Errorf("stack %s has several environments on the %s target (%s); pick one with --environment", p.stack.Name, local.Target, strings.Join(names, ", "))
}

// writeModels writes the model of each DB schema a database of env hosts
// into the program's models directory, as the local provisioner reads
// them: what each migration plans to.
func (p *stackProject) writeModels(env *ir.ResolvedEnvironment, dir string) error {
	modelsDir := filepath.Join(dir, local.ModelsDir)
	if err := os.RemoveAll(modelsDir); err != nil {
		return err
	}
	var hosted []*ir.ResolvedDeployable
	for _, d := range env.Deployables {
		if d.Kind == ir.DeployableDatabase {
			hosted = append(hosted, d)
		}
	}
	if len(hosted) == 0 {
		return nil
	}
	version, err := openSchemaVersion(p.servicesRoot, p.names, p.reg)
	if err != nil {
		return err
	}
	defer version.close()
	if err := os.MkdirAll(modelsDir, 0o755); err != nil {
		return err
	}
	for _, d := range hosted {
		for _, svc := range d.Services {
			model, err := version.model(svc.Name, sqlmigrate.Dialect(d.Dialect))
			if err != nil {
				return fmt.Errorf("the model of %s: %w", svc.Name, err)
			}
			data, err := model.CanonicalJSON()
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(modelsDir, svc.Name+".json"), append(data, '\n'), 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

// The local provisioner's optional methods stack dev uses: where it
// writes, the wait on its servers, and the removal of the container.
type (
	outputSetter interface{ SetOutput(io.Writer) }
	waiter       interface {
		Wait(context.Context, registry.ProvisionRequest) error
	}
	purger interface {
		Purge(context.Context, registry.ProvisionRequest) error
	}
)

// cleanupTimeout bounds stopping an environment after Ctrl-C or a failure.
const cleanupTimeout = 2 * time.Minute

func runStackDev(cmd *cobra.Command, a *app, flags *stackDevFlags, dir string) error {
	p, err := openStackProject(cmd, a, dir, flags.out, flags.namingPath)
	if err != nil {
		return err
	}
	if err := p.build(cmd); err != nil {
		return err
	}
	env, err := p.localEnvironment(flags.environment)
	if err != nil {
		return err
	}
	spec, ok := p.reg.Provisioner(env.Provisioner)
	if !ok {
		return fmt.Errorf("environment %s names provisioner %q, which is not registered", env.Environment, env.Provisioner)
	}
	program := programDir(p.outputRoot, env.Stack, env.Environment)
	if err := p.writeModels(env, program); err != nil {
		return err
	}
	stateDir, err := local.EnsureStateDir(p.schemasRoot, env.Stack, env.Environment)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	prov := spec.Provisioner
	if s, ok := prov.(outputSetter); ok {
		s.SetOutput(out)
	}
	req := registry.ProvisionRequest{
		Environment:    env,
		Dir:            program,
		OutputRoot:     p.outputRoot,
		RepositoryRoot: filepath.Dir(p.schemasRoot),
		Backend:        local.StateBackend(stateDir),
	}
	if err := prov.Render(env, program); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "\nRunning stack %s environment %s (program %s)\n", env.Stack, env.Environment, program)

	// From here on Ctrl-C stops what runs, rather than the command.
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cleanup := func() error {
		cctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		if pr, ok := prov.(purger); ok && flags.removeDatabase {
			return pr.Purge(cctx, req)
		}
		return prov.Destroy(cctx, req)
	}
	for _, step := range env.DeployOrder {
		if err := prov.Apply(ctx, req, *step); err != nil {
			return errors.Join(err, cleanup())
		}
	}
	printLocalSummary(out, env, stateDir)

	var runErr error
	if w, ok := prov.(waiter); ok {
		runErr = w.Wait(ctx, req)
	} else {
		<-ctx.Done()
	}
	_, _ = fmt.Fprintf(out, "\nStopping stack %s environment %s\n", env.Stack, env.Environment)
	return errors.Join(runErr, cleanup())
}

// printLocalSummary prints where each server, site and database of a
// running local environment is reached, and when each job runs.
func printLocalSummary(w io.Writer, env *ir.ResolvedEnvironment, stateDir string) {
	var lines []string
	for _, d := range env.Deployables {
		switch {
		case d.Kind == ir.DeployableServer:
			if address, ok := d.Address.(string); ok {
				lines = append(lines, fmt.Sprintf("  server   %-24s %s", d.Name, address))
			}
		case d.Kind == ir.DeployableJob && d.Job != nil && d.Job.Schedule != "":
			lines = append(lines, fmt.Sprintf("  job      %-24s on %s (%s)", d.Name, d.Job.Schedule, d.Job.TimeZone))
		case d.Kind == ir.DeployableJob:
			lines = append(lines, fmt.Sprintf("  job      %-24s on demand: superschematic stack run %s %s", d.Name, env.Environment, d.Name))
		case d.Kind == ir.DeployableSite:
			if address, ok := d.PublicAddress.(string); ok {
				lines = append(lines, fmt.Sprintf("  site     %-24s %s", d.Name, address))
			}
		}
	}
	for _, res := range env.Resources.Resources {
		if res.Type == local.TypeDatabase {
			lines = append(lines, fmt.Sprintf("  database %-24s %v", res.Properties["service"], res.Properties["url"]))
		}
	}
	sort.Strings(lines)
	_, _ = fmt.Fprintf(w, "\nStack %s environment %s is running:\n%s\n  state    %s\nPress Ctrl-C to stop.\n",
		env.Stack, env.Environment, strings.Join(lines, "\n"), stateDir)
}
