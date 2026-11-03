package stack

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// The origins of a stack (D91; docs/stack-model.md, section 8.10). An
// origin puts a site and APIs it calls on one origin in every environment:
// the site's platform, which must serve origins
// (registry.PlatformSpec.ServesOrigin), serves each such API under
// ir.SiteDevAPIPath(service) on the site's own public address, forwarded
// to the API's server with the prefix taken off, as `stack dev`'s proxy
// forwards it (D82). The site edge to such an API derives the same-origin
// URL (ir.SiteOriginURL), which the site's config hands the page, so the
// SDK's code is the same in development and on a deploy; the API's CORS
// field leaves the site out, since the page calls it from its own origin;
// and its server needs no exposure of its own when it runs on the site's
// provider, whose site platform reaches it. A server on another provider
// is reached at its public address, so it must be exposed.

// resolveOrigins reads @stack's origins into each site's ir.ResolvedSite
// Origin and each server's Origins. An origin names a site the stack
// deploys, on a platform that serves origins, which no other origin names,
// and APIs the site calls, each once; with no APIs, every API the site
// calls.
func (r *resolver) resolveOrigins() {
	named := map[string]int{}
	for i, origin := range r.stack.Origins {
		if origin == nil {
			continue
		}
		where := fmt.Sprintf("origins[%d]", i)
		if !r.checkRef(where+" site", origin.Site, ir.SchemaKindSite) {
			continue
		}
		site, ok := r.deployables[r.byService[origin.Site.Name]]
		if !ok || site.res.Kind != ir.DeployableSite {
			r.fail(CodeOrigin, "%s names site %s, which the stack does not deploy; add %s to @stack's deploy", where, origin.Site.Name, origin.Site.Name)
			continue
		}
		if first, twice := named[site.res.Name]; twice {
			r.fail(CodeOrigin, "origins[%d] and %s both name site %s; a site has one origin, so list every API it serves there in one", first, where, site.res.Name)
			continue
		}
		named[site.res.Name] = i
		if !site.platform.ServesOrigin {
			r.fail(CodeUnrealizable, "site %s: platform %s serves no site's origin, so the APIs of %s cannot reach the page on the site's own; place the site on a platform that serves one, or leave it out of @stack's origins", site.res.Name, site.platform.Name, where)
			continue
		}
		apis := origin.APIs
		if len(apis) == 0 {
			apis = site.res.Calls
		}
		var served []*ir.SiteOriginAPI
		listed := map[string]bool{}
		for _, ref := range apis {
			if !r.checkRef(where+" apis", ref, ir.SchemaKindAPI) {
				continue
			}
			if listed[ref.Name] {
				r.fail(CodeOrigin, "%s lists %s twice", where, ref.Name)
				continue
			}
			listed[ref.Name] = true
			if !slices.ContainsFunc(site.res.Calls, func(call ir.ServiceRef) bool { return call.Name == ref.Name }) {
				r.fail(CodeOrigin, "%s serves %s on the origin of site %s, which does not call it; an origin serves only the APIs its site calls, so add %s to the site's calls", where, ref.Name, site.res.Name, ref.Name)
				continue
			}
			server, ok := r.deployables[r.byService[ref.Name]]
			if !ok {
				continue // collectMembers reported it
			}
			served = append(served, &ir.SiteOriginAPI{Service: ref.Name, Server: server.res.Name, Path: ir.SiteDevAPIPath(ref.Name)})
			if !slices.Contains(server.res.Origins, site.res.Name) {
				server.res.Origins = append(server.res.Origins, site.res.Name)
				slices.Sort(server.res.Origins)
			}
		}
		slices.SortFunc(served, func(a, b *ir.SiteOriginAPI) int { return strings.Compare(a.Service, b.Service) })
		site.res.Site.Origin = served
	}
}

// inOrigin reports whether the site edge e reaches an API its site serves
// on its own origin (D91).
func inOrigin(e *edge) bool {
	return e.res.Kind == ir.EdgeSite && e.from.res.Site.OriginAPI(e.res.Service.Name) != nil
}

// checkOriginURLs checks that the connector of each site edge to an API of
// the site's origin derived the same-origin URL, the site's public address
// and the API's path (ir.SiteOriginURL), which the site's config hands the
// page as `stack dev`'s proxy does (D91).
func (r *resolver) checkOriginURLs() {
	for _, id := range sortedKeys(r.edges) {
		e := r.edges[id]
		if !inOrigin(e) {
			continue
		}
		want := r.normalize(fmt.Sprintf("the same-origin URL of %s on site %s is", e.res.Service.Name, e.res.From), ir.SiteOriginURL(e.from.res.PublicAddress, e.res.Service.Name))
		for _, b := range e.from.res.Bindings {
			if b.Edge != id {
				continue
			}
			value, _ := b.Value.(map[string]any)
			got, _ := json.Marshal(value["url"])
			wanted, _ := json.Marshal(want)
			if string(got) != string(wanted) {
				r.fail(CodeLowering, "connector %s on edge %s derives the url %s for %s, which the origin of site %s serves at %s; the connector of a site's origin derives ir.SiteOriginURL", e.connector.Name, id, got, e.res.Service.Name, e.res.From, wanted)
			}
		}
	}
}
