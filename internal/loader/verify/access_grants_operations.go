package verify

import (
	"fmt"

	"github.com/parable-work/superschematic/internal/loader/accessgrantsops"
	ir "github.com/parable-work/superschematic/ir"
)

// AccessGrantsOperationsProblem is a refusal of an @accessGrantsOperations
// set's own form (#735) that the TypeScript reader reports at its node and
// the pass for every form.
type AccessGrantsOperationsProblem int

const (
	// AccessGrantsOperationsMember is a member of the class, or an
	// authored operation in the set: the loader adds the set's operation.
	AccessGrantsOperationsMember AccessGrantsOperationsProblem = iota
	// AccessGrantsOperationsAuthenticated is an Authenticated base, which
	// the IR keeps only on the operations, so only the TypeScript reader
	// sees it.
	AccessGrantsOperationsAuthenticated
	// AccessGrantsOperationsEncrypted is an Encrypted base.
	AccessGrantsOperationsEncrypted
)

// AccessGrantsOperationsMessage words a refusal of set's own form.
func AccessGrantsOperationsMessage(set *ir.OperationSet, problem AccessGrantsOperationsProblem) string {
	switch problem {
	case AccessGrantsOperationsMember:
		return fmt.Sprintf("%s: an @accessGrantsOperations class has no members: the loader adds the route of the active grants, and the generated server runs it; an operation that needs code of the project's own is a method of another class, which calls activeGrants", set.Name)
	case AccessGrantsOperationsAuthenticated:
		return fmt.Sprintf("%s: an @accessGrantsOperations class does not extend Authenticated: its route needs a caller already, and @auth on the class says the same", set.Name)
	case AccessGrantsOperationsEncrypted:
		return fmt.Sprintf("%s: an @accessGrantsOperations class is not Encrypted: its route takes no body", set.Name)
	}
	return ""
}

// CheckAccessGrantsOperations runs the checks of the API's
// @accessGrantsOperations set alone (checkAccessGrantsOperations), which
// Run runs among the rest. The loader runs it before it fills the set
// (accessgrantsops.Expand), which it runs before Run, so the operation it
// adds meets every check an authored operation meets.
func CheckAccessGrantsOperations(schema *ir.Schema, in Input) *Result {
	r := &Result{}
	checkAccessGrantsOperations(schema, in, r)
	return r
}

// checkAccessGrantsOperations validates the API's @accessGrantsOperations
// set (#735), before the loader fills it: the set's own form and options,
// one set per API, the API's database (in.Database), which declares the
// @accessGrants table, and the names, route and MCP handle the expansion
// adds. Run runs it again after the expansion, which it does not refuse:
// the operation the expansion added is the set's own, and a name whose
// definition it added is its own.
func checkAccessGrantsOperations(schema *ir.Schema, in Input, r *Result) {
	var sets []*ir.OperationSet
	for _, set := range schema.OperationSets {
		if set.IsAccessGrantsOperations() {
			sets = append(sets, set)
		}
	}
	if len(sets) == 0 {
		return
	}
	if schema.Kind != ir.SchemaKindAPI {
		for _, set := range sets {
			r.errorf("", "%s: @accessGrantsOperations is only allowed in an API schema (this schema has kind %s)", set.Name, schema.Kind)
		}
		return
	}
	for _, set := range sets[1:] {
		r.errorf("", "%s: @accessGrantsOperations appears on %s too: an API lists its active grants from one set, at /api/%s", set.Name, sets[0].Name, ir.AccessGrantsActivePath)
	}
	set := sets[0]
	failed := len(r.Errors)
	if set.Encrypted {
		r.errorf("", "%s", AccessGrantsOperationsMessage(set, AccessGrantsOperationsEncrypted))
	}
	for _, op := range set.Operations {
		if op.AccessGrantsOperation == nil {
			r.errorf("", "%s", AccessGrantsOperationsMessage(set, AccessGrantsOperationsMember))
			break
		}
	}
	if set.Public {
		r.errorf("", "%s: @publicRoute on an @accessGrantsOperations class would list the active grants to anyone: its route needs a caller, the holder of %s, a target's owner or a grant's holder", set.Name, set.AccessGrantsOperations.AdminPermission())
	}
	if permission := set.AccessGrantsOperations.Permission; permission != "" && !identityPermission.MatchString(permission) {
		r.errorf("", "%s: @accessGrantsOperations names the permission %q, which is no permission name: dotted words of letters, digits, _ and -, as access.admin is", set.Name, permission)
	}
	if len(r.Errors) > failed {
		return
	}
	db, _, ok := schema.Database()
	if !ok {
		r.errorf("", "%s: @accessGrantsOperations lists the grants of the API's database, and %s connects to none: set its authDb, or give it one DB dependency, the DB service that declares the @accessGrants table", set.Name, schema.Name)
		return
	}
	if in.Database == nil {
		// The loader reads the database for an API with such a set;
		// without it, the table cannot be checked.
		return
	}
	if in.Database.AccessGrantsTable() == nil {
		r.errorf("", "%s: @accessGrantsOperations lists the grants of %s, which declares no @accessGrants table (D156): mark the class whose rows are the grants @accessGrants()", set.Name, db)
		return
	}
	plan, err := accessgrantsops.PlanSet(set, in.Database, in.registry())
	if err != nil {
		r.errorf("", "%s: @accessGrantsOperations: %v", set.Name, err)
		return
	}
	checkAccessGrantsNames(schema, set, plan, r)
	checkAccessGrantsRoute(schema, set, plan, r)
}

// checkAccessGrantsNames refuses the type the expansion adds when the API
// already declares or imports one of its name, the operation it adds when
// another operation of its namespace takes its name, and its MCP handle
// when another tool takes it. A definition or an operation the expansion
// added is its own.
func checkAccessGrantsNames(schema *ir.Schema, set *ir.OperationSet, plan *accessgrantsops.Plan, r *Result) {
	operations, handles := map[string]string{}, map[string]string{}
	for _, other := range schema.OperationSets {
		if other == nil || other.IsAccessGrantsOperations() {
			continue
		}
		namespace := setNamespace(other.Name)
		for _, op := range other.Operations {
			if op == nil {
				continue
			}
			operations[namespace+"."+op.Name] = other.Name + "." + op.Name
			if op.MCP != nil && !op.MCP.Hidden && op.MCP.Handle != "" {
				handles[op.MCP.Handle] = other.Name + "." + op.Name
			}
		}
	}
	declared := func(name string) bool {
		for _, imp := range schema.Imports {
			for _, imported := range imp.Types {
				if imported == name {
					return true
				}
			}
		}
		if td := schema.Types[name]; td != nil {
			return td.Origin != ir.OriginAccessGrantsOperations
		}
		return schema.Enums[name] != nil || schema.Unions[name] != nil || schema.Scalars[name] != nil || schema.Inputs[name] != nil
	}
	for _, td := range plan.Types {
		if declared(td.Name) {
			r.errorf("", "%s: @accessGrantsOperations adds the type %s, and %s already declares or imports one of that name; rename it", set.Name, td.Name, schema.Name)
		}
	}
	namespace := setNamespace(set.Name)
	op := plan.Operation
	if other, taken := operations[namespace+"."+op.Name]; taken {
		r.errorf("", "%s: @accessGrantsOperations adds the operation %s to the namespace %s, where %s takes that name; rename that operation, or name the set for another namespace", set.Name, op.Name, namespace, other)
	}
	if op.MCP != nil && !op.MCP.Hidden && op.MCP.Handle != "" {
		if other, taken := handles[op.MCP.Handle]; taken {
			r.errorf("", "%s: @accessGrantsOperations adds the MCP tool %q, and %s's @mcp takes that handle; rename that handle (D11)", set.Name, op.MCP.Handle, other)
		}
	}
}

// checkAccessGrantsRoute refuses the operation the expansion adds when
// another operation of the API shares its method and a path that matches
// its own.
func checkAccessGrantsRoute(schema *ir.Schema, set *ir.OperationSet, plan *accessgrantsops.Plan, r *Result) {
	op := plan.Operation
	path := "/api/" + op.RestPath
	for _, other := range schema.OperationSets {
		if other == nil || other.IsAccessGrantsOperations() {
			continue
		}
		namespace := setNamespace(other.Name)
		for _, authored := range other.Operations {
			if authored == nil {
				continue
			}
			method, otherPath, ok := operationRoute(schema, namespace, authored)
			if !ok || method != op.HTTPMethod || !routesMatch(path, otherPath) {
				continue
			}
			r.errorf("", "%s: the operation %s it adds is routed at %s %s, which %s.%s's %s %s also matches: move that route (#735)", set.Name, op.Name, op.HTTPMethod, path, other.Name, authored.Name, method, otherPath)
		}
	}
}
