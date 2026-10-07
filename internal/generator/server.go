package generator

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"slices"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/generator/stackgen"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/stack"
	ir "github.com/parable-work/superschematic/ir"
)

// serverGenerator is the name of the Stack kind's generator of server
// entrypoints.
const serverGenerator = "server"

// generateServers writes the entrypoint of each Go server of the stack the
// schema declares under servergen.StackDir (docs/stack-model.md, sections
// 8.1 and 8.2), and scaffolds each served API's implementation that is
// missing (section 8.5). No environment changes the servers, but the
// entrypoint of one that some environment connects to a database on Cloud
// SQL links the Cloud SQL connector. It plans every server before it
// writes anything, so a server it refuses leaves the last build's
// entrypoints and every implementation as they were. A build without a
// repository root writes none: the implementations live under it.
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
	for _, s := range servers {
		if s.Language != APILanguageGo {
			r.Logf("  - server %s: a %s server, which gets no generated entrypoint yet\n", s.Name, s.Language)
			continue
		}
		server, scaffolds, err := r.planServer(st.Name, s, cloudSQL[s.Name])
		if err != nil {
			return err
		}
		plans = append(plans, planned{server, scaffolds})
	}

	for _, p := range plans {
		for _, sc := range p.scaffold {
			if err := sc.write(r); err != nil {
				return err
			}
		}
	}
	dir := servergen.StackDir(r.Options.OutputRoot, st.Name)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("stack %s: %w", st.Name, err)
	}
	if len(plans) == 0 {
		r.Skip(serverGenerator)
		return nil
	}
	for _, p := range plans {
		if err := servergen.Write(p.server, servergen.ServerDir(r.Options.OutputRoot, st.Name, p.server.Name)); err != nil {
			return err
		}
		if p.server.Docker == nil {
			r.Logf("  - server %s: no Dockerfile, since %s\n", p.server.Name, p.server.NoDocker)
		}
	}
	r.Done(serverGenerator, dir)
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

// planServer plans the entrypoint of server s of the stack: the Go server
// output of each API it serves, read as that API's own build reads it,
// where each implementation lives, and every module the build needs.
// cloudSQL are the DB services some environment connects it to on Cloud
// SQL. It returns the implementations that are missing, which the caller
// scaffolds.
func (r run) planServer(stackName string, s *ir.ResolvedDeployable, cloudSQL []string) (*servergen.Server, []scaffold, error) {
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
	var scaffolds []scaffold
	var versionGraph bool
	var modules []servergen.Module
	for _, ref := range s.Services {
		output, err := r.servedAPI(stackName, s.Name, ref.Name)
		if err != nil {
			return nil, nil, err
		}
		apiModules := r.goServerModules(output)
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
	schema, err := r.LoadDependency(service)
	if err != nil {
		return nil, fmt.Errorf("stack %s: server %s serves %s: %w", stackName, server, service, err)
	}
	cfg, err := r.Options.LoadDependencyConfig(service)
	if err != nil {
		return nil, fmt.Errorf("stack %s: server %s serves %s: %w", stackName, server, service, err)
	}
	outputs, err := registry.ParseOutputs(cfg.Outputs, r.Registry)
	if err != nil {
		return nil, fmt.Errorf("schema config for %s: %w", service, err)
	}
	if !outputs.APIEnabled() {
		return nil, fmt.Errorf("stack %s: server %s serves %s, whose config generates no API server; enable outputs.api in %s's config", stackName, server, service, service)
	}
	opts := r.Options
	opts.Stage = registry.StageAll
	served, _ := newRun(schema, cfg, outputs, opts, r.Registry)
	output, err := served.goServerOutput()
	if err != nil {
		return nil, err
	}
	if output == nil {
		return nil, fmt.Errorf("stack %s: server %s serves %s, which declares no operations", stackName, server, service)
	}
	return output, nil
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
// modules. The API module and the packages its Deps imports are direct.
// A runtime module no [paths] key names a checkout of is pinned to the
// release that generates the server, which the module proxy serves:
// superschematic's at the release's tag, superscalar's at the version the
// release links and builds its static archives from (D47, amended). A
// binary built from a checkout names no release, and pins none.
func (r run) goServerModules(o *apigen.APIOutput) []servergen.Module {
	out, paths, n := r.Options.OutputRoot, r.Options.Paths, r.Options.Naming
	rel, defaults := r.Options.ReleaseInfo(), naming.Default()
	runtimeModule := func(module, dir string, direct bool) servergen.Module {
		m := servergen.Module{Path: module, Dir: dir, Direct: direct}
		if dir == "" && rel.Version != "" && slices.Contains([]string{defaults.HTTPRuntimeGoModule, defaults.SchemaRuntimeGoModule, defaults.SchemaIRGoModule, defaults.VersionGraphGoModule}, module) {
			m.Version, m.Pinned = rel.ModuleVersion(), true
		}
		return m
	}
	scalar := servergen.Module{Path: n.ScalarGoModule, Dir: paths.ScalarGo, Version: "v1.0.0"}
	if paths.ScalarGo == "" && rel.Version != "" && rel.ScalarGo != "" && n.ScalarGoModule == defaults.ScalarGoModule {
		scalar.Version, scalar.Pinned = rel.ScalarGo, true
	}
	modules := []servergen.Module{
		{Path: o.ModulePath, Dir: APIDir(out, o.SchemaName), Direct: true},
		{Path: o.TypesModule, Dir: TypesDir(out, LangGo, o.SchemaName)},
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
