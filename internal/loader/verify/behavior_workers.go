package verify

import (
	"encoding/json"
	"strings"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// CheckBehaviorWorkers runs the checks of the API's @behaviorWorker
// classes alone (checkBehaviorWorkers), which Run runs among the rest. The
// loader runs it before it gives each its table's queue
// (behaviorops.ExpandWorkers), which it runs before Run, so the worker
// meets every check an authored worker meets.
func CheckBehaviorWorkers(schema *ir.Schema, in Input) *Result {
	r := &Result{}
	checkBehaviorWorkers(schema, in, r)
	return r
}

// checkBehaviorWorkers validates the API's @behaviorWorker classes (D103):
// each is in an API, names no queue of its own, and names a table of the
// API's database (its authDb, or its one DB dependency, in.Database) that
// the API imports from it, which is a table and no engine type, and whose
// behaviors arm deadlines or hear its writes, so the database holds a queue
// the worker runs; and an API runs a table's behaviors from one worker. Run runs it
// again after the loader filled each worker's queue, which it does not
// refuse.
func checkBehaviorWorkers(schema *ir.Schema, in Input, r *Result) {
	var workers []*ir.Worker
	for _, worker := range schema.Workers {
		if worker != nil && worker.BehaviorWorker != nil {
			workers = append(workers, worker)
		}
	}
	if len(workers) == 0 {
		return
	}
	if schema.Kind != ir.SchemaKindAPI {
		for _, worker := range workers {
			r.errorf("", "%s: @behaviorWorker is only allowed in an API schema (this schema has kind %s)", worker.Name, schema.Kind)
		}
		return
	}
	var formed []*ir.Worker
	for _, worker := range workers {
		if worker.Queue != "" && worker.Origin != ir.OriginBehavior {
			r.errorf("", "@behaviorWorker %s names queue %s: a behavior worker runs its table's queues, which the build adds (D103)", worker.Name, worker.Queue)
			continue
		}
		formed = append(formed, worker)
	}
	if len(formed) == 0 {
		return
	}
	db, _, ok := schema.Database()
	if !ok {
		var deps int
		for _, dep := range schema.Dependencies {
			if dep.Kind == ir.SchemaKindDB {
				deps++
			}
		}
		for _, worker := range formed {
			typeName := worker.BehaviorWorker.Type
			if deps > 1 {
				r.errorf("", "%s: @behaviorWorker(%s) runs %s's behaviors on the API's database, and %s names %d DB dependencies and no authDb: set its authDb to the one that declares %s", worker.Name, typeName, typeName, schema.Name, deps, typeName)
				continue
			}
			r.errorf("", "%s: @behaviorWorker(%s) runs %s's behaviors on the API's database, and %s connects to none: set its authDb, or give it one DB dependency, the DB service that declares %s", worker.Name, typeName, typeName, schema.Name, typeName)
		}
		return
	}
	reg := in.registry()
	run := map[string]string{}
	for _, worker := range formed {
		typeName := worker.BehaviorWorker.Type
		if !workerTableImportedFrom(schema, worker, db, r) || in.Database == nil {
			continue
		}
		td := in.Database.Types[typeName]
		switch {
		case td == nil:
			r.errorf("", "%s: @behaviorWorker names %s, which %s does not declare", worker.Name, typeName, in.Database.Name)
			continue
		case td.Role != ir.RoleDBTable || td.JsonField:
			r.errorf("", "%s: @behaviorWorker names %s, which is not a table of %s: a behavior worker runs a table's behaviors (D103)", worker.Name, typeName, in.Database.Name)
			continue
		case td.IsEngineType():
			r.errorf("", "%s: @behaviorWorker names %s, an engine type of %s (D58), whose engine runs its timed work; a behavior worker runs a table's behaviors (D103)", worker.Name, typeName, in.Database.Name)
			continue
		case !runsDeadlines(td, in.Database, reg) && in.Database.BehaviorQueueOf(typeName, ir.BehaviorQueueChanges) == nil:
			r.errorf("", "%s: @behaviorWorker names %s, whose behaviors arm no deadlines and hear no writes, so the worker has nothing to run (D103)", worker.Name, typeName)
			continue
		}
		if other, taken := run[typeName]; taken {
			r.errorf("", "%s: @behaviorWorker names %s, which %s names too: an API runs a table's behaviors from one worker", worker.Name, typeName, other)
			continue
		}
		run[typeName] = worker.Name
		warnReleaseLeasesUnheld(schema, worker, td, in.Database, r)
	}
}

// warnReleaseLeasesUnheld warns, never refuses, when worker runs the misses
// of td's Presence, whose releaseLeases names a table whose Lease names an
// overridePermission the worker's permissions do not cover (D104): a miss
// expires the principal's leases there through Lease's expire, which checks
// that permission against the deadline's caller, the worker, so without it
// every miss fails and the worker releases it to fall due again.
func warnReleaseLeasesUnheld(schema *ir.Schema, worker *ir.Worker, td *ir.TypeDef, db *ir.Schema, r *Result) {
	config, ok := implementedConfig(td, presenceBehavior)
	if !ok {
		return
	}
	var presence struct {
		ReleaseLeases []string `json:"releaseLeases"`
	}
	if len(config) == 0 || json.Unmarshal(config, &presence) != nil {
		return
	}
	for _, name := range presence.ReleaseLeases {
		leased := db.Types[name]
		if leased == nil {
			continue
		}
		lease, ok := implementedConfig(leased, leaseBehavior)
		if !ok {
			continue
		}
		permission := leaseOverridePermission(lease)
		if permission == "" || coveredBy(permission, worker.BehaviorWorker.Permissions) {
			continue
		}
		r.warnf("", "%s: %s runs %s's misses, which expire the leases %s's Presence releases on %s through Lease's expire, which needs %s's Lease overridePermission %s: list it in the worker's permissions, or every miss fails (D104)", schema.Name, worker.Name, td.Name, td.Name, name, name, permission)
	}
}

// runsDeadlines reports whether td's behaviors arm deadlines and db holds
// their queue.
func runsDeadlines(td *ir.TypeDef, db *ir.Schema, reg *registry.Registry) bool {
	return len(armingBehaviors(td, reg)) > 0 && db.BehaviorQueueOf(td.Name, ir.BehaviorQueueDeadlines) != nil
}

// workerTableImportedFrom reports whether the API imports worker's table
// from db, its database, refusing the worker when it does not.
func workerTableImportedFrom(schema *ir.Schema, worker *ir.Worker, db string, r *Result) bool {
	typeName := worker.BehaviorWorker.Type
	from := importedFromService(schema, typeName, db)
	switch from {
	case db:
		return true
	case "":
		r.errorf("", "%s: @behaviorWorker names %s, which %s does not import; import it from %s, its database", worker.Name, typeName, schema.Name, db)
	default:
		r.errorf("", "%s: @behaviorWorker names %s, which %s imports from %s, not from %s, its database (its authDb, or its one DB dependency)", worker.Name, typeName, schema.Name, from, db)
	}
	return false
}

// importedFromService is the service schema imports typeName from: db when one of
// its imports of typeName is from db, else the first service one is from,
// else empty.
func importedFromService(schema *ir.Schema, typeName, db string) string {
	from := ""
	for _, imp := range schema.Imports {
		for _, name := range imp.Types {
			if name != typeName {
				continue
			}
			service := imp.Package
			if i := strings.LastIndex(service, "/"); i >= 0 {
				service = service[i+1:]
			}
			if service == db {
				return db
			}
			if from == "" {
				from = service
			}
		}
	}
	return from
}
