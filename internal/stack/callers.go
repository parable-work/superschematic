package stack

import (
	"fmt"
	"slices"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// The callee's side of service auth (D37; docs/stack-model.md, section
// 9.2). A served API with a service clause gets a callers field, named by
// ir.CallersField, whose value is how its server verifies a caller's
// service credential: the connector of each http edge to the API from
// another server returns an issuer listing the edge's caller
// (registry.Connected.Callee), and the resolver merges those entries by
// issuer into an ir.ServiceAuth. An API no other server calls gets the
// field with no issuers, so its server starts and refuses every service
// credential. A call within one server stays on loopback and carries no
// credential, so it adds no entry.

// hasServiceClause reports whether an operation of svc has a service
// clause, its own or its set's.
func hasServiceClause(svc *Service) bool {
	return slices.ContainsFunc(svc.Operations, func(op Operation) bool { return op.ServiceCallers != nil })
}

// callersFields returns the callers fields of a server: one per API it
// serves that has a service clause, by field name.
func (r *resolver) callersFields(d *deployable) map[string]string {
	fields := map[string]string{}
	for _, served := range d.res.Services {
		if svc := r.services[served.Name]; svc != nil && hasServiceClause(svc) {
			fields[ir.CallersField(svc.Name)] = svc.Name
		}
	}
	return fields
}

// checkCallersFields refuses a callers field of server d that a config
// field, an edge's derived field or an env key of the environment takes:
// its name, or a name it begins with and an underscore, either way round.
// It reports the env keys it refused, which bindConfig then skips.
func (r *resolver) checkCallersFields(d *deployable, callers map[string]string, derived map[string]*edge) map[string]bool {
	refused := map[string]bool{}
	for _, name := range sortedKeys(callers) {
		api := callers[name]
		for _, fieldName := range sortedKeys(d.fields) {
			if f := d.fields[fieldName]; ir.DerivedFieldClaims(name, f.name) {
				r.fail(CodeFieldCollision, "server %s: config field %s of %s collides with %s, the callers field of %s", d.res.Name, f.name, f.declaring, name, api)
			}
		}
		for _, other := range sortedKeys(derived) {
			if ir.DerivedFieldClaims(name, other) || ir.DerivedFieldClaims(other, name) {
				r.fail(CodeFieldCollision, "server %s: %s, the field edge %s derives, collides with %s, the callers field of %s", d.res.Name, other, derived[other].res.ID, name, api)
			}
		}
		if _, set := d.settings.env[name]; set {
			r.fail(CodeUnknownEnvKey, "environment %s sets env %s on server %s, the callers field of %s, which the http edges to %s derive", d.settings.envFrom[name], name, d.res.Name, api, api)
			refused[name] = true
		}
	}
	return refused
}

// callersBindings binds each callers field of server d to the http edges
// to its API from other servers, whose values connectEdges merges.
func (r *resolver) callersBindings(d *deployable, callers map[string]string) []*ir.Binding {
	var out []*ir.Binding
	for _, name := range sortedKeys(callers) {
		api := callers[name]
		b := &ir.Binding{Field: name, Source: ir.BindingDerived, CallersOf: api}
		for _, id := range sortedKeys(r.edges) {
			e := r.edges[id]
			if e.res.Kind == ir.EdgeHTTP && e.res.Service.Name == api && e.res.To == d.res.Name && e.res.From != d.res.Name {
				b.Edges = append(b.Edges, id)
			}
		}
		out = append(out, b)
	}
	return out
}

// checkCallee checks what a connector gives the callee of edge e: an
// issuer of a callers field whose one caller is the edge's caller, with
// the APIs it serves.
func checkCallee(e *edge, callee any) error {
	if err := ir.CheckServiceAuthIssuer(callee); err != nil {
		return err
	}
	callers, _ := callee.(map[string]any)["callers"].([]any)
	if len(callers) != 1 {
		return fmt.Errorf("it lists %d callers; an edge's entry lists the edge's caller, %s, alone", len(callers), e.from.res.Name)
	}
	caller := callers[0].(map[string]any)
	if got := caller["deployable"]; got != e.from.res.Name {
		return fmt.Errorf("its caller is %v, not the edge's caller %s", got, e.from.res.Name)
	}
	var serves []string
	for _, ref := range e.from.res.Services {
		serves = append(serves, ref.Name)
	}
	slices.Sort(serves)
	var got []string
	for _, api := range caller["serves"].([]any) {
		got = append(got, api.(string))
	}
	if !slices.Equal(got, serves) {
		return fmt.Errorf("its caller serves %s, but %s serves %s", strings.Join(got, ", "), e.from.res.Name, strings.Join(serves, ", "))
	}
	return nil
}

// fillCallers sets the value of each callers field: the entries the
// connectors of its edges gave, merged by issuer. callees holds each
// edge's entry, and connected the edges whose connector ran.
func (r *resolver) fillCallers(callees map[string]any, connected map[string]bool) {
	for _, name := range sortedKeys(r.deployables) {
		for _, b := range r.deployables[name].res.Bindings {
			if b.CallersOf == "" {
				continue
			}
			var entries []callerEntry
			missing := false
			for _, id := range b.Edges {
				callee, ok := callees[id]
				if !ok {
					missing = true
					if connected[id] {
						r.fail(CodeLowering, "connector %s on edge %s gives %s nothing to verify %s's service credential with, but %s has operations with a service clause, which %s checks against its callers field %s", r.edges[id].connector.Name, id, name, r.edges[id].res.From, b.CallersOf, name, b.Field)
					}
					continue
				}
				entries = append(entries, callerEntry{edge: id, issuer: callee.(map[string]any)})
			}
			if missing {
				continue
			}
			value, err := mergeCallers(entries)
			if err == nil {
				err = ir.CheckServiceAuth(value)
			}
			if err != nil {
				r.fail(CodeLowering, "server %s: the callers field %s of %s: %v", name, b.Field, b.CallersOf, err)
				continue
			}
			b.Value = value
		}
	}
}

// callerEntry is the issuer an edge's connector gave the callee.
type callerEntry struct {
	edge   string
	issuer map[string]any
}

// mergeCallers merges the entries of a callers field's edges into its
// value: one issuer per issuer the entries name, its callers the union of
// theirs. Two entries that name one issuer must agree on everything but
// their callers, and two callers with one subject must be one deployable.
// The issuers are sorted by issuer, and each one's callers by deployable.
func mergeCallers(entries []callerEntry) (map[string]any, error) {
	type merged struct {
		edge    string
		rest    string
		issuer  map[string]any
		callers []any
	}
	byIssuer := map[string]*merged{}
	for _, entry := range entries {
		rest := map[string]any{}
		for key, value := range entry.issuer {
			if key != "callers" {
				rest[key] = value
			}
		}
		key := describe(entry.issuer["issuer"])
		m, ok := byIssuer[key]
		if !ok {
			m = &merged{edge: entry.edge, rest: describe(rest), issuer: rest}
			byIssuer[key] = m
		} else if describe(rest) != m.rest {
			return nil, fmt.Errorf("edges %s and %s give the issuer %s two ways: %s and %s", m.edge, entry.edge, key, m.rest, describe(rest))
		}
		for _, c := range entry.issuer["callers"].([]any) {
			caller := c.(map[string]any)
			idx := slices.IndexFunc(m.callers, func(other any) bool {
				return describe(other.(map[string]any)["subject"]) == describe(caller["subject"])
			})
			switch {
			case idx < 0:
				m.callers = append(m.callers, caller)
			case describe(m.callers[idx]) != describe(caller):
				return nil, fmt.Errorf("the issuer %s's subject %s is both %s and %s", key, describe(caller["subject"]), describe(m.callers[idx]), describe(caller))
			}
		}
	}
	issuers := []any{}
	for _, key := range sortedKeys(byIssuer) {
		m := byIssuer[key]
		slices.SortFunc(m.callers, func(a, b any) int {
			ca, cb := a.(map[string]any), b.(map[string]any)
			if c := strings.Compare(fmt.Sprint(ca["deployable"]), fmt.Sprint(cb["deployable"])); c != 0 {
				return c
			}
			return strings.Compare(describe(ca["subject"]), describe(cb["subject"]))
		})
		m.issuer["callers"] = m.callers
		issuers = append(issuers, m.issuer)
	}
	return map[string]any{"issuers": issuers}, nil
}
