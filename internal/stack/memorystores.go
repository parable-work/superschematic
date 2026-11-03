package stack

import (
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/tsutil"
	ir "github.com/parable-work/superschematic/ir"
)

// The memory stores of a stack (D75). Each MemoryStore service an API
// lists in its memoryStores, or names its rateLimitStore, is a deployable
// of kind memoryStore, named after the service, and the server, jobs and
// workers of each such API reach it over a memoryStore edge, whose field
// (`{SERVICE}_MEMORY_STORE`) holds an ir.MemoryStoreConnection. A
// server's stores are the union of its APIs', so two APIs of one server
// that list one store get one edge.
//
// An environment may turn a store off: `settings: [{ of: ShopCache,
// enabled: false }]`. Such a store is no deployable of the environment:
// no platform places it, so a target with no memoryStore platform resolves
// it, no edge reaches it, so no workload's field is set and each opens the
// store disabled, and the environment lists it in Disabled.

// applyMemoryStoreSettings merges a memory store's settings of one
// element: enabled, which turns it on or off. It reports whether d is a
// memory store, whose element it was; a job's or a worker's settings on
// one are refused.
func (r *resolver) applyMemoryStoreSettings(where, env string, d *deployable, entry *ir.DeployableSettings) bool {
	if d.res.Kind != ir.DeployableMemoryStore {
		return false
	}
	if entry.Schedule != "" || entry.TimeZone != "" || entry.OnDeploy != nil || entry.Instances != nil || entry.MaxInstances != nil || entry.Concurrency != nil {
		r.fail(CodeInvalidSettings, "%s sets a job's or a worker's settings on memory store %s, which takes enabled and its platform's settings alone", where, d.res.Name)
	}
	if entry.Enabled != nil {
		enabled := *entry.Enabled
		d.settings.enabled = &enabled
		if d.settings.scheduleFrom == nil {
			d.settings.scheduleFrom = map[string]string{}
		}
		d.settings.scheduleFrom["enabled"] = env
	}
	return true
}

// disableDeployables takes each memory store the environment turns off
// out of the deployables, before any is placed, and records it in
// disabled. Platform settings beside `enabled: false` have no platform to
// go to, and are refused.
func (r *resolver) disableDeployables() {
	for _, name := range sortedKeys(r.deployables) {
		d := r.deployables[name]
		if d.res.Kind != ir.DeployableMemoryStore || d.settings.enabled == nil || *d.settings.enabled {
			continue
		}
		from := d.settings.scheduleFrom["enabled"]
		if d.settings.platform != "" || len(d.settings.values) > 0 {
			r.fail(CodeInvalidSettings, "environment %s turns memory store %s off and gives it a platform or settings; a store that is off is placed nowhere", from, name)
		}
		r.disabled[name] = &ir.DisabledDeployable{
			Name:     name,
			Kind:     d.res.Kind,
			Services: slices.Clone(d.res.Services),
			From:     from,
		}
		delete(r.deployables, name)
		for service, owner := range r.byService {
			if owner == name {
				delete(r.byService, service)
			}
		}
	}
}

// resolveMemoryStores gives each server, job and worker the memory stores
// it reaches: the union of the memoryStores of the APIs it serves, sorted
// by name, less those the environment turns off.
func (r *resolver) resolveMemoryStores() {
	for _, name := range sortedKeys(r.deployables) {
		d := r.deployables[name]
		if !d.res.Kind.RunsCode() {
			continue
		}
		seen := map[string]bool{}
		for _, served := range d.res.Services {
			for _, ref := range r.services[served.Name].MemoryStores {
				svc, ok := r.services[ref.Name]
				if !ok || svc.Kind != ir.SchemaKindMemoryStore || ref.Kind != svc.Kind || seen[ref.Name] {
					continue // collectMembers reported a bad handle
				}
				seen[ref.Name] = true
				if r.disabled[ref.Name] != nil {
					continue
				}
				d.res.MemoryStores = append(d.res.MemoryStores, ir.ServiceRef{Name: svc.Name, Kind: svc.Kind})
			}
		}
		slices.SortFunc(d.res.MemoryStores, func(a, b ir.ServiceRef) int { return strings.Compare(a.Name, b.Name) })
	}
}

// tsHandle is the name a service's handle takes in a schema file, as its
// sentinel exports it (`ShopCache` for shop-cache).
func tsHandle(service string) string {
	return tsutil.ToClassName(service)
}
