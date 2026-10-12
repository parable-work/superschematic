package stack

import (
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// The workers of a stack (docs/stack-model.md, section 8.8, D53). Each
// `@worker` of a member API service is a deployable of kind worker, named
// after its API and its class as a job is (ir.WorkerDeployableName). Its
// edges, its config and its identity are its API's, as a job's are: the
// resolver treats a worker as it treats a job wherever a deployable has an
// image (ir.DeployableKind.HasImage). What is its own is decided here: the
// queue it claims from, which must be a queue of the database its API
// connects to, a database the environment runs on Postgres, and how many
// instances run, each handling how many messages at a time.

// Workers returns the workers of in.Stack, which no environment changes: a
// worker per `@worker` of every API service the stack reaches, sorted by
// name. Each has its Name, Services (its API), Calls (its API's), Language
// and Worker's API, Name and Queue; the rest is the environment's to
// resolve. in.Environment is not read. The error is an *Errors with every
// failure found among the checks that decide the workers.
func Workers(in Input) ([]*ir.ResolvedDeployable, error) {
	if in.Stack == nil {
		return nil, &Errors{List: []Error{{Code: CodeInvalidStack, Message: "no stack to read workers from"}}}
	}
	in.Environment = ""
	r := newResolver(nil, in)
	r.indexServices()
	r.collectMembers()
	r.declareDeployables()
	r.defaultDeployables()
	r.defaultWorkers()
	r.resolveCalls()
	var workers []*ir.ResolvedDeployable
	for _, name := range sortedKeys(r.deployables) {
		res := r.deployables[name].res
		if res.Kind != ir.DeployableWorker {
			continue
		}
		res.Language, _ = r.serverLanguage(res)
		workers = append(workers, res)
	}
	if r.failed() {
		return nil, r.errs
	}
	return workers, nil
}

// defaultWorkers makes each worker of a member API service a deployable of
// its own, named after its API and its class.
func (r *resolver) defaultWorkers() {
	if r.byWorker == nil {
		r.byWorker = map[string]string{}
	}
	for _, name := range sortedKeys(r.members) {
		svc := r.services[name]
		if svc.Kind != ir.SchemaKindAPI {
			continue
		}
		for i := range svc.Workers {
			worker := &svc.Workers[i]
			key := name + "/" + worker.Name
			if _, dup := r.byWorker[key]; dup {
				r.fail(CodeInvalidStack, "service %s declares worker %s twice", name, worker.Name)
				continue
			}
			workerName := ir.WorkerDeployableName(name, worker.Name)
			if !namePattern.MatchString(workerName) {
				r.fail(CodeInvalidStack, "worker %s of %s would be named %q; a deployable's name is letters, digits, hyphens and underscores", worker.Name, name, workerName)
				continue
			}
			if other, clash := r.deployables[workerName]; clash {
				r.fail(CodeInvalidStack, "worker %s of %s would be named %s, like the %s %s; rename the worker or the %s", worker.Name, name, workerName, other.res.Kind, workerName, other.res.Kind)
				continue
			}
			r.deployables[workerName] = &deployable{
				res: &ir.ResolvedDeployable{
					Name:     workerName,
					Kind:     ir.DeployableWorker,
					Services: []ir.ServiceRef{{Name: svc.Name, Kind: svc.Kind}},
					Worker:   &ir.ResolvedWorker{API: name, Name: worker.Name, Queue: worker.Queue, GraceSeconds: registry.WorkerGraceSeconds(worker)},
				},
				worker: worker,
			}
			r.byWorker[key] = workerName
		}
	}
}

// resolveWorkerRef finds the worker a settings `of` names beside its API's
// handle.
func (r *resolver) resolveWorkerRef(where string, ref ir.DeployableRef) (*deployable, bool) {
	if ref.Service == nil {
		r.fail(CodeInvalidStack, "%s names worker %s but no API service; a worker is named beside its API's handle", where, ref.Worker)
		return nil, false
	}
	if ref.Job != "" {
		r.fail(CodeInvalidStack, "%s names both job %s and worker %s; an element names one", where, ref.Job, ref.Worker)
		return nil, false
	}
	if !r.checkRef(where, *ref.Service, ir.SchemaKindAPI) {
		return nil, false
	}
	if !r.members[ref.Service.Name] {
		r.fail(CodeUnknownDeployable, "%s names service %s, which is not in stack %s", where, ref.Service.Name, r.stack.Name)
		return nil, false
	}
	name, ok := r.byWorker[ref.Service.Name+"/"+ref.Worker]
	if !ok {
		var workers []string
		for _, worker := range r.services[ref.Service.Name].Workers {
			workers = append(workers, worker.Name)
		}
		declared := "none"
		if len(workers) > 0 {
			declared = strings.Join(workers, ", ")
		}
		r.fail(CodeUnknownDeployable, "%s names worker %s of %s, which declares no such @worker class (its workers: %s)", where, ref.Worker, ref.Service.Name, declared)
		return nil, false
	}
	return r.deployables[name], true
}

// applyWorkerSettings merges a worker's settings of one element, from
// environment env: instances, concurrency and enabled. It reports whether
// the element is done with: it named a worker, or set what only a worker
// takes on another deployable, which it refuses.
func (r *resolver) applyWorkerSettings(where, env string, d *deployable, entry *ir.DeployableSettings) bool {
	if d.res.Kind != ir.DeployableWorker {
		if entry.Instances != nil || entry.Concurrency != nil {
			r.fail(CodeInvalidSettings, "%s sets instances or concurrency on %s %s; only a worker takes them, named beside its API's handle with `worker`", where, d.res.Kind, d.res.Name)
			return true
		}
		return false
	}
	if entry.Schedule != "" || entry.TimeZone != "" {
		r.fail(CodeInvalidSettings, "%s sets a schedule or a time zone on worker %s, which runs until it is stopped; only a job takes them", where, d.res.Name)
	}
	s := &d.settings
	if s.workerFrom == nil {
		s.workerFrom = map[string]string{}
	}
	if entry.Instances != nil {
		if *entry.Instances < 0 {
			r.fail(CodeInvalidSettings, "%s instances is %d; it is zero or more", where, *entry.Instances)
		}
		instances := *entry.Instances
		s.instances, s.workerFrom["instances"] = &instances, env
	}
	if entry.Concurrency != nil {
		if *entry.Concurrency < 1 {
			r.fail(CodeInvalidSettings, "%s concurrency is %d; it is one or more", where, *entry.Concurrency)
		}
		concurrency := *entry.Concurrency
		s.concurrency, s.workerFrom["concurrency"] = &concurrency, env
	}
	if entry.Enabled != nil {
		enabled := *entry.Enabled
		s.enabled, s.workerFrom["enabled"] = &enabled, env
	}
	return true
}

// runWorkers decides what each worker runs in the environment (D53): its
// queue, which must be a `@queue` of the database its API connects to, on
// a database the environment runs on Postgres; its concurrency, the
// decorator's unless the settings change it; and its instances, one unless
// the settings change them, and none when they turn it off. Its grace is
// the decorator's.
func (r *resolver) runWorkers() {
	for _, name := range sortedKeys(r.deployables) {
		d := r.deployables[name]
		if d.res.Kind != ir.DeployableWorker || d.worker == nil {
			continue
		}
		run, s := d.res.Worker, d.settings
		if err := registry.CheckWorker(d.worker); err != nil {
			r.fail(CodeInvalidStack, "worker %s of %s: %v", d.worker.Name, run.API, err)
			continue
		}
		db, ok := r.databaseOf(r.services[run.API])
		if !ok {
			r.fail(CodeInvalidStack, "worker %s of %s handles queue %s, and %s connects to no database; a worker's queue is a queue of its API's database, its authDb or its one DB dependency", d.worker.Name, run.API, run.Queue, run.API)
			continue
		}
		if !slices.Contains(r.services[db].Queues, run.Queue) {
			declared := "none"
			if queues := r.services[db].Queues; len(queues) > 0 {
				declared = strings.Join(queues, ", ")
			}
			r.fail(CodeInvalidStack, "worker %s of %s handles queue %s, which is no @queue class of %s, the database %s connects to (its queues: %s)", d.worker.Name, run.API, run.Queue, db, run.API, declared)
			continue
		}
		run.Database = db
		if host := r.deployables[r.byService[db]]; host != nil && host.res.Dialect != "" && host.res.Dialect != registry.SQLDialectPostgres {
			r.fail(CodeUnrealizable, "worker %s claims the messages of queue %s of %s, which environment %s places on %s, a %s database; a worker claims on Postgres, the one dialect of the Go ORM that claims (D53)",
				name, run.Queue, db, r.envName(), host.res.Name, host.res.Dialect)
		}
		run.Concurrency = registry.WorkerConcurrency(d.worker)
		if s.concurrency != nil {
			run.Concurrency = *s.concurrency
		}
		run.Instances = ir.DefaultWorkerInstances
		if s.instances != nil {
			run.Instances = *s.instances
		}
		if s.enabled != nil && !*s.enabled {
			run.Instances = 0
		}
		run.GraceSeconds = registry.WorkerGraceSeconds(d.worker)
	}
}
