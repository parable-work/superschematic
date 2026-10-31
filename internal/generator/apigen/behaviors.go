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
// for: Endpoints, and BehaviorEndpoints, whose factories call the ORM, in
// Generate's order (by path, then method). Without BehaviorEndpoints they
// are Endpoints.
func (o *APIOutput) HandlerEndpoints() []EndpointInfo {
	if len(o.BehaviorEndpoints) == 0 {
		return o.Endpoints
	}
	return sortedEndpoints(o.Endpoints, o.BehaviorEndpoints)
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

// DeclaresFieldBehaviors reports whether a table of schema (nil for none)
// has a field that is a behavior (D103): its Go ORM then requires the rules
// module, which links the behaviors' core, and a module that requires the
// ORM requires and replaces the rules module too, since Go takes no
// replace from a dependency's go.mod.
func DeclaresFieldBehaviors(schema *ir.Schema) bool {
	_, _, _, found := schema.FindFieldBehavior()
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
