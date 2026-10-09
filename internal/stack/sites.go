package stack

import (
	ir "github.com/parable-work/superschematic/ir"
)

// The static sites of a stack (D55; docs/stack-model.md, section 8.10). A
// Site service is a deployable of kind site, always exposed. It has a site
// edge to the server of each API it calls, which must be exposed too, and
// whose connector derives the API's public address and nothing else. The
// site's bindings are those edges' values, keyed by the API's name, which
// the deploy writes into the site's config for each environment. Each API a
// site calls gets a CORS field on its server, ir.CORSField, whose value
// lists the public origin of each site that calls it: the server answers
// CORS for those origins and no other.

// bindSite binds a site's config: one derived binding per site edge, named
// after the API it reaches (ir.DerivedFieldNames.Field). A site reads no
// environment variable, so an env key set on it is refused.
func (r *resolver) bindSite(d *deployable) {
	for _, key := range sortedKeys(d.settings.env) {
		r.fail(CodeUnknownEnvKey, "environment %s sets env %s on site %s; a site reads no environment variable, only the address of each API it calls from its config", d.settings.envFrom[key], key, d.res.Name)
	}
	var bindings []*ir.Binding
	for _, e := range r.edgesFrom(d.res.Name) {
		bindings = append(bindings, &ir.Binding{Field: e.res.Field, Source: ir.BindingDerived, Edge: e.res.ID})
	}
	d.res.Bindings = bindings
}

// siteEdgesTo returns the site edges to the API service api, sorted by ID.
func (r *resolver) siteEdgesTo(api string) []string {
	var ids []string
	for _, id := range sortedKeys(r.edges) {
		if e := r.edges[id].res; e.Kind == ir.EdgeSite && e.Service.Name == api {
			ids = append(ids, id)
		}
	}
	return ids
}

// corsFields returns the CORS fields of a server: one per API it serves
// that a site calls, by field name.
func (r *resolver) corsFields(d *deployable) map[string]string {
	fields := map[string]string{}
	for _, served := range d.res.Services {
		if len(r.siteEdgesTo(served.Name)) > 0 {
			fields[ir.CORSField(served.Name)] = served.Name
		}
	}
	return fields
}

// checkCORSFields refuses a CORS field of server d that a config field, an
// edge's derived field, a callers field or an env key of the environment
// takes: its name, or a name it begins with and an underscore, either way
// round. It reports the env keys it refused, which bindConfig then skips.
func (r *resolver) checkCORSFields(d *deployable, cors, callers map[string]string, derived map[string]*edge) map[string]bool {
	refused := map[string]bool{}
	for _, name := range sortedKeys(cors) {
		api := cors[name]
		for _, fieldName := range sortedKeys(d.fields) {
			if f := d.fields[fieldName]; ir.DerivedFieldClaims(name, f.name) {
				r.fail(CodeFieldCollision, "server %s: config field %s of %s collides with %s, the CORS field of %s", d.res.Name, f.name, f.declaring, name, api)
			}
		}
		for _, other := range sortedKeys(derived) {
			if ir.DerivedFieldClaims(name, other) || ir.DerivedFieldClaims(other, name) {
				r.fail(CodeFieldCollision, "server %s: %s, the field edge %s derives, collides with %s, the CORS field of %s", d.res.Name, other, derived[other].res.ID, name, api)
			}
		}
		for _, other := range sortedKeys(callers) {
			if ir.DerivedFieldClaims(name, other) || ir.DerivedFieldClaims(other, name) {
				r.fail(CodeFieldCollision, "server %s: %s, the callers field of %s, collides with %s, the CORS field of %s", d.res.Name, other, callers[other], name, api)
			}
		}
		if _, set := d.settings.env[name]; set {
			r.fail(CodeUnknownEnvKey, "environment %s sets env %s on server %s, the CORS field of %s, which the site edges to %s derive", d.settings.envFrom[name], name, d.res.Name, api, api)
			refused[name] = true
		}
	}
	return refused
}

// corsBindings binds each CORS field of server d to the site edges to its
// API, whose sites' origins fillCORS lists.
func (r *resolver) corsBindings(d *deployable, cors map[string]string) []*ir.Binding {
	var out []*ir.Binding
	for _, name := range sortedKeys(cors) {
		api := cors[name]
		out = append(out, &ir.Binding{Field: name, Source: ir.BindingDerived, CORSOf: api, Edges: r.siteEdgesTo(api)})
	}
	return out
}

// fillCORS sets the value of each CORS field: the public origin of the
// site of each of its edges, in the order of the edges, each once.
func (r *resolver) fillCORS() {
	for _, name := range sortedKeys(r.deployables) {
		for _, b := range r.deployables[name].res.Bindings {
			if b.CORSOf == "" {
				continue
			}
			var origins []any
			seen := map[string]bool{}
			for _, id := range b.Edges {
				site := r.edges[id].from
				if seen[site.res.Name] || site.res.PublicAddress == nil {
					continue // deriveSiteEdges reported a site with none
				}
				seen[site.res.Name] = true
				origins = append(origins, deepCopy(site.res.PublicAddress))
			}
			value := map[string]any{"origins": origins}
			if err := ir.CheckCORSPolicy(value); err != nil {
				r.fail(CodeLowering, "server %s: the CORS field %s of %s: %v", name, b.Field, b.CORSOf, err)
				continue
			}
			b.Value = value
		}
	}
}
