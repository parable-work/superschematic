package rustrestgen

import (
	"fmt"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	ir "github.com/parable-work/superschematic/ir"
)

// The active grants in a Rust API (#735). The operation the loader adds
// for an @accessGrantsOperations set has no method in its namespace's
// trait: the router's handler hands its query to src/accessgrants.rs,
// which lists the grants the caller may see through the repositories of
// the API's database, in the database's Rust types (the HTTP runtime's
// access::routes), and answers them as the API's AccessGrant. TypeScript
// serves the same route through the runtime's accessGrantsOperation and Go
// through apigen's accessgrants.go.

// AccessGrantsFile is the file of an API crate that lists its active
// grants.
const AccessGrantsFile = "accessgrants.rs"

// AccessGrantsInfo is the active grants' route as the crate serves it.
type AccessGrantsInfo struct {
	// Database and Permission are the loader's marker: the API's database
	// and the permission whose holder lists every active grant; Owners the
	// member that holds the owner of each table's rows.
	Database   string
	Permission string
	Owners     []ir.AccessGrantOwner
	// HolderField, TargetTypeField and TargetIDField are the Args' fields
	// of the query's arguments.
	HolderField, TargetTypeField, TargetIDField string
}

// accessGrantsInfo is the active grants' route endpoint serves, nil for
// every other. info must hold its arguments (builder.params). IR the
// loader did not fill is an error that names the operation.
func accessGrantsInfo(endpoint apigen.EndpointInfo, info EndpointInfo) (*AccessGrantsInfo, error) {
	op := endpoint.AccessGrantsOperation()
	if op == nil {
		return nil, nil
	}
	owner := info.Namespace + "." + info.Name
	if op.Database == "" || op.Grants == "" || op.Permission == "" {
		return nil, fmt.Errorf("rustrestgen: %s lists the active grants (#735) and its marker is not whole (database %q, grants %q, permission %q); the loader fills it, so load the schema before generating", owner, op.Database, op.Grants, op.Permission)
	}
	out := &AccessGrantsInfo{Database: op.Database, Permission: op.Permission, Owners: op.Owners}
	for _, arg := range info.QueryArgs {
		switch arg.Name {
		case ir.AccessGrantsHolderArgument:
			out.HolderField = arg.Field
		case ir.AccessGrantsTargetTypeArgument:
			out.TargetTypeField = arg.Field
		case ir.AccessGrantsTargetIDArgument:
			out.TargetIDField = arg.Field
		}
	}
	if out.HolderField == "" || out.TargetTypeField == "" || out.TargetIDField == "" {
		return nil, fmt.Errorf("rustrestgen: %s lists the active grants (#735), and its query has no holder, targetType or targetId argument; the loader adds them", owner)
	}
	return out, nil
}

// HasAccessGrantOperations reports whether the router lists the active
// grants (#735): the crate then has src/accessgrants.rs, Implementations
// holds the AccessGrants its handler runs on, and the crate depends on the
// database's Rust types for its repositories.
func (o *APIOutput) HasAccessGrantOperations() bool {
	return len(o.AccessGrantEndpoints()) > 0
}

// AccessGrantEndpoints are the active grants' routes the router serves, in
// route order.
func (o *APIOutput) AccessGrantEndpoints() []EndpointInfo {
	var out []EndpointInfo
	for _, endpoint := range o.Endpoints {
		if endpoint.AccessGrants != nil {
			out = append(out, endpoint)
		}
	}
	return out
}

// AccessGrantsDatabase is the DB service whose grants the route lists,
// the API's database; empty without one.
func (o *APIOutput) AccessGrantsDatabase() string {
	for _, endpoint := range o.AccessGrantEndpoints() {
		return endpoint.AccessGrants.Database
	}
	return ""
}

// AccessGrantsInterfacesUse is src/accessgrants.rs's import of the
// route's Args struct.
func (o *APIOutput) AccessGrantsInterfacesUse() string {
	var names []string
	for _, endpoint := range o.AccessGrantEndpoints() {
		names = append(names, endpoint.ArgsName)
	}
	return "use crate::interfaces::{" + joinNames(names) + "};"
}

func joinNames(names []string) string {
	out := ""
	for i, name := range names {
		if i > 0 {
			out += ", "
		}
		out += name
	}
	return out
}

// checkAccessGrantsDeps refuses an active grants' route the crate cannot
// serve: an API with no database of its own in Deps, one whose grants are
// of another DB service, and a database whose Rust types are not
// generated.
func (o *APIOutput) checkAccessGrantsDeps() error {
	if !o.HasAccessGrantOperations() {
		return nil
	}
	db := o.AccessGrantsDatabase()
	switch {
	case o.Deps == nil || o.Deps.Database == "":
		return fmt.Errorf("rustrestgen: %s lists the active grants of %s (#735), and its Deps hold no database; depend on %s", o.SchemaName, db, db)
	case o.Deps.Database != db:
		return fmt.Errorf("rustrestgen: %s lists the active grants of %s (#735), and its Deps hold %s's", o.SchemaName, db, o.Deps.Database)
	case o.Deps.DatabaseTypesCrate == "":
		return fmt.Errorf("rustrestgen: %s lists the active grants of %s (#735), whose repositories are in its Rust types, and %s generates none; enable outputs.types.rust in %s's config", o.SchemaName, db, db, db)
	}
	return nil
}
