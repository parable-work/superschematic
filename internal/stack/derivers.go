package stack

import (
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// The derivers of a stack (D74). Each `@deriver` of a member API service
// is a deployable of kind deriver, named after its API and its class as a
// worker is (ir.DeriverDeployableName). Its edges, its config and its
// identity are its API's, as a worker's are: the resolver treats a deriver
// as it treats a worker wherever a deployable runs code
// (ir.DeployableKind.RunsCode). What is its own is decided here: the
// types whose Derived fields it fills, which must be types of the database
// its API connects to that compose Derived, engine types or tables (D146),
// and whether its one instance runs. It claims from no queue.

// Derivers returns the derivers of in.Stack, which no environment
// changes: a deriver per `@deriver` of every API service the stack
// reaches, sorted by name. Each has its Name, Services (its API), Calls
// (its API's), Language and Deriver's API, Name, Database and Schemas;
// the rest is the environment's to resolve. in.Environment is not read.
// The error is an *Errors with every failure found among the checks that
// decide the derivers.
func Derivers(in Input) ([]*ir.ResolvedDeployable, error) {
	if in.Stack == nil {
		return nil, &Errors{List: []Error{{Code: CodeInvalidStack, Message: "no stack to read derivers from"}}}
	}
	in.Environment = ""
	r := newResolver(nil, in)
	r.indexServices()
	r.collectMembers()
	r.declareDeployables()
	r.defaultDeployables()
	r.defaultWorkers()
	r.defaultDerivers()
	r.resolveCalls()
	r.runDerivers()
	var derivers []*ir.ResolvedDeployable
	for _, name := range sortedKeys(r.deployables) {
		res := r.deployables[name].res
		if res.Kind != ir.DeployableDeriver {
			continue
		}
		res.Language, _ = r.serverLanguage(res)
		derivers = append(derivers, res)
	}
	if r.failed() {
		return nil, r.errs
	}
	return derivers, nil
}

// defaultDerivers makes each deriver of a member API service a deployable
// of its own, named after its API and its class.
func (r *resolver) defaultDerivers() {
	if r.byDeriver == nil {
		r.byDeriver = map[string]string{}
	}
	for _, name := range sortedKeys(r.members) {
		svc := r.services[name]
		if svc.Kind != ir.SchemaKindAPI {
			continue
		}
		for i := range svc.Derivers {
			deriver := &svc.Derivers[i]
			key := name + "/" + deriver.Name
			if _, dup := r.byDeriver[key]; dup {
				r.fail(CodeInvalidStack, "service %s declares deriver %s twice", name, deriver.Name)
				continue
			}
			deriverName := ir.DeriverDeployableName(name, deriver.Name)
			if !namePattern.MatchString(deriverName) {
				r.fail(CodeInvalidStack, "deriver %s of %s would be named %q; a deployable's name is letters, digits, hyphens and underscores", deriver.Name, name, deriverName)
				continue
			}
			if other, clash := r.deployables[deriverName]; clash {
				r.fail(CodeInvalidStack, "deriver %s of %s would be named %s, like the %s %s; rename the deriver or the %s", deriver.Name, name, deriverName, other.res.Kind, deriverName, other.res.Kind)
				continue
			}
			r.deployables[deriverName] = &deployable{
				res: &ir.ResolvedDeployable{
					Name:     deriverName,
					Kind:     ir.DeployableDeriver,
					Services: []ir.ServiceRef{{Name: svc.Name, Kind: svc.Kind}},
					Deriver:  &ir.ResolvedDeriver{API: name, Name: deriver.Name},
				},
				deriver: deriver,
			}
			r.byDeriver[key] = deriverName
		}
	}
}

// resolveDeriverRef finds the deriver a settings `of` names beside its
// API's handle.
func (r *resolver) resolveDeriverRef(where string, ref ir.DeployableRef) (*deployable, bool) {
	switch {
	case ref.Service == nil:
		r.fail(CodeInvalidStack, "%s names deriver %s but no API service; a deriver is named beside its API's handle", where, ref.Deriver)
		return nil, false
	case ref.Job != "" || ref.Worker != "":
		r.fail(CodeInvalidStack, "%s names deriver %s and a job or a worker; an element names one", where, ref.Deriver)
		return nil, false
	}
	if !r.checkRef(where, *ref.Service, ir.SchemaKindAPI) {
		return nil, false
	}
	if !r.members[ref.Service.Name] {
		r.fail(CodeUnknownDeployable, "%s names service %s, which is not in stack %s", where, ref.Service.Name, r.stack.Name)
		return nil, false
	}
	name, ok := r.byDeriver[ref.Service.Name+"/"+ref.Deriver]
	if !ok {
		var derivers []string
		for _, deriver := range r.services[ref.Service.Name].Derivers {
			derivers = append(derivers, deriver.Name)
		}
		declared := "none"
		if len(derivers) > 0 {
			declared = strings.Join(derivers, ", ")
		}
		r.fail(CodeUnknownDeployable, "%s names deriver %s of %s, which declares no such @deriver class (its derivers: %s)", where, ref.Deriver, ref.Service.Name, declared)
		return nil, false
	}
	return r.deployables[name], true
}

// applyDeriverSettings merges a deriver's settings of one element, from
// environment env: instances, zero or one, concurrency and enabled. It
// reports whether d is a deriver, whose element it was; a schedule, a time
// zone, onDeploy or maxInstances on one is refused.
func (r *resolver) applyDeriverSettings(where, env string, d *deployable, entry *ir.DeployableSettings) bool {
	if d.res.Kind != ir.DeployableDeriver {
		return false
	}
	if entry.Schedule != "" || entry.TimeZone != "" {
		r.fail(CodeInvalidSettings, "%s sets a schedule or a time zone on deriver %s, which runs until it is stopped; only a job takes them", where, d.res.Name)
	}
	if entry.OnDeploy != nil {
		r.fail(CodeInvalidSettings, "%s sets onDeploy on deriver %s, which runs until it is stopped; only a job runs on each deploy", where, d.res.Name)
	}
	if entry.MaxInstances != nil {
		r.fail(CodeInvalidSettings, "%s sets maxInstances on deriver %s, which runs one instance or none", where, d.res.Name)
	}
	s := &d.settings
	if s.workerFrom == nil {
		s.workerFrom = map[string]string{}
	}
	if entry.Instances != nil {
		if *entry.Instances < 0 || *entry.Instances > 1 {
			r.fail(CodeInvalidSettings, "%s instances is %d; a deriver runs zero instances or one", where, *entry.Instances)
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

// runDerivers decides what each deriver runs in the environment (D74,
// D146): the database its API connects to, one whose types compose
// Derived; the types whose Derived fields it fills, those its decorator
// names, each an engine type or a table of that database that composes
// Derived, or every such type when it names none, and at least one; its
// concurrency and batch size, the decorator's unless the settings change
// the concurrency; and its instances, one unless the settings turn it off.
// A deriver fills engine types through the engine, or tables through
// their repositories, not both: one whose schemas are of both kinds is
// refused, and so is one that names none over a database that has both. A
// deriver of tables runs on a TypeScript, Rust or Go API (D146), and one of
// engine types on a TypeScript or Rust API: a Go API's deriver of engine
// types is refused.
func (r *resolver) runDerivers() {
	for _, name := range sortedKeys(r.deployables) {
		d := r.deployables[name]
		if d.res.Kind != ir.DeployableDeriver || d.deriver == nil {
			continue
		}
		run, s := d.res.Deriver, d.settings
		if err := registry.CheckDeriver(d.deriver); err != nil {
			r.fail(CodeInvalidStack, "deriver %s of %s: %v", d.deriver.Name, run.API, err)
			continue
		}
		lang := r.services[run.API].Language
		if lang == "" {
			lang = registry.APILanguageGo
		}
		db, ok := r.connectsQuietly(r.services[run.API])
		if !ok || (!r.services[db].Engine && len(r.services[db].DerivedTables) == 0) {
			r.fail(CodeInvalidStack, "deriver %s of %s fills Derived fields, and %s connects to no database whose types compose Derived; a deriver's schemas are engine types or tables of its API's database, its authDb or its one DB dependency", d.deriver.Name, run.API, run.API)
			continue
		}
		run.Database = db
		engineTypes, tables := r.services[db].Derived, r.services[db].DerivedTables
		derived := slices.Concat(engineTypes, tables)
		slices.Sort(derived)
		run.Schemas = nil
		unknown := false
		for _, schema := range d.deriver.Schemas {
			if !slices.Contains(derived, schema) {
				declared := "none"
				if len(derived) > 0 {
					declared = strings.Join(derived, ", ")
				}
				r.fail(CodeInvalidStack, "deriver %s of %s fills the derived fields of %s, which is no engine type or table of %s that composes Derived (its types that do: %s)", d.deriver.Name, run.API, schema, db, declared)
				unknown = true
				continue
			}
			run.Schemas = append(run.Schemas, schema)
		}
		if unknown {
			continue
		}
		if len(d.deriver.Schemas) == 0 {
			run.Schemas = slices.Clone(derived)
			if len(run.Schemas) == 0 {
				r.fail(CodeInvalidStack, "deriver %s of %s fills the derived fields of %s, and no type of %s composes Derived; compose Derived on a type, or remove the deriver", d.deriver.Name, run.API, db, db)
				continue
			}
		}
		var ofEngine, ofTables []string
		for _, schema := range run.Schemas {
			if slices.Contains(tables, schema) {
				ofTables = append(ofTables, schema)
			} else {
				ofEngine = append(ofEngine, schema)
			}
		}
		if len(ofEngine) > 0 && len(ofTables) > 0 {
			r.fail(CodeInvalidStack, "deriver %s of %s fills the derived fields of %s, engine types of %s, and of %s, tables of it; a deriver fills engine types through the engine or tables through their repositories, so declare a deriver for each and name its schemas (D146)", d.deriver.Name, run.API, strings.Join(ofEngine, ", "), db, strings.Join(ofTables, ", "))
			continue
		}
		if len(ofEngine) > 0 && lang == registry.APILanguageGo {
			r.fail(CodeUnrealizable, "deriver %s of %s fills the derived fields of %s, engine types of %s, and %s is a Go API; %s; write the API in TypeScript or Rust", d.deriver.Name, run.API, strings.Join(ofEngine, ", "), db, run.API, DeriverLanguages)
			continue
		}
		run.Concurrency = registry.DeriverConcurrency(d.deriver)
		if s.concurrency != nil {
			run.Concurrency = *s.concurrency
		}
		run.BatchSize = registry.DeriverBatchSize(d.deriver)
		run.Instances = ir.DefaultDeriverInstances
		if s.instances != nil {
			run.Instances = *s.instances
		}
		if s.enabled != nil && !*s.enabled {
			run.Instances = 0
		}
	}
}

// DeriverLanguages says which languages a deriver runs in (D74, D146), for
// the refusals of a Go API's deriver of engine types.
const DeriverLanguages = "a deriver of engine types runs on a TypeScript or Rust API, and a Go API's deriver fills tables alone (D146)"
