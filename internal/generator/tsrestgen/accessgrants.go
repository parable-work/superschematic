package tsrestgen

import (
	"fmt"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	ir "github.com/parable-work/superschematic/ir"
)

// AccessGrantsRoute is the active grants (#735) as the router mounts them:
// the operation the loader added for an @accessGrantsOperations set, run
// by the runtime's accessGrantsOperation over RouterOptions.orm, which
// decides whether the request's caller is the administrator, the target's
// owner or the holder, and lists the grants that one may see.
type AccessGrantsRoute struct {
	// Set is the @accessGrantsOperations set that serves the operation.
	Set string
	// Database is the API's database, Permission the permission whose
	// holder lists every active grant, and Owners the member that holds
	// the owner of each table's rows: the loader's marker.
	Database   string
	Permission string
	Owners     []ir.AccessGrantOwner
}

// Literal is the operation as router.ts's table of them holds it, an
// AccessGrantsOperation: { permission: 'access.admin', owners: { Folder: 'owner' } }.
func (r *AccessGrantsRoute) Literal() string {
	owners := make([]string, len(r.Owners))
	for i, owner := range r.Owners {
		owners[i] = owner.Type + ": " + tsString(owner.Member())
	}
	return "{ permission: " + tsString(r.Permission) + ", owners: { " + strings.Join(owners, ", ") + " } }"
}

// AccessGrantsRoutes are the active grants' routes the router mounts, in
// the order of Endpoints.
func (o *APIOutput) AccessGrantsRoutes() []*AccessGrantsRoute {
	var out []*AccessGrantsRoute
	for _, endpoint := range o.Endpoints {
		if endpoint.AccessGrants != nil {
			out = append(out, endpoint.AccessGrants)
		}
	}
	return out
}

// HasAccessGrants reports whether the API lists the active grants (#735),
// which the router runs over RouterOptions.orm.
func (o *APIOutput) HasAccessGrants() bool {
	return len(o.AccessGrantsRoutes()) > 0
}

// checkAccessGrantsDeps refuses deps for an API that lists the active
// grants when they hold no repositories, which the router reads them
// through.
func (o *APIOutput) checkAccessGrantsDeps(deps DepsInfo) error {
	routes := o.AccessGrantsRoutes()
	if len(routes) == 0 || depsORM(deps) {
		return nil
	}
	route := routes[0]
	return fmt.Errorf("tsrestgen: %s lists the active grants (@accessGrantsOperations on %s), which read through Deps.orm, the repositories %s's TypeScript types export; enable outputs.types.typescript in %s's config", o.SchemaName, route.Set, route.Database, route.Database)
}

// accessGrantsEndpoint is the active grants (#735) as the router mounts
// them: the route, the query parameters and the result, decoded as an
// implemented endpoint's, and the marker it runs. Its types are rendered by
// a builder of its own, so they add no import to interfaces.ts, which has
// no method for it. The loader fills the marker; IR that did not go
// through it is refused here, naming the operation.
func (b *builder) accessGrantsEndpoint(ep apigen.EndpointInfo, op *ir.AccessGrantsOperation) (EndpointInfo, error) {
	own := newBuilder(b.schema, b.opts)
	endpoint, err := own.endpoint(ep)
	if err != nil {
		return endpoint, err
	}
	owner := behaviorSet(b.schema, ep.Operation)
	if op.Database == "" || op.Grants == "" || op.Permission == "" {
		return endpoint, fmt.Errorf("tsrestgen: %s.%s lists the active grants (#735) and its marker is not whole (database %q, grants %q, permission %q); the loader fills it, so load the schema before generating", owner, ep.Operation.Name, op.Database, op.Grants, op.Permission)
	}
	endpoint.AccessGrants = &AccessGrantsRoute{Set: owner, Database: op.Database, Permission: op.Permission, Owners: op.Owners}
	return endpoint, nil
}
