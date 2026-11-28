package apigen

import (
	"fmt"
	"slices"

	ir "github.com/parable-work/superschematic/ir"
)

// The active grants (#735), the operation the loader adds to an
// @accessGrantsOperations set, are the build's to serve: the Go server's
// router mounts it at GET grants/active behind the steps any route runs,
// from the set's clause, on the handler factory of any route, whose call is
// a method of accessgrants.go rather than of the implementation. That
// method decides whether the request's caller holds the set's permission,
// owns the row the query names, or asks for its own grants, and lists
// exactly the grants that one may see through ActiveGrants of the ORM of
// the API's database; any other request is the 403 of the permission it
// lacks. The OpenAPI document, the SDKs, the permission catalog and the
// tool documents read it as any other operation, so it stays in
// APIOutput.Endpoints, and ImplementedOutput moves it to
// APIOutput.AccessGrantEndpoints for the Go server's writers.

// AccessGrantsOperation is the active grants the endpoint lists, or nil.
func (e EndpointInfo) AccessGrantsOperation() *ir.AccessGrantsOperation {
	if e.Operation == nil {
		return nil
	}
	return e.Operation.AccessGrantsOperation
}

// accessGrantsDatabase is the database whose grants the endpoints list,
// the API's database, or "" when none lists them.
func accessGrantsDatabase(endpoints []EndpointInfo) string {
	for _, ep := range endpoints {
		if op := ep.AccessGrantsOperation(); op != nil && op.Database != "" {
			return op.Database
		}
	}
	return ""
}

// HasAccessGrantOperations reports whether the API lists the active
// grants: an endpoint the loader added for an @accessGrantsOperations set.
func (o *APIOutput) HasAccessGrantOperations() bool {
	return slices.ContainsFunc(o.Endpoints, func(ep EndpointInfo) bool { return ep.AccessGrantsOperation() != nil })
}

// HasAccessGrantEndpoints reports whether the Go server lists the active
// grants: one of its AccessGrantEndpoints in the view ImplementedOutput
// returns, or of its Endpoints that it would move there. Its Config then
// takes the ORM of its database, and the module holds accessgrants.go.
func (o *APIOutput) HasAccessGrantEndpoints() bool {
	return len(o.AccessGrantEndpoints) > 0 || o.HasAccessGrantOperations()
}

// AccessGrantOwners are the tables whose rows name an owner, each with the
// JSON member that holds it, as accessgrants.go's table of them holds
// them: the owner of a grant's target lists the grants on it.
func (o *APIOutput) AccessGrantOwners() []ir.AccessGrantOwner {
	for _, list := range [][]EndpointInfo{o.AccessGrantEndpoints, o.Endpoints} {
		for _, ep := range list {
			if op := ep.AccessGrantsOperation(); op != nil {
				return op.Owners
			}
		}
	}
	return nil
}

// checkAccessGrants refuses a Go server whose active grants it cannot
// list: IR the loader did not fill, and Deps that hold the ORM of another
// database.
func (o *APIOutput) checkAccessGrants() error {
	for _, list := range [][]EndpointInfo{o.Endpoints, o.AccessGrantEndpoints} {
		for _, ep := range list {
			op := ep.AccessGrantsOperation()
			if op == nil {
				continue
			}
			owner := ep.Namespace + "." + ep.Name
			if op.Database == "" || op.Grants == "" || op.Permission == "" {
				return fmt.Errorf("apigen: %s lists the active grants (#735) and its marker is not whole (database %q, grants %q, permission %q); the loader fills it, so load the schema before generating", owner, op.Database, op.Grants, op.Permission)
			}
			if o.Deps.Database != op.Database || o.Deps.ORMModule == "" {
				return fmt.Errorf("apigen: %s lists the active grants of %s, and the Go server's Deps hold the ORM of %q (#735)", o.SchemaName, op.Database, o.Deps.Database)
			}
			if o.IsPublic && o.ORMModule != o.Deps.ORMModule {
				return fmt.Errorf("apigen: %s lists the active grants of %s, and its Config.DB is the ORM of %s, its auth schema: list them from an API whose auth schema is %s (#735)", o.SchemaName, op.Database, o.UpstreamSchema, op.Database)
			}
		}
	}
	return nil
}

// AccessGrantsDoc is the doc comment line of the active grants' handler
// factory.
func (e EndpointInfo) AccessGrantsDoc() string {
	op := e.AccessGrantsOperation()
	if op == nil {
		return ""
	}
	return fmt.Sprintf("It lists the active grants of %s (#735) the caller may see, through the method of accessgrants.go that serves it: no implementation method serves it.", op.Database)
}
