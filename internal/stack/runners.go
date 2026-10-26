package stack

import (
	"slices"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// The engine's runners of a stack (D58). A DB service whose types compose
// behaviors is an engine database: every server of an API that connects to
// it opens the engine on it, and the engine's runner, which runs the
// reactions that follow a commit, the schedules, Timers' deadlines and the
// event log's retention, is a deployable of its own, of kind runner, named
// after the database (ir.RunnerDeployableName). Cloud Run gives a server's
// instance CPU only while it serves, so no server runs it; on Postgres one
// process runs it at a time, under the runner's lock, so a second instance
// waits to take over.
//
// The runner is written in the language of the first API, by name, that
// connects to the database, and runs the engine archive that API's servers
// link. Its one need is its database: a sql edge to it, whose derived
// field is its whole config. A database no API of the stack connects to
// gets no runner, since nothing opens its engine.

// Runners returns the engine's runners of in.Stack, which no environment
// changes: one per member engine database some member API connects to,
// sorted by name. Each has its Name, Services (its database), Language and
// Runner's Database and API; the rest is the environment's to resolve.
// in.Environment is not read. The error is an *Errors with every failure
// found among the checks that decide the runners.
func Runners(in Input) ([]*ir.ResolvedDeployable, error) {
	if in.Stack == nil {
		return nil, &Errors{List: []Error{{Code: CodeInvalidStack, Message: "no stack to read runners from"}}}
	}
	in.Environment = ""
	r := newResolver(nil, in)
	r.indexServices()
	r.collectMembers()
	r.declareDeployables()
	r.defaultDeployables()
	r.defaultWorkers()
	r.defaultRunners()
	var runners []*ir.ResolvedDeployable
	for _, name := range sortedKeys(r.deployables) {
		res := r.deployables[name].res
		if res.Kind != ir.DeployableRunner {
			continue
		}
		res.Language = r.runnerLanguage(res)
		runners = append(runners, res)
	}
	if r.failed() {
		return nil, r.errs
	}
	return runners, nil
}

// defaultRunners gives each member engine database that a member API
// connects to an engine's runner of its own.
func (r *resolver) defaultRunners() {
	for _, db := range sortedKeys(r.members) {
		svc := r.services[db]
		if svc.Kind != ir.SchemaKindDB || !svc.Engine {
			continue
		}
		api := ""
		for _, name := range sortedKeys(r.members) {
			other := r.services[name]
			if other.Kind != ir.SchemaKindAPI {
				continue
			}
			if connects, ok := r.connectsQuietly(other); ok && connects == db {
				api = name
				break
			}
		}
		if api == "" {
			continue
		}
		name := ir.RunnerDeployableName(db)
		if !namePattern.MatchString(name) {
			r.fail(CodeInvalidStack, "the engine's runner of %s would be named %q; a deployable's name is letters, digits, hyphens and underscores", db, name)
			continue
		}
		if other, clash := r.deployables[name]; clash {
			r.fail(CodeInvalidStack, "the engine's runner of %s would be named %s, like the %s %s; rename the %s", db, name, other.res.Kind, name, other.res.Kind)
			continue
		}
		r.deployables[name] = &deployable{res: &ir.ResolvedDeployable{
			Name:     name,
			Kind:     ir.DeployableRunner,
			Services: []ir.ServiceRef{{Name: svc.Name, Kind: svc.Kind}},
			Runner:   &ir.ResolvedRunner{Database: db, API: api},
		}}
	}
}

// resolveRunnerRef finds the engine's runner a settings `of` names beside
// its database's handle (`{of: ShopDb, runner: true}`).
func (r *resolver) resolveRunnerRef(where string, ref ir.DeployableRef) (*deployable, bool) {
	switch {
	case ref.Service == nil:
		r.fail(CodeInvalidStack, "%s names a runner but no DB service; an engine's runner is named beside its database's handle", where)
		return nil, false
	case ref.Job != "" || ref.Worker != "" || ref.Deriver != "" || ref.App != "":
		r.fail(CodeInvalidStack, "%s names the runner and a job, a worker, a deriver or an app; an element names one", where)
		return nil, false
	}
	if !r.checkRef(where, *ref.Service, ir.SchemaKindDB) {
		return nil, false
	}
	db := ref.Service.Name
	if !r.members[db] {
		r.fail(CodeUnknownDeployable, "%s names service %s, which is not in stack %s", where, db, r.stack.Name)
		return nil, false
	}
	if d, ok := r.deployables[ir.RunnerDeployableName(db)]; ok && d.res.Kind == ir.DeployableRunner {
		return d, true
	}
	why := "no API of the stack connects to it, so nothing opens its engine"
	if !r.services[db].Engine {
		why = "none of its types composes a behavior, so it is no engine database"
	}
	r.fail(CodeUnknownDeployable, "%s names the engine's runner of %s, which has none: %s", where, db, why)
	return nil, false
}

// applyRunnerSettings merges a runner's settings of one element: enabled
// alone, which turns it on or off. It reports whether d is a runner, whose
// element it was; a schedule or a time zone on one is refused, and
// applyWorkerSettings refuses instances and concurrency.
func (r *resolver) applyRunnerSettings(where string, d *deployable, entry *ir.DeployableSettings) bool {
	if d.res.Kind != ir.DeployableRunner {
		return false
	}
	if entry.Schedule != "" || entry.TimeZone != "" {
		r.fail(CodeInvalidSettings, "%s sets a schedule or a time zone on runner %s, which runs until it is stopped; only a job takes them", where, d.res.Name)
	}
	if entry.Enabled != nil {
		enabled := *entry.Enabled
		d.settings.enabled = &enabled
	}
	return true
}

// connectsQuietly is databaseOf without its failure, which the server of
// the API reports: the DB service an API connects to.
func (r *resolver) connectsQuietly(svc *Service) (string, bool) {
	if svc.AuthDB != nil {
		return svc.AuthDB.Name, true
	}
	var dbs []string
	for _, dep := range svc.Dependencies {
		if db, ok := r.services[dep.Name]; ok && dep.Kind == ir.SchemaKindDB && db.Kind == ir.SchemaKindDB {
			dbs = append(dbs, dep.Name)
		}
	}
	if len(dbs) != 1 {
		return "", false
	}
	return dbs[0], true
}

// runnerLanguage is the language of a runner: its API's, Go for an API
// that names none.
func (r *resolver) runnerLanguage(res *ir.ResolvedDeployable) string {
	if res.Runner == nil {
		return registry.APILanguageGo
	}
	if svc, ok := r.services[res.Runner.API]; ok && svc.Language != "" {
		return svc.Language
	}
	return registry.APILanguageGo
}

// placeRunner gives a runner its API's language and its platform's
// artifact, an image unless the platform builds a bundle, and checks its
// platform runs the language.
func (r *resolver) placeRunner(d *deployable) {
	d.res.Artifact = d.platform.Artifact
	if d.res.Artifact == "" {
		d.res.Artifact = ir.ArtifactImage
	}
	d.res.Language = r.runnerLanguage(d.res)
	if !slices.Contains(d.platform.Languages, d.res.Language) {
		r.fail(CodeUnrealizable, "runner %s runs the engine of %s in %s, the language of %s, and platform %s runs only %v", d.res.Name, d.res.Runner.Database, d.res.Language, d.res.Runner.API, d.platform.Name, d.platform.Languages)
	}
}

// runRunners decides what each runner runs in the environment: one
// instance, on a database the environment runs on Postgres, where the
// runner's lock lets one process run at a time, or none when its settings
// turn it off, which leaves the engine's reactions, schedules, Timers'
// deadlines and the event log's retention waiting until it is on again.
// A runner of a database on another dialect is refused, on or off: SQLite
// is a file one process writes, whose engine its server runs in-process.
func (r *resolver) runRunners() {
	for _, name := range sortedKeys(r.deployables) {
		d := r.deployables[name]
		if d.res.Kind != ir.DeployableRunner || d.res.Runner == nil {
			continue
		}
		run := d.res.Runner
		run.Instances = 1
		if s := d.settings; s.enabled != nil && !*s.enabled {
			run.Instances = 0
		}
		if host := r.deployables[r.byService[run.Database]]; host != nil && host.res.Dialect != "" && host.res.Dialect != registry.SQLDialectPostgres {
			r.fail(CodeUnrealizable, "runner %s runs the engine of %s, which environment %s places on %s, a %s database; a runner of its own runs on Postgres, under the runner's lock (D58)",
				name, run.Database, r.envName(), host.res.Name, host.res.Dialect)
		}
	}
}
