package generator

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/goutil"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/generator/stackgen"
	"github.com/parable-work/superschematic/internal/generator/tsgen"
	"github.com/parable-work/superschematic/internal/generator/tsrestgen"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/stack"
	ir "github.com/parable-work/superschematic/ir"
)

// serverGenerator is the name of the Stack kind's generator of server
// entrypoints.
const serverGenerator = "server"

// generateServers writes the entrypoint of each Go and TypeScript server of
// the stack the schema declares under servergen.StackDir (docs/stack-model.md,
// sections 8.1, 8.2 and 8.6), and of each Go job beside them (section 8.7,
// D52), and scaffolds each served API's implementation that is missing, a
// Go server's in Go and a TypeScript server's in TypeScript (sections 8.5
// and 8.6). No environment changes the servers or the jobs, but the
// entrypoint of one that some environment connects to a database on Cloud
// SQL links the Cloud SQL connector, or depends on the Node one. It plans
// every entrypoint before it writes anything, so one it refuses leaves the
// last build's entrypoints and every implementation as they were. A build
// without a repository root writes none: the implementations live under it.
func (r run) generateServers() error {
	if r.Options.RepositoryRoot == "" {
		r.Skip(serverGenerator)
		return nil
	}
	st := ir.StackOf(r.Schema)
	if st == nil {
		return fmt.Errorf("stack %s: no class declares @stack", r.Config.Name)
	}
	services, err := stackgen.Services(r.GenerateContext, st)
	if err != nil {
		return err
	}
	servers, err := stack.Servers(stack.Input{Stack: st, Services: services})
	if err != nil {
		return err
	}
	cloudSQL, err := cloudSQLDatabases(r.GenerateContext, st, services)
	if err != nil {
		return err
	}

	type planned struct {
		server   *servergen.Server
		scaffold []scaffold
	}
	var plans []planned
	var tsPlans []*servergen.TypeScriptServer
	scaffolding := map[string]bool{}
	var tsScaffolds []*tsrestgen.APIOutput
	for _, s := range servers {
		switch s.Language {
		case APILanguageGo:
			server, scaffolds, err := r.planEntrypoint(st.Name, s, cloudSQL[s.Name])
			if err != nil {
				return err
			}
			plans = append(plans, planned{server, scaffolds})
			for _, sc := range scaffolds {
				scaffolding[sc.output.SchemaName] = true
			}
		case APILanguageTypeScript:
			server, scaffolds, err := r.planTypeScriptServer(st.Name, s, cloudSQL[s.Name])
			if err != nil {
				return err
			}
			tsPlans = append(tsPlans, server)
			tsScaffolds = append(tsScaffolds, scaffolds...)
		default:
			r.Logf("  - server %s: a %s server, which gets no generated entrypoint yet\n", s.Name, s.Language)
		}
	}
	jobs, err := stack.Jobs(stack.Input{Stack: st, Services: services})
	if err != nil {
		return err
	}
	if err := r.checkJobs(st.Name, jobs, services, scaffolding); err != nil {
		return err
	}
	for _, j := range jobs {
		if j.Language != APILanguageGo {
			r.Logf("  - job %s: a %s job, which gets no generated entrypoint yet\n", j.Name, j.Language)
			continue
		}
		// The servers' plans scaffold the job's API, which a server serves.
		job, _, err := r.planEntrypoint(st.Name, j, cloudSQL[j.Name])
		if err != nil {
			return err
		}
		plans = append(plans, planned{server: job})
	}

	for _, p := range plans {
		for _, sc := range p.scaffold {
			if err := sc.write(r); err != nil {
				return err
			}
		}
	}
	for _, output := range tsScaffolds {
		if err := r.scaffoldTypeScriptImplementation(output, r.Options.RepositoryRoot); err != nil {
			return err
		}
	}
	if len(tsPlans) > 0 {
		// The servers and the implementations join the output root's Bun
		// workspace.
		if err := r.writeTypeScriptWorkspace(); err != nil {
			return fmt.Errorf("stack %s: %w", st.Name, err)
		}
		r.noteIgnoredLockfile()
	}
	dir := servergen.StackDir(r.Options.OutputRoot, st.Name)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("stack %s: %w", st.Name, err)
	}
	if len(plans) == 0 && len(tsPlans) == 0 {
		r.Skip(serverGenerator)
		return nil
	}
	for _, p := range plans {
		if err := servergen.Write(p.server, servergen.ServerDir(r.Options.OutputRoot, st.Name, p.server.Name)); err != nil {
			return err
		}
		if p.server.Docker == nil {
			r.Logf("  - %s %s: no Dockerfile, since %s\n", p.server.Kind, p.server.Name, p.server.NoDocker)
		}
	}
	for _, server := range tsPlans {
		if err := servergen.WriteTypeScript(server, servergen.ServerDir(r.Options.OutputRoot, st.Name, server.Name)); err != nil {
			return err
		}
		if server.Docker == nil {
			r.Logf("  - server %s: no Dockerfile, since %s\n", server.Name, server.NoDocker)
		}
	}
	r.Done(serverGenerator, dir)
	return nil
}

// noteIgnoredLockfile says, in one line, how to commit the lockfile of the
// output root's Bun workspace when git ignores it (D51, amended): a
// server's image and the generated CI install the versions it pins only
// when the project commits it. The build leaves the project's ignore
// rules as they are.
func (r run) noteIgnoredLockfile() {
	rule, ignored := tsgen.IgnoredLockfile(r.Options.OutputRoot)
	if !ignored {
		return
	}
	by := "git"
	if rule != "" {
		by = rule
	}
	r.Logf("  - %s is ignored by %s; commit it, so images and CI install the TypeScript versions it pins: ignore the output root's contents, not the directory (dist/* and !dist/%s in place of dist/; docs/stack-model.md, section 8.6)\n",
		filepath.Join(r.Options.OutputRoot, tsgen.LockfileName), by, tsgen.LockfileName)
}

// checkJobs refuses a stack one of whose Go APIs declares jobs while its
// implementation declares no NewJobs (D52). The implementation exists, so
// it is the engineer's, and the build writes into it no more: an API that
// predates its jobs fails here, saying what to add, rather than in the
// compile of a job's entrypoint. An API whose scaffold this build writes,
// scaffolding, gets NewJobs from it.
func (r run) checkJobs(stackName string, jobs []*ir.ResolvedDeployable, services []stack.Service, scaffolding map[string]bool) error {
	checked := map[string]bool{}
	for _, job := range jobs {
		api := job.Job.API
		if checked[api] || scaffolding[api] || job.Language != APILanguageGo {
			continue
		}
		checked[api] = true
		impl, _, err := r.implementation(api)
		if err != nil {
			return err
		}
		exists, err := apigen.ImplementationExists(impl.Dir)
		if err != nil || !exists {
			return err
		}
		declares, err := apigen.DeclaresFunc(impl.Dir, apigen.JobsFunc)
		if err != nil || declares {
			return err
		}
		var methods []string
		for _, svc := range services {
			if svc.Name != api {
				continue
			}
			for _, j := range svc.Jobs {
				methods = append(methods, fmt.Sprintf("\t%s(ctx context.Context) error", goutil.GoPublicIdentifier(j.Name)))
			}
		}
		return fmt.Errorf("stack %s: %s declares jobs, and its implementation at %s, which the build no longer writes into, declares no %s; add\n\n"+
			"\tfunc %s(deps api.Deps) (api.Jobs, error)\n\n"+
			"returning a value with a method per job:\n\n%s\n\n"+
			"where api is %s, whose api.JobsConstructor is %s's signature (docs/stack-model.md, section 8.7)",
			stackName, api, impl.Dir, apigen.JobsFunc, apigen.JobsFunc, strings.Join(methods, "\n"), r.Options.Naming.GoAPIModule(api), apigen.JobsFunc)
	}
	return nil
}

// scaffold is an implementation a server's build writes because it is
// missing, and the module it writes beside it when no go.mod holds it.
type scaffold struct {
	output    *apigen.APIOutput
	impl      servergen.Implementation
	newModule bool
	modules   []servergen.Module
}

func (sc scaffold) write(r run) error {
	written, err := apigen.WriteImplementationScaffold(sc.output, sc.impl.Dir)
	if err != nil {
		return fmt.Errorf("generator: implementation scaffold for %s: %w", sc.output.SchemaName, err)
	}
	if !written {
		return nil
	}
	r.Logf("  + implementation scaffold of %s written to %s\n", sc.output.SchemaName, sc.impl.Dir)
	if sc.newModule {
		if err := servergen.WriteImplementationModule(r.Options.Naming, sc.output.SchemaName, sc.impl, sc.output.ModulePath, sc.modules); err != nil {
			return err
		}
	}
	return nil
}

// planEntrypoint plans the entrypoint of deployable s of the stack, a
// server or a job: the Go server output of each API it serves, or of its
// job's API, read as that API's own build reads it, where each
// implementation lives, and every module the build needs. cloudSQL are the
// DB services some environment connects it to on Cloud SQL. It returns the
// implementations that are missing, which the caller scaffolds.
func (r run) planEntrypoint(stackName string, s *ir.ResolvedDeployable, cloudSQL []string) (*servergen.Server, []scaffold, error) {
	in := servergen.Input{
		Stack:          stackName,
		Server:         s.Name,
		Dir:            servergen.ServerDir(r.Options.OutputRoot, stackName, s.Name),
		Naming:         r.Options.Naming,
		RepositoryRoot: r.Options.Naming.BuildContext(r.Options.RepositoryRoot),
		ScalarGo:       r.Options.Paths.ScalarGo,
		Release:        r.Options.ReleaseInfo(),
		CloudSQL:       cloudSQL,
	}
	if s.Job != nil {
		in.Job = &servergen.JobInput{Name: s.Job.Name}
	}
	var scaffolds []scaffold
	var versionGraph bool
	var modules []servergen.Module
	for _, ref := range s.Services {
		output, err := r.servedAPI(stackName, s.Name, ref.Name)
		if err != nil {
			return nil, nil, err
		}
		apiModules := r.goServerModules(output, s.Job != nil)
		impl, newModule, err := r.implementation(ref.Name)
		if err != nil {
			return nil, nil, err
		}
		exists, err := apigen.ImplementationExists(impl.Dir)
		if err != nil {
			return nil, nil, err
		}
		if newModule && exists {
			return nil, nil, fmt.Errorf("stack %s: server %s serves %s, whose implementation %s is in no Go module; add a go.mod at it or above it, under %s", stackName, s.Name, ref.Name, impl.Dir, r.Options.RepositoryRoot)
		}
		if !exists {
			scaffolds = append(scaffolds, scaffold{output: output, impl: impl, newModule: newModule, modules: apiModules})
		}
		if (output.IsPublic && output.UpstreamVersionGraph) || output.Deps.VersionGraph {
			versionGraph = true
		}
		in.APIs = append(in.APIs, servergen.APIInput{Output: output, Implementation: impl})
		modules = append(modules, apiModules...)
		modules = append(modules, servergen.Module{Path: impl.Module, Dir: impl.ModuleDir, Direct: true})
	}
	in.Modules = modules
	if versionGraph {
		in.VersionGraphGo = r.Options.Paths.VersionGraphGo
	}
	server, err := servergen.Plan(in)
	if err != nil {
		return nil, nil, err
	}
	return server, scaffolds, nil
}

// planTypeScriptServer plans the entrypoint of the TypeScript server s of
// the stack (D51): the TypeScript API package of each API it serves, as
// that API's own build writes it, its EnvConfig, and where each
// implementation lives, at the naming file's [implementation_paths]
// typescript template. cloudSQL are the DB services some environment
// connects it to on Cloud SQL. It returns the APIs whose implementation is
// missing, which the caller scaffolds.
func (r run) planTypeScriptServer(stackName string, s *ir.ResolvedDeployable, cloudSQL []string) (*servergen.TypeScriptServer, []*tsrestgen.APIOutput, error) {
	out := r.Options.OutputRoot
	in := servergen.TypeScriptInput{
		Stack:              stackName,
		Server:             s.Name,
		Dir:                servergen.ServerDir(out, stackName, s.Name),
		OutputRoot:         out,
		Naming:             r.Options.Naming,
		RepositoryRoot:     r.Options.Naming.BuildContext(r.Options.RepositoryRoot),
		ImplementationRoot: r.Options.RepositoryRoot,
		PackageDirs:        map[string]string{},
		Paths:              r.Options.Paths,
		CloudSQL:           cloudSQL,
	}
	var scaffolds []*tsrestgen.APIOutput
	for _, ref := range s.Services {
		served, err := r.servedRun(stackName, s.Name, ref.Name)
		if err != nil {
			return nil, nil, err
		}
		output, config, err := served.typeScriptAPI()
		if err != nil {
			return nil, nil, err
		}
		if output == nil {
			return nil, nil, fmt.Errorf("stack %s: server %s serves %s, which declares no operations", stackName, s.Name, ref.Name)
		}
		routes, err := served.APIOutput()
		if err != nil {
			return nil, nil, err
		}
		dir := r.Options.Naming.TypeScriptImplementationDir(r.Options.RepositoryRoot, ref.Name)
		exists, err := tsrestgen.ImplementationExists(dir)
		if err != nil {
			return nil, nil, err
		}
		if !exists {
			scaffolds = append(scaffolds, output)
		} else if _, err := os.Stat(filepath.Join(dir, servergen.TypeScriptPackageFile)); errors.Is(err, fs.ErrNotExist) {
			return nil, nil, fmt.Errorf("stack %s: server %s serves %s, whose implementation %s holds no package.json, by which the Bun workspace links it; add one named %s", stackName, s.Name, ref.Name, dir, r.Options.Naming.NpmImplementationPackage(ref.Name))
		}
		pkg, err := servergen.ImplementationPackage(r.Options.Naming, ref.Name, dir)
		if err != nil {
			return nil, nil, err
		}
		in.APIs = append(in.APIs, servergen.TypeScriptAPIInput{
			Output:         output,
			Routes:         routes,
			Config:         config,
			Implementation: servergen.TypeScriptImplementation{Dir: dir, Package: pkg},
		})
		in.PackageDirs[output.PackageName] = APIDir(out, ref.Name)
		in.PackageDirs[pkg] = dir
		for _, call := range output.Deps.Calls {
			in.PackageDirs[call.Package] = SDKDir(out, LangTypeScript, call.Service)
		}
	}
	server, err := servergen.PlanTypeScript(in)
	if err != nil {
		return nil, nil, err
	}
	return server, scaffolds, nil
}

// cloudSQLDatabases resolves every environment of the stack and returns, by
// server, the DB services, sorted, that some environment's sql edge
// connects the server to with a Cloud SQL connector configuration. The
// entrypoint of such a server links the Cloud SQL connector, and no other
// does (docs/stack-model.md, section 8.1). The stack generator runs first
// and resolves the same environments, so a build whose environment does
// not resolve stops at its error before this runs.
func cloudSQLDatabases(c registry.GenerateContext, st *ir.Stack, services []stack.Service) (map[string][]string, error) {
	envs, err := stackgen.Resolve(c, st, services)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, env := range envs {
		edges := map[string]*ir.Edge{}
		for _, e := range env.Edges {
			edges[e.ID] = e
		}
		for _, d := range env.Deployables {
			for _, b := range d.Bindings {
				e := edges[b.Edge]
				if b.Source != ir.BindingDerived || e == nil || e.Kind != ir.EdgeSQL || !derivesCloudSQL(b.Value) {
					continue
				}
				if !slices.Contains(out[d.Name], e.Service.Name) {
					out[d.Name] = append(out[d.Name], e.Service.Name)
				}
			}
		}
	}
	for _, dbs := range out {
		slices.Sort(dbs)
	}
	return out, nil
}

// derivesCloudSQL reports whether a sql edge's derived value, an
// ir.DatabaseConnection in its JSON form, is a Cloud SQL connector
// configuration.
func derivesCloudSQL(value any) bool {
	data, err := json.Marshal(value)
	if err != nil {
		return false
	}
	var conn ir.DatabaseConnection
	return json.Unmarshal(data, &conn) == nil && conn.CloudSQL != nil
}

// servedAPI is the Go server output of the API service a server serves, as
// the API's own build prepares it.
func (r run) servedAPI(stackName, server, service string) (*apigen.APIOutput, error) {
	served, err := r.servedRun(stackName, server, service)
	if err != nil {
		return nil, err
	}
	output, err := served.goServerOutput()
	if err != nil {
		return nil, err
	}
	if output == nil {
		return nil, fmt.Errorf("stack %s: server %s serves %s, which declares no operations", stackName, server, service)
	}
	return output, nil
}

// servedRun is the run of the API service a server serves, as the API's
// own build makes it.
func (r run) servedRun(stackName, server, service string) (run, error) {
	schema, err := r.LoadDependency(service)
	if err != nil {
		return run{}, fmt.Errorf("stack %s: server %s serves %s: %w", stackName, server, service, err)
	}
	cfg, err := r.Options.LoadDependencyConfig(service)
	if err != nil {
		return run{}, fmt.Errorf("stack %s: server %s serves %s: %w", stackName, server, service, err)
	}
	outputs, err := registry.ParseOutputs(cfg.Outputs, r.Registry)
	if err != nil {
		return run{}, fmt.Errorf("schema config for %s: %w", service, err)
	}
	if !outputs.APIEnabled() {
		return run{}, fmt.Errorf("stack %s: server %s serves %s, whose config generates no API server; enable outputs.api in %s's config", stackName, server, service, service)
	}
	opts := r.Options
	opts.Stage = registry.StageAll
	served, _ := newRun(schema, cfg, outputs, opts, r.Registry)
	return served, nil
}

// implementation finds the implementation of service: its package at the
// naming file's [implementation_paths] go template under the repository
// root, and the module that holds it. When no go.mod at the package or
// above it, up to the repository root, holds it, newModule is true and the
// module is the one the scaffold writes into the package.
func (r run) implementation(service string) (impl servergen.Implementation, newModule bool, err error) {
	impl.Dir = r.Options.Naming.GoImplementationDir(r.Options.RepositoryRoot, service)
	module, moduleDir, found, err := servergen.FindModule(impl.Dir, r.Options.RepositoryRoot)
	if err != nil {
		return impl, false, fmt.Errorf("generator: the implementation of %s: %w", service, err)
	}
	if !found {
		module, moduleDir, newModule = servergen.ImplementationModulePath(r.Options.Naming, service), impl.Dir, true
	}
	impl.Module, impl.ModuleDir = module, moduleDir
	if impl.Import, err = servergen.ImportPath(module, moduleDir, impl.Dir); err != nil {
		return impl, false, err
	}
	return impl, newModule, nil
}

// goServerModules lists the modules a Go API server's build reads, each
// with its directory: its own module, the types modules it reaches, the
// ORM of its database, the SDK of each API it calls, and the runtime
// modules. The API module and the packages its Deps imports are direct,
// and so are its database's Go types when it authenticates with the
// identity runtime, whose store a server's main.go builds from their
// descriptor; a job's, which builds none, reaches them through the ORM.
// A runtime module no [paths] key names a checkout of is pinned to the
// release that generates the server, which the module proxy serves
// (releasePins).
func (r run) goServerModules(o *apigen.APIOutput, job bool) []servergen.Module {
	out, paths, n := r.Options.OutputRoot, r.Options.Paths, r.Options.Naming
	pins := r.releasePins()
	runtimeModule := func(module, dir string, direct bool) servergen.Module {
		m := servergen.Module{Path: module, Dir: dir, Direct: direct}
		if version, ok := pins[module]; ok {
			m.Version, m.Pinned = version, true
		}
		return m
	}
	scalar := runtimeModule(n.ScalarGoModule, paths.ScalarGo, false)
	if !scalar.Pinned {
		scalar.Version = "v1.0.0"
	}
	modules := []servergen.Module{
		{Path: o.ModulePath, Dir: APIDir(out, o.SchemaName), Direct: true},
		{Path: o.TypesModule, Dir: TypesDir(out, LangGo, o.SchemaName)},
	}
	if o.Auth.Identity && o.Deps.Database != "" {
		// main.go builds the identity store from the descriptor constant of
		// the database's Go types (D50).
		modules = append(modules, servergen.Module{Path: n.GoTypesModule(o.Deps.Database), Dir: TypesDir(out, LangGo, o.Deps.Database), Direct: !job})
	}
	for _, m := range o.IndirectModules {
		modules = append(modules, servergen.Module{Path: m, Dir: TypesDir(out, LangGo, path.Base(m))})
	}
	if o.Deps.Database != "" {
		modules = append(modules, servergen.Module{Path: o.Deps.ORMModule, Dir: ORMDir(out, o.Deps.Database), Direct: true})
	}
	if o.IsPublic && o.UpstreamSchema != "" {
		modules = append(modules, servergen.Module{Path: o.ORMModule, Dir: ORMDir(out, o.UpstreamSchema), Direct: true})
	}
	for _, call := range o.Deps.Calls {
		modules = append(modules, servergen.Module{Path: call.Module, Dir: SDKDir(out, LangGo, call.Service), Direct: true})
	}
	modules = append(modules,
		runtimeModule(n.HTTPRuntimeGoModule, paths.HTTPRuntimeGo, true),
		runtimeModule(n.SchemaRuntimeGoModule, paths.SchemaRuntimeGo, false),
		runtimeModule(n.SchemaIRGoModule, paths.SchemaIR, false),
		scalar,
	)
	if (o.IsPublic && o.UpstreamVersionGraph) || o.Deps.VersionGraph {
		modules = append(modules, runtimeModule(n.VersionGraphGoModule, paths.VersionGraphGo, false))
	}
	return modules
}
