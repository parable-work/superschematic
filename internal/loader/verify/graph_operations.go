package verify

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/loader/graphops"
	ir "github.com/parable-work/superschematic/ir"
)

// GraphOperationsProblem is a refusal of a @graphOperations set's own
// form (D138) that the TypeScript reader reports at its node and the pass
// for every form.
type GraphOperationsProblem int

const (
	// GraphOperationsMember is a member of the class, or an authored
	// operation in the set: the loader adds the set's operations.
	GraphOperationsMember GraphOperationsProblem = iota
	// GraphOperationsAuthenticated is an Authenticated base, which the IR
	// keeps only on the operations, so only the TypeScript reader sees it.
	GraphOperationsAuthenticated
	// GraphOperationsEncrypted is an Encrypted base.
	GraphOperationsEncrypted
)

// GraphOperationsMessage words a refusal of set's own form.
func GraphOperationsMessage(set *ir.OperationSet, problem GraphOperationsProblem) string {
	switch problem {
	case GraphOperationsMember:
		typeName := ""
		if set.GraphOperations != nil {
			typeName = set.GraphOperations.Type
		}
		return fmt.Sprintf("%s: a @graphOperations class has no members: the loader adds one operation for each operation of %s's version graph the set serves, and the generated server runs it; an operation that needs code of the project's own is a method of another class, which calls the graph's facade", set.Name, typeName)
	case GraphOperationsAuthenticated:
		return fmt.Sprintf("%s: a @graphOperations class does not extend Authenticated: write @auth on the class", set.Name)
	case GraphOperationsEncrypted:
		return fmt.Sprintf("%s: a @graphOperations class is not Encrypted: its routes' bodies are the graph's edits, which the generated handlers read as plain JSON", set.Name)
	}
	return ""
}

// CheckGraphOperations runs the checks of the API's @graphOperations sets
// alone (checkGraphOperations), which Run runs among the rest. The loader
// runs it before it fills the sets (graphops.Expand), which it runs before
// Run, so the operations it adds meet every check an authored operation
// meets; when it fails, the pass runs over the sets as written and
// reports its findings among the rest.
func CheckGraphOperations(schema *ir.Schema, in Input) *Result {
	r := &Result{}
	checkGraphOperations(schema, in, r)
	return r
}

// checkGraphOperations validates the API's @graphOperations sets (D138),
// before the loader fills them: each set's own form and options; the
// API's database (in.Database) and the root the set names; one set per
// root; and the names, routes and MCP handles the expansion adds.
// Everything the expansion relies on is checked here. Run runs it again
// after the expansion, which it does not refuse: an operation the
// expansion added is the set's own, and a name whose definition it added
// is its own.
func checkGraphOperations(schema *ir.Schema, in Input, r *Result) {
	var sets []*ir.OperationSet
	for _, set := range schema.OperationSets {
		if set.IsGraphOperations() {
			sets = append(sets, set)
		}
	}
	if len(sets) == 0 {
		return
	}
	if schema.Kind != ir.SchemaKindAPI {
		for _, set := range sets {
			r.errorf("", "%s: @graphOperations is only allowed in an API schema (this schema has kind %s)", set.Name, schema.Kind)
		}
		return
	}
	var formed []*ir.OperationSet
	for _, set := range sets {
		failed := len(r.Errors)
		if set.Encrypted {
			r.errorf("", "%s", GraphOperationsMessage(set, GraphOperationsEncrypted))
		}
		for _, op := range set.Operations {
			// An operation the expansion added is the set's own; any other
			// was authored.
			if op.GraphOperation == nil {
				r.errorf("", "%s", GraphOperationsMessage(set, GraphOperationsMember))
				break
			}
		}
		if set.Public {
			// Every operation the expansion adds is the set's @publicRoute,
			// which takes no service clause from its set
			// (ir.EffectiveServiceCallers): the class's would reach no route.
			switch {
			case set.Auth:
				r.errorf("", "%s: @publicRoute contradicts @auth on the class: a public route needs no caller", set.Name)
			case len(set.Permissions) > 0:
				r.errorf("", "%s: @publicRoute contradicts @requirePermission on the class: a public route needs no caller", set.Name)
			case set.ServiceCallers != nil:
				r.errorf("", "%s: @publicRoute contradicts @%s on the class: a public route needs no caller", set.Name, serviceClauseDecorator(set.ServiceCallers.Mode))
			}
		}
		checkGraphOperationsOptions(set, r)
		if len(r.Errors) == failed {
			formed = append(formed, set)
		}
	}
	if len(formed) == 0 {
		return
	}
	db, ok := graphOperationsDatabase(schema, formed, r)
	if !ok {
		return
	}
	reg := in.registry()
	served := map[string]*ir.OperationSet{}
	var planned []plannedGraphSet
	for _, set := range formed {
		typeName := set.GraphOperations.Type
		if !graphRootImportedFrom(schema, set, db, r) {
			continue
		}
		if in.Database == nil {
			// The loader reads the database for an API with such a set;
			// without it, the root cannot be checked.
			continue
		}
		if !graphOperationsRoot(set, in.Database, r) {
			continue
		}
		if other := served[typeName]; other != nil {
			r.errorf("", "%s: @graphOperations names %s, which %s names too: an API serves a root's graph from one set", set.Name, typeName, other.Name)
			continue
		}
		served[typeName] = set
		plan, err := graphops.PlanSet(set, in.Database, reg)
		if err != nil {
			r.errorf("", "%s: @graphOperations(%s): %v", set.Name, typeName, strings.TrimPrefix(err.Error(), "graphops: "+set.Name+": "))
			continue
		}
		if slices.Contains(set.GraphOperations.Operations, ir.GraphOpArrange) && !graphops.Offers(plan.Members, ir.GraphOpArrange) {
			r.errorf("", "%s: @graphOperations(%s) lists arrange, which places the rows of a kind whose order is a string key, and no member of %s's graph has one: take arrange off the list, or order a member by a string field (D137)", set.Name, typeName, typeName)
			continue
		}
		planned = append(planned, plannedGraphSet{set: set, plan: plan})
	}
	checkGraphOperationNames(schema, planned, r)
	checkGraphRoutes(schema, planned, r)
}

// plannedGraphSet is a set whose root passed its checks, and what the
// expansion adds for it.
type plannedGraphSet struct {
	set  *ir.OperationSet
	plan *graphops.Plan
}

// checkGraphOperationsOptions refuses what a set's options cannot say: an
// operation that is none of a graph's, one listed twice, a permission
// list for an operation the set does not serve or an empty one, a row
// rule that names a member other than createdBy, and an override
// permission with no row rule to lift.
func checkGraphOperationsOptions(set *ir.OperationSet, r *Result) {
	cfg := set.GraphOperations
	known := strings.Join(ir.GraphOperationNames, ", ")
	seen := map[string]bool{}
	for _, name := range cfg.Operations {
		switch {
		case !slices.Contains(ir.GraphOperationNames, name):
			r.errorf("", "%s: @graphOperations lists the operation %q, which is none of a version graph's: its operations are %s", set.Name, name, known)
		case seen[name]:
			r.errorf("", "%s: @graphOperations lists the operation %s twice", set.Name, name)
		}
		seen[name] = true
	}
	operations := make([]string, 0, len(cfg.Permissions))
	for name := range cfg.Permissions {
		operations = append(operations, name)
	}
	sort.Strings(operations)
	for _, name := range operations {
		switch {
		case !slices.Contains(ir.GraphOperationNames, name):
			r.errorf("", "%s: @graphOperations names permissions for %q, which is none of a version graph's operations: they are %s", set.Name, name, known)
		case !cfg.Serves(name):
			r.errorf("", "%s: @graphOperations names permissions for %s, which the set does not serve: its operations lists %s", set.Name, name, strings.Join(cfg.Operations, ", "))
		case len(cfg.Permissions[name]) == 0 || slices.Contains(cfg.Permissions[name], ""):
			r.errorf("", "%s: @graphOperations names no permission for %s: permissions.%s lists the permissions the operation needs, at least one", set.Name, name, name)
		case set.Public:
			r.errorf("", "%s: @graphOperations names permissions for %s, and @publicRoute on the class opens every route it serves: a public route needs no caller", set.Name, name)
		}
	}
	if len(cfg.By) > 1 || (len(cfg.By) == 1 && cfg.By[0] != ir.GraphOperationsByCreatedBy) {
		r.errorf("", "%s: @graphOperations by is [%q] for now, the ref's creator, and this names %s: the operations that write a change set are kept to the caller who branched it", set.Name, ir.GraphOperationsByCreatedBy, strings.Join(cfg.By, ", "))
	}
	if cfg.OverridePermission != "" && len(cfg.By) == 0 {
		r.errorf("", "%s: @graphOperations overridePermission lifts a row rule, and the set has none: name the ref's creator in by, by: [%q]", set.Name, ir.GraphOperationsByCreatedBy)
	}
	if len(cfg.By) > 0 && set.Public {
		r.errorf("", "%s: @graphOperations by keeps a change set to the caller who branched it, and @publicRoute on the class admits a request with no caller", set.Name)
	}
}

// graphOperationsDatabase returns the API's database (ir.Schema.Database),
// refusing each set when the API connects to none, or to several DB
// dependencies and names no authDb.
func graphOperationsDatabase(schema *ir.Schema, sets []*ir.OperationSet, r *Result) (string, bool) {
	db, _, ok := schema.Database()
	if ok {
		return db, true
	}
	var deps int
	for _, dep := range schema.Dependencies {
		if dep.Kind == ir.SchemaKindDB {
			deps++
		}
	}
	for _, set := range sets {
		typeName := set.GraphOperations.Type
		if deps > 1 {
			r.errorf("", "%s: @graphOperations(%s) runs %s's version graph on the API's database, and %s names %d DB dependencies and no authDb: set its authDb to the one that declares %s", set.Name, typeName, typeName, schema.Name, deps, typeName)
			continue
		}
		r.errorf("", "%s: @graphOperations(%s) runs %s's version graph on the API's database, and %s connects to none: set its authDb, or give it one DB dependency, the DB service that declares %s", set.Name, typeName, typeName, schema.Name, typeName)
	}
	return "", false
}

// graphRootImportedFrom reports whether the API imports set's root from
// db, its database, refusing the set when it does not.
func graphRootImportedFrom(schema *ir.Schema, set *ir.OperationSet, db string, r *Result) bool {
	typeName := set.GraphOperations.Type
	from := ""
	for _, imp := range schema.Imports {
		for _, name := range imp.Types {
			if name != typeName {
				continue
			}
			service := imp.Package
			if i := strings.LastIndex(service, "/"); i >= 0 {
				service = service[i+1:]
			}
			if service == db {
				return true
			}
			if from == "" {
				from = service
			}
		}
	}
	if from == "" {
		r.errorf("", "%s: @graphOperations names %s, which %s does not import; import it from %s, its database", set.Name, typeName, schema.Name, db)
		return false
	}
	r.errorf("", "%s: @graphOperations names %s, which %s imports from %s, not from %s, its database (its authDb, or its one DB dependency)", set.Name, typeName, schema.Name, from, db)
	return false
}

// graphOperationsRoot reports whether the type set names is the root of a
// version graph of db whose routes can address it: a table marked
// @versionGraph, keyed by one field that no route's other path parameter
// is named as, none of whose members' rows a tree holds under a member of
// its own. It refuses the set when it is not.
func graphOperationsRoot(set *ir.OperationSet, db *ir.Schema, r *Result) bool {
	typeName := set.GraphOperations.Type
	td := db.Types[typeName]
	switch {
	case td == nil:
		r.errorf("", "%s: @graphOperations names %s, which %s does not declare", set.Name, typeName, db.Name)
		return false
	case td.Role != ir.RoleDBTable || td.JsonField:
		r.errorf("", "%s: @graphOperations names %s, which is not a table of %s: a set serves the root of a version graph, a table marked @versionGraph (D138)", set.Name, typeName, db.Name)
		return false
	case td.VersionGraph == nil && td.GraphMember != nil:
		r.errorf("", "%s: @graphOperations names %s, a member of %s's version graph, not its root: name %s, the table marked @versionGraph (D138)", set.Name, typeName, td.GraphMember.Graph, td.GraphMember.Graph)
		return false
	case td.VersionGraph == nil:
		r.errorf("", "%s: @graphOperations names %s, a table of %s that is no version graph's root; a set serves a table marked @versionGraph (D138)", set.Name, typeName, db.Name)
		return false
	}
	var keys []string
	for _, fd := range td.Fields {
		if fd.Key {
			keys = append(keys, fd.Name)
		}
	}
	switch {
	case len(keys) == 0:
		r.errorf("", "%s: @graphOperations names %s, which has no @key field: its operations' routes address a root by its key", set.Name, typeName)
		return false
	case len(keys) > 1:
		r.errorf("", "%s: @graphOperations names %s, whose key is %d fields (%s): its operations' routes address a root by one", set.Name, typeName, len(keys), strings.Join(keys, ", "))
		return false
	case slices.Contains(graphops.PathParams, keys[0]):
		r.errorf("", "%s: @graphOperations names %s, whose key field is named %s, as a path parameter of its routes is (%s): serve the graph from code of the project's own, or rename the key", set.Name, typeName, keys[0], strings.Join(graphops.PathParams, ", "))
		return false
	}
	ok := true
	for _, member := range db.Types {
		if member.GraphMember == nil || member.GraphMember.Graph != typeName {
			continue
		}
		if tree := graphops.TreeMember(member.Name); slices.Contains(graphops.TreeMembers, tree) {
			r.errorf("", "%s: @graphOperations names %s, whose member %s a tree would hold under %s, the member that holds a tree's %s: rename the member type, or serve the graph from code of the project's own", set.Name, typeName, member.Name, tree, tree)
			ok = false
		}
	}
	return ok
}

// checkGraphOperationNames refuses a type or an enum the expansion adds
// whose name the API already declares or imports, or that two sets add;
// an operation it adds whose name another operation of its namespace
// takes, which every generator names its handler, its method and its tool
// by; and an MCP handle longer than @mcp accepts, or one another tool
// takes. A definition or an operation the expansion added is its own.
func checkGraphOperationNames(schema *ir.Schema, planned []plannedGraphSet, r *Result) {
	operations, handles := map[string]string{}, map[string]string{}
	for _, set := range schema.OperationSets {
		if set == nil || set.IsGraphOperations() {
			continue
		}
		namespace := setNamespace(set.Name)
		for _, op := range set.Operations {
			if op == nil {
				continue
			}
			operations[namespace+"."+op.Name] = set.Name + "." + op.Name
			if op.MCP != nil && !op.MCP.Hidden && op.MCP.Handle != "" {
				handles[op.MCP.Handle] = set.Name + "." + op.Name
			}
		}
	}
	imported := map[string]bool{}
	for _, imp := range schema.Imports {
		for _, name := range imp.Types {
			imported[name] = true
		}
	}
	declared := func(name string) bool {
		if imported[name] {
			return true
		}
		if td := schema.Types[name]; td != nil {
			return td.Origin != ir.OriginGraphOperations
		}
		if ed := schema.Enums[name]; ed != nil {
			return ed.Origin != ir.OriginGraphOperations
		}
		return schema.Unions[name] != nil || schema.Scalars[name] != nil || schema.Inputs[name] != nil
	}
	added := map[string]bool{}
	for _, p := range planned {
		typeName := p.set.GraphOperations.Type
		for _, a := range p.plan.Added {
			if declared(a.Name) || added[a.Name] {
				r.errorf("", "%s: @graphOperations(%s) adds the %s %s (%s), and %s already declares or imports one of that name; rename it", p.set.Name, typeName, a.Kind, a.Name, a.For, schema.Name)
			}
			added[a.Name] = true
		}
		namespace := setNamespace(p.set.Name)
		for _, op := range p.plan.Operations {
			owner := p.set.Name + "." + op.Name
			if other, taken := operations[namespace+"."+op.Name]; taken {
				r.errorf("", "%s: @graphOperations(%s) adds the operation %s to the namespace %s, where %s takes that name; rename that operation, or name the set for another namespace", p.set.Name, typeName, op.Name, namespace, other)
			}
			operations[namespace+"."+op.Name] = owner
			handle := op.MCP.Handle
			if handle == "" {
				// A hidden tool, of a set that admits only services.
				continue
			}
			if len(handle) > ir.MCPHandleMaxLength {
				r.errorf("", "%s: the MCP handle of %s's %s, %q, is longer than %d characters (D11)", p.set.Name, typeName, op.Name, handle, ir.MCPHandleMaxLength)
			}
			if other, taken := handles[handle]; taken {
				r.errorf("", "%s: @graphOperations(%s) adds the MCP tool %q, for %s's %s, and %s's @mcp takes that handle; rename that handle (D11)", p.set.Name, typeName, handle, typeName, op.Name, other)
			}
			handles[handle] = owner
		}
	}
}

// checkGraphRoutes refuses an operation the expansion adds whose method
// another operation of the API shares and whose path that operation's
// matches, a {parameter} read as any one segment on either side: which of
// the two a request reaches would be the router's precedence to decide,
// which differs among the generated servers. The API's own operations,
// the operations another expansion added and every set's are held to it.
func checkGraphRoutes(schema *ir.Schema, planned []plannedGraphSet, r *Result) {
	type route struct {
		owner, method, path string
	}
	var routes []route
	for _, set := range schema.OperationSets {
		if set == nil || set.IsGraphOperations() {
			continue
		}
		namespace := setNamespace(set.Name)
		for _, op := range set.Operations {
			if op == nil {
				continue
			}
			if method, path, ok := operationRoute(schema, namespace, op); ok {
				routes = append(routes, route{owner: set.Name + "." + op.Name, method: method, path: path})
			}
		}
	}
	for _, p := range planned {
		for _, op := range p.plan.Operations {
			owner, path := p.set.Name+"."+op.Name, "/api/"+op.RestPath
			for _, other := range routes {
				if other.method != op.HTTPMethod || !routesMatch(path, other.path) {
					continue
				}
				r.errorf("", "%s: the operation %s it adds is routed at %s %s, which %s's %s %s also matches: move that route, or leave %s out of the set's operations (D138)", p.set.Name, op.Name, op.HTTPMethod, path, other.owner, other.method, other.path, op.Name)
			}
			routes = append(routes, route{owner: owner, method: op.HTTPMethod, path: path})
		}
	}
}
