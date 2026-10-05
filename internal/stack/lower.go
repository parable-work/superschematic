package stack

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// produced is a resource some producer returned, before resources are
// merged into the graph.
type produced struct {
	owner    string
	phase    ir.ResourcePhase
	resource *ir.Resource
}

// produce records the resources a producer returned. phase is the
// producer's default for a resource that names none.
func (r *resolver) produce(owner string, phase ir.ResourcePhase, resources []*ir.Resource) {
	for _, res := range resources {
		if res == nil {
			continue
		}
		r.produced = append(r.produced, produced{owner: owner, phase: phase, resource: res})
	}
}

// lower asks each deployable's platform for its resources and its DNS
// records, then the DNS platform for the records' resources, and merges
// everything into the graph.
func (r *resolver) lower(out *ir.ResolvedEnvironment) {
	var records []*ir.DNSRecord
	for _, name := range sortedKeys(r.deployables) {
		d := r.deployables[name]
		ctx := registry.PlatformContext{Environment: r.stackEnvironment(), Deployable: cloneDeployable(d.res)}
		lowered, err := d.platform.Lower(ctx)
		if err != nil {
			r.fail(CodeLowering, "platform %s lowering %s: %v", d.platform.Name, name, err)
			continue
		}
		phase := ir.PhaseInfrastructure
		if d.res.Kind == ir.DeployableServer {
			phase = ir.PhaseRollout
		}
		r.produce(name, phase, lowered.Resources)
		if len(lowered.Records) == 0 {
			continue
		}
		switch {
		case !d.res.Exposed:
			r.fail(CodeLowering, "platform %s returned DNS records for %s, which is not exposed", d.platform.Name, name)
			continue
		case r.env.domain == "":
			r.fail(CodeLowering, "platform %s returned DNS records for %s, but environment %s sets no domain", d.platform.Name, name, r.envName())
			continue
		}
		for _, rec := range lowered.Records {
			if rec == nil {
				continue
			}
			copied := *rec
			copied.Deployable = name
			r.checkParameters(fmt.Sprintf("platform %s names a DNS record of %s with", d.platform.Name, name), copied.Name)
			records = append(records, &copied)
		}
	}
	r.lowerDNS(out, records)
	if r.failed() {
		return
	}
	r.mergeResources(out)
}

// lowerDNS places the environment's records on its DNS platform: the one
// the environment names, else the target's default, else ir.ManualDNS,
// whose records nothing writes (section 6.9).
func (r *resolver) lowerDNS(out *ir.ResolvedEnvironment, records []*ir.DNSRecord) {
	if r.env.domain == "" {
		return
	}
	dns := &ir.ResolvedDNS{Platform: r.target.DNS, Records: records}
	if r.env.dns != nil {
		dns.Platform = r.env.dns.Platform
		if len(r.env.dns.Values) > 0 {
			dns.Values = deepCopy(r.env.dns.Values).(map[string]any)
		}
	}
	if dns.Platform == "" {
		dns.Platform = ir.ManualDNS
	}
	out.DNS = dns
	if dns.Platform == ir.ManualDNS || len(records) == 0 {
		return
	}
	spec, _ := r.reg.DNSPlatform(dns.Platform)
	ctx := registry.DNSContext{Environment: r.stackEnvironment(), Values: deepCopy(dns.Values).(map[string]any)}
	if ctx.Values == nil {
		ctx.Values = map[string]any{}
	}
	for _, rec := range records {
		copied := *rec
		copied.Name = deepCopy(rec.Name)
		copied.Value = deepCopy(rec.Value)
		ctx.Records = append(ctx.Records, copied)
	}
	resources, err := spec.Lower(ctx)
	if err != nil {
		r.fail(CodeLowering, "DNS platform %s: %v", spec.Name, err)
		return
	}
	r.produce("dns", ir.PhaseExposure, resources)
}

// mergeResources builds the graph from the produced resources. Two
// producers that return the same node share it; two that return different
// nodes under one ID fail. Each node's dependencies become the ones its
// producer named plus every node its properties reference.
func (r *resolver) mergeResources(out *ir.ResolvedEnvironment) {
	byID := map[string]*ir.Resource{}
	for _, p := range r.produced {
		src := p.resource
		if src.ID == "" || src.Type == "" {
			r.fail(CodeGraph, "%s produced a resource without an ID or a type", p.owner)
			continue
		}
		res := &ir.Resource{
			ID:        src.ID,
			Type:      src.Type,
			Phase:     src.Phase,
			Inherited: src.Inherited,
			Owners:    []string{p.owner},
		}
		if res.Phase == "" {
			res.Phase = p.phase
		}
		if len(src.Properties) > 0 {
			res.Properties = deepCopy(src.Properties).(map[string]any)
		}
		deps := slices.Clone(src.DependsOn)
		outputs, _ := ir.ValueRefs(res.Properties)
		for _, o := range outputs {
			deps = append(deps, o.Resource)
		}
		slices.Sort(deps)
		res.DependsOn = slices.Compact(deps)
		prev, ok := byID[res.ID]
		if !ok {
			byID[res.ID] = res
			continue
		}
		if !sameResource(prev, res) {
			r.fail(CodeGraph, "resource %s: %s and %s produce it differently", res.ID, strings.Join(prev.Owners, ", "), p.owner)
			continue
		}
		if !slices.Contains(prev.Owners, p.owner) {
			prev.Owners = append(prev.Owners, p.owner)
			slices.Sort(prev.Owners)
		}
	}
	for _, id := range sortedKeys(byID) {
		out.Resources.Resources = append(out.Resources.Resources, byID[id])
	}
}

func sameResource(a, b *ir.Resource) bool {
	if a.Type != b.Type || a.Phase != b.Phase || a.Inherited != b.Inherited || !slices.Equal(a.DependsOn, b.DependsOn) {
		return false
	}
	pa, errA := json.Marshal(a.Properties)
	pb, errB := json.Marshal(b.Properties)
	return errA == nil && errB == nil && bytes.Equal(pa, pb)
}

// checkGraph runs the graph checks of validation level 3 (section 10):
// every dependency and referenced output names a node, every referenced
// parameter is declared, the graph has no cycle, every node's properties
// validate against the schema of its type, and every inherited node is a
// node of the parent environment.
func (r *resolver) checkGraph(out *ir.ResolvedEnvironment) {
	graph := out.Resources
	ids := map[string]*ir.Resource{}
	for _, res := range graph.Resources {
		ids[res.ID] = res
	}
	checkOutputs := func(where string, v any) {
		outputs, _ := ir.ValueRefs(v)
		for _, o := range outputs {
			if _, ok := ids[o.Resource]; !ok {
				r.fail(CodeGraph, "%s references output %s of resource %s, which no platform, connector or DNS platform produced", where, o.Name, o.Resource)
			}
		}
	}
	for _, res := range graph.Resources {
		if res.Phase.Rank() < 0 {
			r.fail(CodeGraph, "resource %s has phase %q (want %s, %s or %s)", res.ID, res.Phase, ir.PhaseInfrastructure, ir.PhaseRollout, ir.PhaseExposure)
		}
		for _, dep := range res.DependsOn {
			if dep == res.ID {
				r.fail(CodeGraph, "resource %s depends on itself", res.ID)
			} else if _, ok := ids[dep]; !ok {
				r.fail(CodeGraph, "resource %s depends on %s, which no platform, connector or DNS platform produced", res.ID, dep)
			}
		}
		r.checkParameters(fmt.Sprintf("resource %s references", res.ID), res.Properties)
		ok, err := r.reg.ValidateResource(res)
		switch {
		case !ok:
			r.fail(CodeGraph, "resource %s has type %s, which no registered target has a schema for", res.ID, res.Type)
		case err != nil:
			r.fail(CodeGraph, "resource %s (%s): %v", res.ID, res.Type, err)
		}
		if res.Inherited {
			r.checkInherited(res)
		}
	}
	for _, d := range out.Deployables {
		checkOutputs(fmt.Sprintf("the address of %s", d.Name), d.Address)
		checkOutputs(fmt.Sprintf("the name of %s", d.Name), d.ResourceName)
		for _, b := range d.Bindings {
			checkOutputs(fmt.Sprintf("binding %s of %s", b.Field, d.Name), b.Value)
		}
	}
	if out.DNS != nil {
		for _, rec := range out.DNS.Records {
			checkOutputs(fmt.Sprintf("a DNS record of %s", rec.Deployable), []any{rec.Name, rec.Value})
		}
	}
	if cycle := findCycle(graph); cycle != nil {
		r.fail(CodeGraph, "resources depend on each other in a cycle: %s", strings.Join(cycle, " -> "))
	}
}

// checkInherited checks that an inherited node is a node of the same type
// in the parent environment's graph.
func (r *resolver) checkInherited(res *ir.Resource) {
	env := r.env.chain[len(r.env.chain)-1]
	if env.Extends == "" {
		r.fail(CodeGraph, "resource %s is inherited, but environment %s extends no environment", res.ID, env.Name)
		return
	}
	parent, err := r.parent()
	if err != nil {
		r.fail(CodeGraph, "resource %s is inherited from environment %s, which does not resolve: %v", res.ID, env.Extends, err)
		return
	}
	theirs := parent.Resources.Resource(res.ID)
	switch {
	case theirs == nil:
		r.fail(CodeGraph, "resource %s is inherited, but environment %s has no such resource", res.ID, env.Extends)
	case theirs.Type != res.Type:
		r.fail(CodeGraph, "resource %s is inherited as a %s, but in environment %s it is a %s", res.ID, res.Type, env.Extends, theirs.Type)
	}
}

// parent resolves the parent environment once.
func (r *resolver) parent() (*ir.ResolvedEnvironment, error) {
	if r.parentEnv == nil && r.parentErr == nil {
		env := r.env.chain[len(r.env.chain)-1]
		r.parentEnv, r.parentErr = Resolve(r.reg, Input{Stack: r.stack, Services: r.in.Services, Environment: env.Extends})
	}
	return r.parentEnv, r.parentErr
}

// findCycle returns the IDs of one dependency cycle, the first repeated at
// the end, or nil.
func findCycle(graph *ir.ResourceGraph) []string {
	const (
		unvisited = iota
		visiting
		done
	)
	state := map[string]int{}
	var stack []string
	var cycle []string
	var visit func(id string) bool
	visit = func(id string) bool {
		state[id] = visiting
		stack = append(stack, id)
		res := graph.Resource(id)
		if res != nil {
			for _, dep := range res.DependsOn {
				if graph.Resource(dep) == nil {
					continue
				}
				switch state[dep] {
				case visiting:
					start := slices.Index(stack, dep)
					cycle = append(slices.Clone(stack[start:]), dep)
					return true
				case unvisited:
					if visit(dep) {
						return true
					}
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[id] = done
		return false
	}
	for _, res := range graph.Resources {
		if state[res.ID] == unvisited && visit(res.ID) {
			return cycle
		}
	}
	return nil
}

// checkPolicies runs the target's policy rules over the resolved
// environment.
func (r *resolver) checkPolicies(out *ir.ResolvedEnvironment) {
	for _, rule := range r.target.Policies {
		for _, msg := range rule.Check(out) {
			r.fail(CodePolicy, "target %s policy %s: %s", r.target.Name, rule.Name, msg)
		}
	}
}
