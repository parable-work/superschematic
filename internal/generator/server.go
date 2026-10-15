package generator

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/goutil"
	"github.com/parable-work/superschematic/internal/generator/rustgen"
	"github.com/parable-work/superschematic/internal/generator/rustrestgen"
	"github.com/parable-work/superschematic/internal/generator/rustutil"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/generator/stackgen"
	"github.com/parable-work/superschematic/internal/generator/tsgen"
	"github.com/parable-work/superschematic/internal/generator/tsrestgen"
	"github.com/parable-work/superschematic/internal/generator/tsutil"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/stack"
	ir "github.com/parable-work/superschematic/ir"
)

// serverGenerator is the name of the Stack kind's generator of server
// entrypoints.
const serverGenerator = "server"

// generateServers writes the entrypoint of each Go, TypeScript and Rust
// server of the stack the schema declares under servergen.StackDir
// (docs/stack-model.md, sections 8.1, 8.2 and 8.6), and of each Go,
// TypeScript and Rust job and worker beside them (sections 8.7 and 8.8,
// D52, D53, D65), and scaffolds each served API's implementation that is
// missing, a Go server's in Go, a TypeScript server's in TypeScript and a
// Rust server's in Rust (sections 8.5 and 8.6). The crates of the Rust
// servers, jobs and workers are the members of the output root's Cargo
// workspace, whose root and lockfile it writes (D57, D65). No environment
// changes the servers, the jobs or the workers, but the entrypoint of a Go
// or TypeScript one that some environment connects to a database on Cloud
// SQL links the Cloud SQL connector, or depends on the Node one; a Rust
// one reaches Cloud SQL through its runtime alone. It plans every
// entrypoint before it writes anything, so one it refuses leaves the last
// build's entrypoints and every implementation as they were. A build
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
	// The APIs a site of the stack calls, whose servers answer CORS for
	// the sites' origins (D55).
	cors := map[string]bool{}
	for _, svc := range services {
		if svc.Kind == ir.SchemaKindSite {
			for _, call := range svc.Calls {
				cors[call.Name] = true
			}
		}
	}

	type planned struct {
		server   *servergen.Server
		scaffold []scaffold
	}
	var plans []planned
	var tsPlans []*servergen.TypeScriptServer
	var rustPlans []*servergen.RustServer
	scaffolding := map[string]bool{}
	tsScaffolding := map[string]bool{}
	rustScaffolding := map[string]bool{}
	var tsScaffolds []*tsrestgen.APIOutput
	var rustScaffolds []*rustrestgen.APIOutput
	for _, s := range servers {
		switch s.Language {
		case APILanguageGo:
			server, scaffolds, err := r.planEntrypoint(st.Name, s, cloudSQL[s.Name], cors)
			if err != nil {
				return err
			}
			plans = append(plans, planned{server, scaffolds})
			for _, sc := range scaffolds {
				scaffolding[sc.output.SchemaName] = true
			}
		case APILanguageTypeScript:
			server, scaffolds, err := r.planTypeScriptServer(st.Name, s, cloudSQL[s.Name], cors)
			if err != nil {
				return err
			}
			tsPlans = append(tsPlans, server)
			tsScaffolds = append(tsScaffolds, scaffolds...)
			for _, sc := range scaffolds {
				tsScaffolding[sc.SchemaName] = true
			}
		case APILanguageRust:
			server, scaffolds, err := r.planRustServer(st.Name, s, cors)
			if err != nil {
				return err
			}
			rustPlans = append(rustPlans, server)
			rustScaffolds = append(rustScaffolds, scaffolds...)
			for _, sc := range scaffolds {
				rustScaffolding[sc.SchemaName] = true
			}
		default:
			r.Logf("  - server %s: a %s server, which gets no generated entrypoint yet\n", s.Name, s.Language)
		}
	}
	jobs, err := stack.Jobs(stack.Input{Stack: st, Services: services})
	if err != nil {
		return err
	}
	if err := r.checkJobs(st.Name, jobs, services, scaffolding, tsScaffolding, rustScaffolding); err != nil {
		return err
	}
	// The servers' plans scaffold the API of each job and worker, which a
	// server serves.
	for _, j := range jobs {
		switch j.Language {
		case APILanguageGo:
			job, _, err := r.planEntrypoint(st.Name, j, cloudSQL[j.Name], nil)
			if err != nil {
				return err
			}
			plans = append(plans, planned{server: job})
		case APILanguageTypeScript:
			job, _, err := r.planTypeScriptServer(st.Name, j, cloudSQL[j.Name], nil)
			if err != nil {
				return err
			}
			tsPlans = append(tsPlans, job)
		case APILanguageRust:
			job, _, err := r.planRustServer(st.Name, j, nil)
			if err != nil {
				return err
			}
			rustPlans = append(rustPlans, job)
		default:
			r.Logf("  - job %s: a %s job, which gets no generated entrypoint yet\n", j.Name, j.Language)
		}
	}
	workers, err := stack.Workers(stack.Input{Stack: st, Services: services})
	if err != nil {
		return err
	}
	if err := r.checkWorkers(st.Name, workers, services, scaffolding, tsScaffolding, rustScaffolding); err != nil {
		return err
	}
	for _, w := range workers {
		switch w.Language {
		case APILanguageGo:
			worker, _, err := r.planEntrypoint(st.Name, w, cloudSQL[w.Name], nil)
			if err != nil {
				return err
			}
			plans = append(plans, planned{server: worker})
		case APILanguageTypeScript:
			if err := r.checkTypeScriptQueue(st.Name, w, services); err != nil {
				return err
			}
			worker, _, err := r.planTypeScriptServer(st.Name, w, cloudSQL[w.Name], nil)
			if err != nil {
				return err
			}
			tsPlans = append(tsPlans, worker)
		case APILanguageRust:
			worker, _, err := r.planRustServer(st.Name, w, nil)
			if err != nil {
				return err
			}
			rustPlans = append(rustPlans, worker)
		default:
			r.Logf("  - worker %s: a %s worker, which gets no generated entrypoint yet\n", w.Name, w.Language)
		}
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
	for _, output := range rustScaffolds {
		if err := r.scaffoldRustImplementation(output, r.Options.RepositoryRoot); err != nil {
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
	if len(plans) == 0 && len(tsPlans) == 0 && len(rustPlans) == 0 {
		// The stack's Rust servers are gone, if it had any: the workspace's
		// root names the other stacks' alone, or is removed, and so do the
		// implementations' mirrors.
		if _, err := servergen.WriteRustWorkspace(r.Options.OutputRoot); err != nil {
			return err
		}
		if err := servergen.PruneRustMirrors(r.Options.OutputRoot); err != nil {
			return err
		}
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
			r.Logf("  - %s %s: no Dockerfile, since %s\n", server.Kind, server.Name, server.NoDocker)
		}
	}
	for _, server := range rustPlans {
		if err := servergen.WriteRust(server, servergen.ServerDir(r.Options.OutputRoot, st.Name, server.Name)); err != nil {
			return err
		}
		if server.Docker == nil {
			r.Logf("  - %s %s: no Dockerfile, since %s\n", server.Kind, server.Name, server.NoDocker)
		}
	}
	if err := r.writeRustMirrors(rustPlans); err != nil {
		return fmt.Errorf("stack %s: %w", st.Name, err)
	}
	if err := r.writeRustWorkspace(len(rustPlans) > 0); err != nil {
		return fmt.Errorf("stack %s: %w", st.Name, err)
	}
	r.Done(serverGenerator, dir)
	return nil
}

// writeRustMirrors brings the mirror of each implementation the stack's
// Rust servers, jobs and workers build against up to date under the output
// root (D65; servergen's mirror.go), once for an implementation two of
// them build against, after the scaffolds are written, then removes the
// mirrors no crate under the output root's server/ depends on any longer,
// whichever stack's. cargo update reads the mirrors next.
func (r run) writeRustMirrors(servers []*servergen.RustServer) error {
	written := map[string]bool{}
	for _, server := range servers {
		for _, m := range server.Mirrors {
			if written[m.Dir] {
				continue
			}
			written[m.Dir] = true
			if err := servergen.WriteRustMirror(m); err != nil {
				return err
			}
		}
	}
	return servergen.PruneRustMirrors(r.Options.OutputRoot)
}

// writeRustWorkspace writes the output root's Cargo workspace for the
// crates of the Rust servers, jobs and workers on disk, every stack's (D57,
// D65), and, when this stack has one, brings its lockfile up to date with cargo (servergen.
// UpdateRustLockfile), which the project commits, and says in one line how
// to commit it when git ignores it. Without cargo on PATH, or when cargo
// cannot resolve the workspace, as after a build of the stack alone,
// before the crates its servers depend on are on disk, it says so in one
// line and leaves the lockfile as it is: the image and the generated CI
// build with --locked from it, and refuse a stale one.
func (r run) writeRustWorkspace(rust bool) error {
	members, err := servergen.WriteRustWorkspace(r.Options.OutputRoot)
	if err != nil || !members || !rust {
		return err
	}
	r.noteIgnoredRustLockfile()
	lockfile := filepath.Join(r.Options.OutputRoot, servergen.RustLockfileName)
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		r.Logf("  - %s: no cargo on PATH to bring it up to date; run cargo update --workspace in %s once every service is built, and commit it\n", lockfile, r.Options.OutputRoot)
		return nil
	}
	if err := servergen.UpdateRustLockfile(cargo, r.Options.OutputRoot); err != nil {
		r.Logf("  - %s: not brought up to date: %v; build every service (superschematic build-all), which runs cargo update --workspace in %s, and commit it\n", lockfile, oneLine(err.Error()), r.Options.OutputRoot)
	}
	return nil
}

// noteIgnoredRustLockfile says, in one line, how to commit the Cargo
// workspace's lockfile when git ignores it, as noteIgnoredLockfile says
// for the Bun workspace's (D51, amended; D57).
func (r run) noteIgnoredRustLockfile() {
	rule, ignored := tsgen.IgnoredFile(r.Options.OutputRoot, servergen.RustLockfileName)
	if !ignored {
		return
	}
	by := "git"
	if rule != "" {
		by = rule
	}
	r.Logf("  - %s is ignored by %s; commit it, so images and CI build the Rust crates it pins: ignore the output root's contents, not the directory (dist/* and !dist/%s in place of dist/; docs/stack-model.md, section 8.6)\n",
		filepath.Join(r.Options.OutputRoot, servergen.RustLockfileName), by, servergen.RustLockfileName)
}

// oneLine is cargo's message s on one line: its lines, trimmed, joined by
// colons, without the blank ones and cargo's "Caused by:" headings.
func oneLine(s string) string {
	var parts []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" && line != "Caused by:" {
			parts = append(parts, line)
		}
	}
	return strings.Join(parts, ": ")
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
// implementation declares no NewJobs (D52), one of whose TypeScript APIs
// does while its implementation exports no createJobs, or one of whose
// Rust APIs does while its implementation declares no create_jobs (D65).
// The implementation exists, so it is the engineer's, and the build writes
// into it no more: an API that predates its jobs fails here, saying what to
// add, rather than in the compile of a job's entrypoint, or when Bun runs
// it. An API whose scaffold this build writes, scaffolding in Go,
// tsScaffolding in TypeScript and rustScaffolding in Rust, gets them from
// it.
func (r run) checkJobs(stackName string, jobs []*ir.ResolvedDeployable, services []stack.Service, scaffolding, tsScaffolding, rustScaffolding map[string]bool) error {
	checked := map[string]bool{}
	for _, job := range jobs {
		api := job.Job.API
		if checked[api] {
			continue
		}
		checked[api] = true
		if job.Language == APILanguageRust {
			if rustScaffolding[api] {
				continue
			}
			var methods []string
			for _, svc := range services {
				if svc.Name == api {
					for _, j := range svc.Jobs {
						methods = append(methods, fmt.Sprintf("      async fn %s(&self, shutdown: Shutdown) -> Result<(), BoxError> {\n          // ...\n      }", rustutil.Identifier(j.Name, "run")))
					}
				}
			}
			if err := r.checkRustFunc(stackName, api, "jobs", rustrestgen.JobsFunc, "Jobs", methods, "Deps, Jobs and BoxError are %s's, and Shutdown the HTTP runtime's", "8.7"); err != nil {
				return err
			}
			continue
		}
		if job.Language == APILanguageTypeScript {
			if tsScaffolding[api] {
				continue
			}
			var methods []string
			for _, svc := range services {
				if svc.Name == api {
					for _, j := range svc.Jobs {
						methods = append(methods, fmt.Sprintf("    async %s(signal) {\n      // ...\n    },", tsutil.ToCamelCase(j.Name)))
					}
				}
			}
			if err := r.checkTypeScriptExport(stackName, api, "jobs", tsrestgen.JobsFunc, "JobsConstructor", methods, "8.7"); err != nil {
				return err
			}
			continue
		}
		if scaffolding[api] || job.Language != APILanguageGo {
			continue
		}
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

// checkWorkers refuses a stack one of whose Go APIs declares workers while
// its implementation declares no NewWorkers (D53), one of whose
// TypeScript APIs does while its implementation exports no createWorkers,
// or one of whose Rust APIs does while its implementation declares no
// create_workers (D65), as checkJobs refuses one without its jobs: the
// implementation is the engineer's, and the build writes into it no more.
// It refuses a Rust worker of a queue the Rust types of its database leave
// out of src/queues.rs too (checkRustQueue).
func (r run) checkWorkers(stackName string, workers []*ir.ResolvedDeployable, services []stack.Service, scaffolding, tsScaffolding, rustScaffolding map[string]bool) error {
	checked := map[string]bool{}
	for _, w := range workers {
		if w.Language == APILanguageRust {
			if err := r.checkRustQueue(stackName, w, services); err != nil {
				return err
			}
		}
		api := w.Worker.API
		if checked[api] {
			continue
		}
		checked[api] = true
		if w.Language == APILanguageRust {
			if rustScaffolding[api] {
				continue
			}
			var methods []string
			for _, svc := range services {
				if svc.Name == api {
					for _, worker := range svc.Workers {
						methods = append(methods, fmt.Sprintf("      async fn %s(&self, message: %s::%s) -> Result<(), BoxError> {\n          // handle a message of %s\n      }", rustutil.Identifier(worker.Name, "handle"), rustrestgen.DatabaseTypesModule, worker.Queue, worker.Queue))
					}
				}
			}
			if err := r.checkRustFunc(stackName, api, "workers", rustrestgen.WorkersFunc, "Workers", methods, "Deps, Workers, BoxError and "+rustrestgen.DatabaseTypesModule+", the Rust types of the database that declares each queue, are %s's", "8.8"); err != nil {
				return err
			}
			continue
		}
		if w.Language == APILanguageTypeScript {
			if tsScaffolding[api] {
				continue
			}
			var methods []string
			for _, svc := range services {
				if svc.Name == api {
					for _, worker := range svc.Workers {
						methods = append(methods, fmt.Sprintf("    async %s(message, signal) {\n      // handle a message of %s\n    },", tsutil.ToCamelCase(worker.Name), worker.Queue))
					}
				}
			}
			if err := r.checkTypeScriptExport(stackName, api, "workers", tsrestgen.WorkersFunc, "WorkersConstructor", methods, "8.8"); err != nil {
				return err
			}
			continue
		}
		if scaffolding[api] || w.Language != APILanguageGo {
			continue
		}
		impl, _, err := r.implementation(api)
		if err != nil {
			return err
		}
		exists, err := apigen.ImplementationExists(impl.Dir)
		if err != nil || !exists {
			return err
		}
		declares, err := apigen.DeclaresFunc(impl.Dir, apigen.WorkersFunc)
		if err != nil || declares {
			return err
		}
		db := w.Worker.Database
		var methods []string
		for _, svc := range services {
			if svc.Name != api {
				continue
			}
			if db == "" && svc.AuthDB != nil {
				db = svc.AuthDB.Name
			}
			for _, dep := range svc.Dependencies {
				if db == "" && dep.Kind == ir.SchemaKindDB {
					db = dep.Name
				}
			}
			for _, worker := range svc.Workers {
				methods = append(methods, fmt.Sprintf("\t%s(ctx context.Context, msg %s.%s) error", goutil.GoPublicIdentifier(worker.Name), toGoPackageName(db), worker.Queue))
			}
		}
		return fmt.Errorf("stack %s: %s declares workers, and its implementation at %s, which the build no longer writes into, declares no %s; add\n\n"+
			"\tfunc %s(deps api.Deps) (api.Workers, error)\n\n"+
			"returning a value with a method per worker:\n\n%s\n\n"+
			"where api is %s, whose api.WorkersConstructor is %s's signature, and %s is %s, the Go types of the database that declares each queue (docs/stack-model.md, section 8.8)",
			stackName, api, impl.Dir, apigen.WorkersFunc, apigen.WorkersFunc, strings.Join(methods, "\n"), r.Options.Naming.GoAPIModule(api), apigen.WorkersFunc,
			toGoPackageName(db), r.Options.Naming.GoTypesModule(db))
	}
	return nil
}

// checkTypeScriptExport refuses a TypeScript API whose implementation
// exists and exports no fn, of the API package's type typ, while the API
// declares what, its jobs or its workers, each a method of methods: the
// build no longer writes into the implementation, and Bun would fail only
// when the job or the worker runs.
func (r run) checkTypeScriptExport(stackName, api, what, fn, typ string, methods []string, section string) error {
	dir := r.Options.Naming.TypeScriptImplementationDir(r.Options.RepositoryRoot, api)
	exists, err := tsrestgen.ImplementationExists(dir)
	if err != nil || !exists {
		return err
	}
	exports, err := tsrestgen.ExportsName(dir, fn)
	if err != nil || exports {
		return err
	}
	return fmt.Errorf("stack %s: %s declares %s, and its implementation at %s, which the build no longer writes into, exports no %s; add\n\n"+
		"  export const %s: %s = deps => ({\n%s\n  });\n\n"+
		"importing %s from %s, the type the entrypoint calls it as (docs/stack-model.md, section %s)",
		stackName, api, what, dir, fn, fn, typ, strings.Join(methods, "\n"), typ, r.Options.Naming.NpmAPIPackage(api), section)
}

// checkRustFunc refuses a Rust API whose implementation exists and whose
// src/lib.rs declares no fn, which returns the API crate's trait typ,
// while the API declares what, its jobs or its workers, each a method of
// methods (D65): the build no longer writes into the implementation, and
// cargo would fail only when it builds the job's or the worker's crate.
// names, a format of the API crate's name, says whose the names the code
// to add takes are.
func (r run) checkRustFunc(stackName, api, what, fn, typ string, methods []string, names, section string) error {
	dir := r.Options.Naming.RustImplementationDir(r.Options.RepositoryRoot, api)
	exists, err := rustrestgen.ImplementationExists(dir)
	if err != nil || !exists {
		return err
	}
	declares, err := rustrestgen.DeclaresFunc(dir, fn)
	if err != nil || declares {
		return err
	}
	crate := strings.ReplaceAll(r.Options.Naming.RustAPICrate(api), "-", "_")
	return fmt.Errorf("stack %s: %s declares %s, and its implementation at %s, which the build no longer writes into, declares no %s in %s; add\n\n"+
		"  pub async fn %s(deps: Deps) -> Result<Arc<dyn %s>, BoxError>\n\n"+
		"returning a value that implements %s, with a method per %s:\n\n"+
		"  #[async_trait]\n  impl %s for ... {\n%s\n  }\n\n"+
		"where %s (docs/stack-model.md, section %s)",
		stackName, api, what, dir, fn, rustrestgen.ImplementationLib, fn, typ, typ, strings.TrimSuffix(what, "s"), typ, strings.Join(methods, "\n"),
		fmt.Sprintf(names, crate), section)
}

// workerDatabase is the database whose queue the worker w claims: the one
// resolution gives it, or else its API's, its authDb or its one DB
// dependency. Empty when the API connects to none.
func workerDatabase(w *ir.ResolvedDeployable, services []stack.Service) string {
	db := w.Worker.Database
	for _, svc := range services {
		if svc.Name != w.Worker.API || db != "" {
			continue
		}
		if svc.AuthDB != nil {
			db = svc.AuthDB.Name
		}
		for _, dep := range svc.Dependencies {
			if db == "" && dep.Kind == ir.SchemaKindDB {
				db = dep.Name
			}
		}
	}
	return db
}

// checkRustQueue refuses a Rust worker whose queue the Rust types of its
// database leave out of src/queues.rs, as they leave out a queue whose
// message the Rust queue client cannot hold yet (D65), and as
// checkTypeScriptQueue refuses a TypeScript one.
func (r run) checkRustQueue(stackName string, w *ir.ResolvedDeployable, services []stack.Service) error {
	db := workerDatabase(w, services)
	if db == "" || r.Options.LoadDependency == nil {
		return nil
	}
	schema, err := r.LoadDependency(db)
	if err != nil {
		return fmt.Errorf("stack %s: worker %s claims a queue of %s: %w", stackName, w.Name, db, err)
	}
	queues, err := rustgen.RustQueues(schema)
	if err != nil {
		return fmt.Errorf("stack %s: worker %s claims a queue of %s: %w", stackName, w.Name, db, err)
	}
	for _, q := range queues {
		if q.Name == w.Worker.Queue && q.Unsupported != "" {
			return fmt.Errorf("stack %s: Rust worker %s claims queue %s of %s, which the Rust queue client cannot hold yet: its %s; hold the value in a @jsonField type, or write the worker in Go (D65)", stackName, w.Name, q.Name, db, q.Unsupported)
		}
	}
	return nil
}

// checkTypeScriptQueue refuses a TypeScript worker whose queue the
// TypeScript types of its database leave out of their ./queues entry, as
// they leave out a queue whose message the TypeScript queue client cannot
// hold yet (D53).
func (r run) checkTypeScriptQueue(stackName string, w *ir.ResolvedDeployable, services []stack.Service) error {
	db := workerDatabase(w, services)
	if db == "" || r.Options.LoadDependency == nil {
		return nil
	}
	schema, err := r.LoadDependency(db)
	if err != nil {
		return fmt.Errorf("stack %s: worker %s claims a queue of %s: %w", stackName, w.Name, db, err)
	}
	queues, err := tsgen.TypeScriptQueues(schema)
	if err != nil {
		return fmt.Errorf("stack %s: worker %s claims a queue of %s: %w", stackName, w.Name, db, err)
	}
	for _, q := range queues {
		if q.Name == w.Worker.Queue && q.Unsupported != "" {
			return fmt.Errorf("stack %s: TypeScript worker %s claims queue %s of %s, which the TypeScript queue client cannot hold yet: its %s; hold the value in a @jsonField type, or write the worker in Go (D53)", stackName, w.Name, q.Name, db, q.Unsupported)
		}
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
func (r run) planEntrypoint(stackName string, s *ir.ResolvedDeployable, cloudSQL []string, cors map[string]bool) (*servergen.Server, []scaffold, error) {
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
	if s.Worker != nil {
		in.Worker = &servergen.WorkerInput{Name: s.Worker.Name, GraceSeconds: s.Worker.GraceSeconds}
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
		in.APIs = append(in.APIs, servergen.APIInput{Output: output, Implementation: impl, CORS: cors[ref.Name]})
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
func (r run) planTypeScriptServer(stackName string, s *ir.ResolvedDeployable, cloudSQL []string, cors map[string]bool) (*servergen.TypeScriptServer, []*tsrestgen.APIOutput, error) {
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
	if s.Job != nil {
		in.Job = &servergen.JobInput{Name: s.Job.Name}
	}
	if s.Worker != nil {
		in.Worker = &servergen.WorkerInput{Name: s.Worker.Name, GraceSeconds: s.Worker.GraceSeconds}
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
		if err := r.checkIdentityDescriptor(stackName, s.Name, output); err != nil {
			return nil, nil, err
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
			CORS:           cors[ref.Name],
		})
		in.PackageDirs[output.PackageName] = APIDir(out, ref.Name)
		in.PackageDirs[pkg] = dir
		for _, call := range output.Deps.Calls {
			in.PackageDirs[call.Package] = SDKDir(out, LangTypeScript, call.Service)
		}
		if s.Worker != nil && output.Deps.DatabaseTypesPackage != "" {
			// The worker imports its queue's definition from the database's
			// TypeScript types (D53).
			in.PackageDirs[output.Deps.DatabaseTypesPackage] = TypesDir(out, LangTypeScript, output.Deps.Database)
		}
	}
	server, err := servergen.PlanTypeScript(in)
	if err != nil {
		return nil, nil, err
	}
	return server, scaffolds, nil
}

// planRustServer plans the entrypoint of the Rust server s of the stack
// (D57), or of its Rust job or worker (D65): the Rust API crate of each API
// it serves, with its Deps, as that API's own build writes it, its
// EnvConfig, and where each implementation lives, at the naming file's
// [implementation_paths] rust template, by the names its Cargo.toml gives
// it. It returns the APIs whose implementation is missing, which the
// caller scaffolds.
func (r run) planRustServer(stackName string, s *ir.ResolvedDeployable, cors map[string]bool) (*servergen.RustServer, []*rustrestgen.APIOutput, error) {
	out := r.Options.OutputRoot
	in := servergen.RustInput{
		Stack:              stackName,
		Server:             s.Name,
		Dir:                servergen.ServerDir(out, stackName, s.Name),
		OutputRoot:         out,
		Naming:             r.Options.Naming,
		RepositoryRoot:     r.Options.Naming.BuildContext(r.Options.RepositoryRoot),
		ImplementationRoot: r.Options.RepositoryRoot,
		SDKDirs:            map[string]string{},
		Paths:              r.Options.Paths,
	}
	if s.Job != nil {
		in.Job = &servergen.JobInput{Name: s.Job.Name}
	}
	if s.Worker != nil {
		in.Worker = &servergen.WorkerInput{Name: s.Worker.Name, GraceSeconds: s.Worker.GraceSeconds}
	}
	var scaffolds []*rustrestgen.APIOutput
	for _, ref := range s.Services {
		served, err := r.servedRun(stackName, s.Name, ref.Name)
		if err != nil {
			return nil, nil, err
		}
		output, config, err := served.rustAPI()
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
		dir := r.Options.Naming.RustImplementationDir(r.Options.RepositoryRoot, ref.Name)
		exists, err := rustrestgen.ImplementationExists(dir)
		if err != nil {
			return nil, nil, err
		}
		if !exists {
			scaffolds = append(scaffolds, output)
		}
		impl, err := servergen.ReadRustImplementation(r.Options.Naming, ref.Name, dir)
		if err != nil {
			return nil, nil, err
		}
		in.APIs = append(in.APIs, servergen.RustAPIInput{
			Output:         output,
			Routes:         routes,
			Config:         config,
			Dir:            APIDir(out, ref.Name),
			Implementation: impl,
			CORS:           cors[ref.Name],
		})
		for _, call := range output.Deps.Calls {
			in.SDKDirs[call.Service] = SDKDir(out, LangRust, call.Service)
		}
	}
	server, err := servergen.PlanRust(in)
	if err != nil {
		return nil, nil, err
	}
	return server, scaffolds, nil
}

// checkIdentityDescriptor refuses a TypeScript server of an API over the
// user model (D50) whose authDb generates no TypeScript types: main.ts
// builds the identity store from the identityDescriptor those types
// export, and the server's package depends on them.
func (r run) checkIdentityDescriptor(stackName, server string, output *tsrestgen.APIOutput) error {
	if output.Identity == nil || r.Options.DependencyConfig == nil {
		return nil
	}
	db := output.Identity.AuthDB
	cfg, ok := r.Options.DependencyConfig(db)
	if !ok {
		return nil
	}
	outputs, err := registry.ParseOutputs(cfg.Outputs, r.Registry)
	if err != nil {
		return fmt.Errorf("generator: schema config for %s: %w", db, err)
	}
	if !outputs.TypesEnabled(LangTypeScript) {
		return fmt.Errorf("stack %s: server %s serves %s, which authenticates with the identity runtime over %s, its authDb, whose TypeScript types export the identity descriptor the server's store reads, and %s generates none; enable outputs.types.%s in %s's config", stackName, server, output.SchemaName, db, db, LangTypeScript, db)
	}
	return nil
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
	if err := served.refuseServedEngineTypes(stackName, server); err != nil {
		return run{}, err
	}
	return served, nil
}

// refuseServedEngineTypes fails when the API a server serves uses an engine
// type of a DB service, a type that composes a behavior: the generated
// servers do not open the engine yet (D58). A dependency the build cannot
// load is left to the API's generators, which report it.
func (r run) refuseServedEngineTypes(stackName, server string) error {
	used := usedDependencyTypes(r.Schema)
	if len(used) == 0 {
		return nil
	}
	deps, err := r.loadDependencySchemas()
	if err != nil {
		return nil
	}
	if use, found := findEngineType(used, deps); found {
		return fmt.Errorf("generator: %s does not render engine types yet: stack %s: server %s serves %s, and %s %s, which composes behavior %s (D58)",
			serverGenerator, stackName, server, r.Config.Name, use.how, use.qualified(), use.behavior)
	}
	return nil
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
