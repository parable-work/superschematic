package stack

import (
	"fmt"
	"slices"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// slot is where a resource lands in the deploy order: a phase, and for
// the rollout phase a wave.
type slot struct {
	phase ir.ResourcePhase
	wave  int
}

func (s slot) after(o slot) bool {
	if s.phase.Rank() != o.phase.Rank() {
		return s.phase.Rank() > o.phase.Rank()
	}
	return s.phase == ir.PhaseRollout && s.wave > o.wave
}

// order writes the deploy order of section 5.3 onto out: infrastructure;
// the migrations' expand steps; the servers and jobs in waves, callees
// before callers; the migrations' contract steps; exposure. A migrate step
// names the databases and holds no resource: the deploy runs the migration
// there. A resource lands in its phase, or in a later step when one of its
// dependencies does, and its Phase is set to where it landed; a server's
// or a job's own rollout resources land in its wave, and a database's
// resources land in infrastructure, before the migration that needs them.
// An inherited resource is the parent environment's: no step applies it,
// and nothing waits on it.
func (r *resolver) order(out *ir.ResolvedEnvironment) {
	waves := r.rolloutWaves
	graph := out.Resources
	slots := map[string]slot{}
	var slotOf func(res *ir.Resource) slot
	slotOf = func(res *ir.Resource) slot {
		if s, ok := slots[res.ID]; ok {
			return s
		}
		s := slot{phase: res.Phase}
		if res.Inherited {
			s = slot{phase: ir.PhaseInfrastructure}
			slots[res.ID] = s
			return s
		}
		if s.phase == ir.PhaseRollout {
			s.wave = 1
			first := true
			for _, owner := range res.Owners {
				if w, ok := waves[owner]; ok && (first || w < s.wave) {
					s.wave, first = w, false
				}
			}
		}
		for _, dep := range res.DependsOn {
			if ds := slotOf(graph.Resource(dep)); ds.after(s) {
				s = ds
			}
		}
		slots[res.ID] = s
		return s
	}
	for _, res := range graph.Resources {
		s := slotOf(res)
		landed := string(s.phase)
		if s.phase == ir.PhaseRollout {
			landed = fmt.Sprintf("rollout wave %d", s.wave)
		}
		for _, owner := range res.Owners {
			if d := out.Deployable(owner); d != nil && d.Kind == ir.DeployableDatabase && !res.Inherited && s.phase != ir.PhaseInfrastructure {
				r.fail(CodeGraph, "resource %s of database %s lands in %s, after the migration that needs it", res.ID, owner, landed)
				continue
			}
			w, rolls := waves[owner]
			if !rolls || res.Phase != ir.PhaseRollout || !s.after(slot{phase: ir.PhaseRollout, wave: w}) {
				continue
			}
			kind := ir.DeployableServer
			if d := out.Deployable(owner); d != nil {
				kind = d.Kind
			}
			r.fail(CodeGraph, "resource %s of %s %s lands in %s through its dependencies, after the %s's rollout wave %d", res.ID, kind, owner, landed, kind, w)
		}
	}
	if r.failed() {
		return
	}

	for _, res := range graph.Resources {
		if !res.Inherited {
			res.Phase = slots[res.ID].phase
		}
	}

	var databases []string
	maxWave := 0
	for _, d := range out.Deployables {
		if d.Kind == ir.DeployableDatabase {
			databases = append(databases, d.Name)
		}
		maxWave = max(maxWave, waves[d.Name])
	}
	byPhase := map[slot][]string{}
	for _, res := range graph.Resources {
		if res.Inherited {
			continue
		}
		s := slots[res.ID]
		byPhase[s] = append(byPhase[s], res.ID)
		if s.phase == ir.PhaseRollout {
			maxWave = max(maxWave, s.wave)
		}
	}

	var steps []*ir.DeployStep
	if ids := byPhase[slot{phase: ir.PhaseInfrastructure}]; len(ids) > 0 {
		steps = append(steps, &ir.DeployStep{Step: ir.StepInfrastructure, Resources: ids})
	}
	if len(databases) > 0 {
		steps = append(steps, &ir.DeployStep{Step: ir.StepMigrate, Migration: ir.MigrationExpand, Deployables: databases})
	}
	for wave := 1; wave <= maxWave; wave++ {
		step := &ir.DeployStep{Step: ir.StepRollout, Wave: wave, Resources: byPhase[slot{phase: ir.PhaseRollout, wave: wave}]}
		for _, d := range out.Deployables {
			if waves[d.Name] == wave {
				step.Deployables = append(step.Deployables, d.Name)
			}
		}
		if len(step.Deployables) > 0 || len(step.Resources) > 0 {
			steps = append(steps, step)
		}
	}
	if len(databases) > 0 {
		steps = append(steps, &ir.DeployStep{Step: ir.StepMigrate, Migration: ir.MigrationContract, Deployables: databases})
	}
	if ids := byPhase[slot{phase: ir.PhaseExposure}]; len(ids) > 0 {
		steps = append(steps, &ir.DeployStep{Step: ir.StepExposure, Resources: ids})
	}
	if steps == nil {
		steps = []*ir.DeployStep{}
	}
	out.DeployOrder = steps
}

// orderRollout numbers each server's and each job's rollout wave from 1:
// a server rolls out one wave after the latest of its callees, so a new
// caller never meets an old callee, and a job rolls out after its callees
// as a server does (D52), and so does a site, whose config names the
// public addresses of the servers it calls (D55). A server's calls to the
// APIs it serves itself do not order it. A cycle of calls between servers
// has no such order and fails.
func (r *resolver) orderRollout() {
	callees := map[string][]string{}
	for _, id := range sortedKeys(r.edges) {
		if e := r.edges[id].res; (e.Kind == ir.EdgeHTTP || e.Kind == ir.EdgeSite) && e.From != e.To {
			callees[e.From] = append(callees[e.From], e.To)
		}
	}
	waves := map[string]int{}
	visiting := map[string]bool{}
	cycle := false
	var path []string
	var visit func(name string) int
	visit = func(name string) int {
		if w, ok := waves[name]; ok {
			return w
		}
		if visiting[name] {
			start := slices.Index(path, name)
			names := append(slices.Clone(path[start:]), name)
			r.fail(CodeCallCycle, "servers call each other in a cycle, so no order rolls out callees first: %s", strings.Join(names, " -> "))
			cycle = true
			return 0
		}
		visiting[name] = true
		path = append(path, name)
		wave := 1
		for _, callee := range callees[name] {
			wave = max(wave, visit(callee)+1)
		}
		path = path[:len(path)-1]
		visiting[name] = false
		waves[name] = wave
		return wave
	}
	for _, name := range sortedKeys(r.deployables) {
		if r.deployables[name].res.Kind.RollsOut() && !cycle {
			visit(name)
		}
	}
	r.rolloutWaves = waves
}
