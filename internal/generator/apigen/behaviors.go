package apigen

import (
	"slices"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// A table's behavior operations (D103), the ones the loader adds to a
// @behaviorOperations set, are the behaviors' core's to run: the Go
// server's router mounts each at its route behind the steps any route
// runs, from the set's clause, on a generated handler that calls the
// operation of the ORM of the API's database (db.Get<Type>Repository().
// <Use>().<Operation>), which runs it through the core in a transaction of
// its own. The implementation the project writes has no method for them.
// The OpenAPI document, the SDKs, the permission catalog and the tool
// documents read them as any other operation, so they stay in
// APIOutput.Endpoints, and ImplementedOutput moves them to
// APIOutput.BehaviorEndpoints for the Go server's writers.

// HasBehaviorEndpoints reports whether the API serves a behavior
// operation: one of its Endpoints, or of its BehaviorEndpoints in the view
// ImplementedOutput returns. Its Config then takes the ORM of its database,
// and the module holds behaviors.go.
func (o *APIOutput) HasBehaviorEndpoints() bool {
	if len(o.BehaviorEndpoints) > 0 {
		return true
	}
	return slices.ContainsFunc(o.Endpoints, func(ep EndpointInfo) bool { return ep.BehaviorOperation() != nil })
}

// HandlerEndpoints are the endpoints routes.go writes a handler factory
// for: Endpoints, and BehaviorEndpoints and TableEndpoints, whose
// factories call the ORM, in Generate's order (by path, then method).
// Without the last two they are Endpoints.
func (o *APIOutput) HandlerEndpoints() []EndpointInfo {
	if len(o.BehaviorEndpoints) == 0 && len(o.TableEndpoints) == 0 {
		return o.Endpoints
	}
	return sortedEndpoints(o.Endpoints, o.BehaviorEndpoints, o.TableEndpoints)
}

// sortedEndpoints are the endpoints of each list, in Generate's order: by
// path, then method.
func sortedEndpoints(lists ...[]EndpointInfo) []EndpointInfo {
	var all []EndpointInfo
	for _, list := range lists {
		all = append(all, list...)
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Path != all[j].Path {
			return all[i].Path < all[j].Path
		}
		return all[i].Method < all[j].Method
	})
	return all
}

// BehaviorRepositoryCall is the ORM method a behavior endpoint's handler
// calls, from the ORM of the API's database: the repository of the table,
// the operations of the use, and the operation, as ormgen names them
// (GetOrderRepository().Status().Transition). It is "" for every other
// endpoint.
func (e EndpointInfo) BehaviorRepositoryCall() string {
	bo := e.BehaviorOperation()
	if bo == nil {
		return ""
	}
	return "Get" + bo.Type + "Repository()." + codegen.ToPascalCase(bo.Use) + "()." + codegen.ToPascalCase(bo.Operation)
}

// BehaviorPreconditionsMember is the member of a behavior operation's input
// that carries the caller's preconditions (D16, D104), each use's entry by
// its use id, on a table one of whose uses takes them: a body member
// rather than a header, so an SDK call and an MCP tool carry it as any
// input member.
const BehaviorPreconditionsMember = "preconditions"

// carriesBehaviorPreconditions reports whether input, the input type of
// op, carries the caller's preconditions: op is a behavior operation and
// input holds the member BehaviorPreconditionsMember of the type the
// loader names for its table, <Table>Preconditions, which it adds beside
// the operation's parameters and refuses a parameter of that name beside.
func carriesBehaviorPreconditions(op *ir.FieldDef, input *ir.TypeDef) bool {
	if op == nil || op.BehaviorOperation == nil || input == nil {
		return false
	}
	for _, field := range input.Fields {
		if field.Name == BehaviorPreconditionsMember && field.Origin == ir.OriginBehavior && field.TypeRef.Name == op.BehaviorOperation.Type+"Preconditions" && !field.TypeRef.IsArray && !field.TypeRef.IsMap {
			return true
		}
	}
	return false
}

// HasBehaviorPreconditions reports whether a behavior operation the API
// serves carries the caller's preconditions in its input
// (EndpointInfo.BehaviorPreconditions): behaviors.go then holds the
// handler's runBehaviorOperationWithPreconditions.
func (o *APIOutput) HasBehaviorPreconditions() bool {
	return slices.ContainsFunc(o.HandlerEndpoints(), func(ep EndpointInfo) bool { return ep.BehaviorPreconditions })
}

// BehaviorRunner is the function a behavior endpoint's handler runs its
// operation with: runTableBehaviorOperation for an operation of the table
// as a whole (D104), whose route and ORM method take no key;
// runBehaviorOperationWithPreconditions for one whose input carries the
// caller's preconditions; runBehaviorOperation otherwise.
func (e EndpointInfo) BehaviorRunner() string {
	if bo := e.BehaviorOperation(); bo != nil && bo.Table {
		return "runTableBehaviorOperation"
	}
	if e.BehaviorPreconditions {
		return "runBehaviorOperationWithPreconditions"
	}
	return "runBehaviorOperation"
}

// HasTableBehaviorEndpoints reports whether the API serves an operation of
// a table as a whole (D104), a command without a row: behaviors.go then
// holds the handler's runTableBehaviorOperation.
func (o *APIOutput) HasTableBehaviorEndpoints() bool {
	return slices.ContainsFunc(o.HandlerEndpoints(), func(ep EndpointInfo) bool {
		bo := ep.BehaviorOperation()
		return bo != nil && bo.Table
	})
}

// BehaviorResultGoType is the Go type a behavior endpoint's handler
// decodes the operation's result into: the map for a map, the list for a
// list, and the output itself otherwise.
func (e EndpointInfo) BehaviorResultGoType() string {
	switch {
	case e.OutputIsMap:
		return e.OutputGoMapType()
	case e.OutputIsArray:
		return e.OutputGoListType()
	}
	return e.OutputGoType
}

// catalogPermissions are the permissions the permission catalog lists
// under the endpoint: those its route requires, then those the config of
// its behavior names, which the core checks (D103), each once.
func (e EndpointInfo) catalogPermissions() []string {
	bo := e.BehaviorOperation()
	if bo == nil || len(bo.Permissions) == 0 {
		return e.RequiredPerms
	}
	permissions := slices.Clone(e.RequiredPerms)
	for _, p := range bo.Permissions {
		if !slices.Contains(permissions, p) {
			permissions = append(permissions, p)
		}
	}
	return permissions
}

// behaviorDatabase is the database whose tables the endpoints' behavior
// operations run on, the API's database, or "" when none is one.
func behaviorDatabase(endpoints []EndpointInfo) string {
	for _, ep := range endpoints {
		if bo := ep.BehaviorOperation(); bo != nil {
			return bo.Database
		}
	}
	return ""
}

// DeclaresTableBehaviors reports whether a table of schema (nil for none)
// composes a behavior, on a field or by implements (D103): its Go ORM then
// requires the rules module, which links the behaviors' core, and a module
// that requires the ORM requires and replaces the rules module too, since
// Go takes no replace from a dependency's go.mod.
func DeclaresTableBehaviors(schema *ir.Schema) bool {
	_, _, found := schema.FindTableBehavior()
	return found
}

// behaviorArticle is a behavior's name after its indefinite article ("a
// Workflow"), as behaviortypes.An writes it, for the handlers' doc
// comments.
func behaviorArticle(name string) string {
	if name != "" && strings.ContainsRune("AEIOUaeiou", rune(name[0])) {
		return "an " + name
	}
	return "a " + name
}
