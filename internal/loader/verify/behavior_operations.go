package verify

import (
	"fmt"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/behaviortypes"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader/behaviorops"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// BehaviorOperationsProblem is a refusal of a @behaviorOperations set's
// own form (D103) that the TypeScript reader reports at its node and the
// pass for every form.
type BehaviorOperationsProblem int

const (
	// BehaviorOperationsMember is a member of the class, or an authored
	// operation in the set: the loader adds the set's operations.
	BehaviorOperationsMember BehaviorOperationsProblem = iota
	// BehaviorOperationsAuthenticated is an Authenticated base, which the
	// IR keeps only on the operations, so only the TypeScript reader sees
	// it.
	BehaviorOperationsAuthenticated
	// BehaviorOperationsEncrypted is an Encrypted base.
	BehaviorOperationsEncrypted
)

// BehaviorOperationsMessage words a refusal of set's own form.
func BehaviorOperationsMessage(set *ir.OperationSet, problem BehaviorOperationsProblem) string {
	switch problem {
	case BehaviorOperationsMember:
		typeName := ""
		if set.BehaviorOperations != nil {
			typeName = set.BehaviorOperations.Type
		}
		return fmt.Sprintf("%s: a @behaviorOperations class has no members: the loader adds one operation for each operation of each behavior on %s's fields, and the generated server runs it", set.Name, typeName)
	case BehaviorOperationsAuthenticated:
		return fmt.Sprintf("%s: a @behaviorOperations class does not extend Authenticated: write @auth on the class", set.Name)
	case BehaviorOperationsEncrypted:
		return fmt.Sprintf("%s: a @behaviorOperations class is not Encrypted: its routes' bodies are its behaviors' parameters, which the generated handlers read as plain JSON", set.Name)
	}
	return ""
}

// SetClauseMessage words the refusal of decorator (@auth,
// @requirePermission or @publicRoute) on the class of set, which is not
// @behaviorOperations: on every other set the user clause is each
// operation's (D103).
func SetClauseMessage(set *ir.OperationSet, decorator string) string {
	if decorator == "@auth" {
		return fmt.Sprintf("%s: @auth on a class applies only to a @behaviorOperations set; extend Authenticated, or put @auth on each method of %s", set.Name, set.Name)
	}
	return fmt.Sprintf("%s: %s on a class applies only to a @behaviorOperations set; put it on each method of %s", set.Name, decorator, set.Name)
}

// CheckBehaviorOperations runs the checks of the API's @behaviorOperations
// sets alone (checkBehaviorOperations), which Run runs among the rest. The
// loader runs it before it fills the sets (behaviorops.Expand), which it
// runs before Run, so the operations it adds meet every check an authored
// operation meets; when it fails, the pass runs over the sets as written
// and reports its findings among the rest.
func CheckBehaviorOperations(schema *ir.Schema, in Input) *Result {
	r := &Result{}
	checkBehaviorOperations(schema, in, r)
	return r
}

// checkBehaviorOperations validates the API's @behaviorOperations sets
// (D103) and the set-level user clause, before the loader fills them:
// each set's own form; the clause on no other set; the API's database
// (in.Database), the table the set names and the behaviors on its fields;
// one set per table; and the names and MCP handles the expansion adds.
// Everything the expansion relies on is checked here, so the expansion
// itself cannot fail. Run runs it again after the expansion, which it
// does not refuse: an operation the expansion added is the set's own, and
// a name whose definition it added is its own.
func checkBehaviorOperations(schema *ir.Schema, in Input, r *Result) {
	var sets []*ir.OperationSet
	for _, set := range schema.OperationSets {
		if set == nil {
			continue
		}
		if set.IsBehaviorOperations() {
			sets = append(sets, set)
			continue
		}
		if set.Auth {
			r.errorf("", "%s", SetClauseMessage(set, "@auth"))
		}
		if len(set.Permissions) > 0 {
			r.errorf("", "%s", SetClauseMessage(set, "@requirePermission"))
		}
		if set.Public {
			r.errorf("", "%s", SetClauseMessage(set, "@publicRoute"))
		}
	}
	if len(sets) == 0 {
		return
	}
	if schema.Kind != ir.SchemaKindAPI {
		for _, set := range sets {
			r.errorf("", "%s: @behaviorOperations is only allowed in an API schema (this schema has kind %s)", set.Name, schema.Kind)
		}
		return
	}
	var formed []*ir.OperationSet
	for _, set := range sets {
		failed := len(r.Errors)
		if set.Encrypted {
			r.errorf("", "%s", BehaviorOperationsMessage(set, BehaviorOperationsEncrypted))
		}
		for _, op := range set.Operations {
			// An operation the expansion added is the set's own; any other
			// was authored.
			if op.BehaviorOperation == nil {
				r.errorf("", "%s", BehaviorOperationsMessage(set, BehaviorOperationsMember))
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
		if len(r.Errors) == failed {
			formed = append(formed, set)
		}
	}
	if len(formed) == 0 {
		return
	}
	db, ok := behaviorOperationsDatabase(schema, formed, r)
	if !ok {
		return
	}
	reg := in.registry()
	served := map[string]*ir.OperationSet{}
	var planned []plannedSet
	for _, set := range formed {
		typeName := set.BehaviorOperations.Type
		if !tableImportedFrom(schema, set, db, r) {
			continue
		}
		if in.Database == nil {
			// The loader reads the database for an API with such a set;
			// without it, the table cannot be checked.
			continue
		}
		td, ok := behaviorOperationsTable(set, in.Database, reg, r)
		if !ok {
			continue
		}
		if other := served[typeName]; other != nil {
			r.errorf("", "%s: @behaviorOperations names %s, which %s names too: an API serves a table's behavior operations from one set", set.Name, typeName, other.Name)
			continue
		}
		served[typeName] = set
		plan, err := behaviorops.PlanSet(set, in.Database, reg)
		if err != nil {
			r.errorf("", "%s: @behaviorOperations(%s): %v", set.Name, typeName, err)
			continue
		}
		planned = append(planned, plannedSet{set: set, td: td, plan: plan})
	}
	checkBehaviorOperationNames(schema, planned, r)
}

// plannedSet is a set whose table passed its checks, and what the
// expansion adds for it.
type plannedSet struct {
	set  *ir.OperationSet
	td   *ir.TypeDef
	plan *behaviorops.Plan
}

// behaviorOperationsDatabase returns the API's database
// (ir.Schema.Database), refusing each set when the API connects to none,
// or to several DB dependencies and names no authDb.
func behaviorOperationsDatabase(schema *ir.Schema, sets []*ir.OperationSet, r *Result) (string, bool) {
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
		typeName := set.BehaviorOperations.Type
		if deps > 1 {
			r.errorf("", "%s: @behaviorOperations(%s) runs %s's behaviors on the API's database, and %s names %d DB dependencies and no authDb: set its authDb to the one that declares %s", set.Name, typeName, typeName, schema.Name, deps, typeName)
			continue
		}
		r.errorf("", "%s: @behaviorOperations(%s) runs %s's behaviors on the API's database, and %s connects to none: set its authDb, or give it one DB dependency, the DB service that declares %s", set.Name, typeName, typeName, schema.Name, typeName)
	}
	return "", false
}

// tableImportedFrom reports whether the API imports set's table from db, its
// database, refusing the set when it does not.
func tableImportedFrom(schema *ir.Schema, set *ir.OperationSet, db string, r *Result) bool {
	typeName := set.BehaviorOperations.Type
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
		r.errorf("", "%s: @behaviorOperations names %s, which %s does not import; import it from %s, its database", set.Name, typeName, schema.Name, db)
		return false
	}
	r.errorf("", "%s: @behaviorOperations names %s, which %s imports from %s, not from %s, its database (its authDb, or its one DB dependency)", set.Name, typeName, schema.Name, from, db)
	return false
}

// behaviorOperationsTable returns the table set names, a table of db one
// of whose fields is a behavior, keyed by one field, each of whose uses'
// operations runs on a row, refusing the set when it is not.
func behaviorOperationsTable(set *ir.OperationSet, db *ir.Schema, reg *registry.Registry, r *Result) (*ir.TypeDef, bool) {
	typeName := set.BehaviorOperations.Type
	td := db.Types[typeName]
	switch {
	case td == nil:
		r.errorf("", "%s: @behaviorOperations names %s, which %s does not declare", set.Name, typeName, db.Name)
		return nil, false
	case td.Role != ir.RoleDBTable || td.JsonField:
		r.errorf("", "%s: @behaviorOperations names %s, which is not a table of %s: a set serves a table one of whose fields is a behavior (D103)", set.Name, typeName, db.Name)
		return nil, false
	case td.IsEngineType():
		r.errorf("", "%s: @behaviorOperations names %s, an engine type of %s (D58), whose engine serves its operations; a set serves a table one of whose fields is a behavior (D103)", set.Name, typeName, db.Name)
		return nil, false
	case len(td.FieldBehaviors()) == 0:
		r.errorf("", "%s: @behaviorOperations names %s, a table of %s none of whose fields is a behavior; a set serves a table one of whose fields is, such as status: Workflow<{ ... }> (D103)", set.Name, typeName, db.Name)
		return nil, false
	}
	var keys []string
	for _, fd := range td.Fields {
		if fd.Key {
			keys = append(keys, fd.Name)
		}
	}
	switch {
	case len(keys) == 0:
		r.errorf("", "%s: @behaviorOperations names %s, which has no @key field: its operations' routes address a row by its key", set.Name, typeName)
		return nil, false
	case len(keys) > 1:
		r.errorf("", "%s: @behaviorOperations names %s, whose key is %d fields (%s): its operations' routes address a row by one", set.Name, typeName, len(keys), strings.Join(keys, ", "))
		return nil, false
	}
	ok := true
	for _, ref := range td.FieldBehaviors() {
		declaration, registered := reg.Behavior(ref.Name)
		if !registered {
			// The database's own load refuses it.
			ok = false
			continue
		}
		for _, op := range declaration.Operations {
			if op.Scope == registry.OperationScopeSchema || op.Scope == registry.OperationScopeNamespace {
				r.errorf("", "%s: %s's %s is %s, whose operation %s runs on the %s as a whole, not on a row: @behaviorOperations serves the operations a behavior runs on a row (D103)", set.Name, typeName, ref.UseID(), behaviortypes.An(ref.Name), op.Name, op.Scope)
				ok = false
			}
		}
	}
	return td, ok
}

// checkBehaviorOperationNames refuses a type or an enum the expansion adds
// whose name the API already declares or imports, or that two sets' uses
// add; an operation it adds whose name another operation of its namespace
// takes, which every generator names its handler, its method and its tool
// by; and an MCP handle longer than @mcp accepts, or one another tool
// takes. A definition or an operation the expansion added is its own.
func checkBehaviorOperationNames(schema *ir.Schema, planned []plannedSet, r *Result) {
	// The operations of the API's other sets, by namespace and name, and
	// the handles of their visible tools, each with the operation that
	// takes it; the planned operations join them as they are checked.
	operations, handles := map[string]string{}, map[string]string{}
	for _, set := range schema.OperationSets {
		if set == nil || set.IsBehaviorOperations() {
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
			return td.Origin != ir.OriginBehavior
		}
		if ed := schema.Enums[name]; ed != nil {
			return ed.Origin != ir.OriginBehavior
		}
		return schema.Unions[name] != nil || schema.Scalars[name] != nil
	}
	added := map[string]bool{}
	for _, p := range planned {
		typeName := p.set.BehaviorOperations.Type
		for _, a := range p.plan.Added {
			if declared(a.Name) || added[a.Name] {
				r.errorf("", "%s: @behaviorOperations(%s) adds the %s %s, for %s's %s %s, and %s already declares or imports one of that name; rename it", p.set.Name, typeName, a.Kind, a.Name, typeName, a.Use, a.Operation, schema.Name)
			}
			added[a.Name] = true
		}
		namespace := setNamespace(p.set.Name)
		for _, op := range p.plan.Operations {
			marker := op.BehaviorOperation
			owner := p.set.Name + "." + op.Name
			if other, taken := operations[namespace+"."+op.Name]; taken {
				r.errorf("", "%s: @behaviorOperations(%s) adds the operation %s, for %s's %s %s, to the namespace %s, where %s takes that name; rename that operation, or name the set for another namespace", p.set.Name, typeName, op.Name, typeName, marker.Use, marker.Operation, namespace, other)
			}
			operations[namespace+"."+op.Name] = owner
			handle := op.MCP.Handle
			if handle == "" {
				// A hidden tool, of a set that admits only services.
				continue
			}
			if len(handle) > ir.MCPHandleMaxLength {
				r.errorf("", "%s: the MCP handle of %s's %s %s, %q, is longer than %d characters (D11)", p.set.Name, typeName, marker.Use, marker.Operation, handle, ir.MCPHandleMaxLength)
			}
			if other, taken := handles[handle]; taken {
				r.errorf("", "%s: @behaviorOperations(%s) adds the MCP tool %q, for %s's %s %s, and %s's @mcp takes that handle; rename that handle (D11)", p.set.Name, typeName, handle, typeName, marker.Use, marker.Operation, other)
			}
			handles[handle] = owner
		}
	}
}

// setNamespace is the namespace the generators put a set's operations in,
// as apigen names it (extractNamespace): its name without a Queries,
// Mutations, Operations or Actions suffix, in kebab case; root for Query
// and Mutation.
func setNamespace(set string) string {
	if set == "Query" || set == "Mutation" {
		return "root"
	}
	name := set
	for _, suffix := range []string{"Queries", "Mutations", "Operations", "Actions"} {
		name = strings.TrimSuffix(name, suffix)
	}
	if name == "" {
		name = set
	}
	return codegen.ToKebabCase(name)
}
