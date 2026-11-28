package verify

import (
	"fmt"
	"strings"

	"github.com/parable-work/superschematic/internal/loader/triageops"
	ir "github.com/parable-work/superschematic/ir"
)

// TriageOperationsProblem is a refusal of a @triageOperations set's own
// form (#746) that the TypeScript reader reports at its node and the pass
// for every form.
type TriageOperationsProblem int

const (
	// TriageOperationsMember is a member of the class, or an authored
	// operation in the set: the loader adds the set's operations.
	TriageOperationsMember TriageOperationsProblem = iota
	// TriageOperationsAuthenticated is an Authenticated base, which the IR
	// keeps only on the operations, so only the TypeScript reader sees it.
	TriageOperationsAuthenticated
	// TriageOperationsEncrypted is an Encrypted base.
	TriageOperationsEncrypted
)

// TriageOperationsMessage words a refusal of set's own form.
func TriageOperationsMessage(set *ir.OperationSet, problem TriageOperationsProblem) string {
	switch problem {
	case TriageOperationsMember:
		return fmt.Sprintf("%s: a @triageOperations class has no members: the loader adds the operations of the inbound path's triage, and the generated server runs them; an operation that needs code of the project's own is a method of another class, which calls the inbound path", set.Name)
	case TriageOperationsAuthenticated:
		return fmt.Sprintf("%s: a @triageOperations class does not extend Authenticated: write @auth on the class", set.Name)
	case TriageOperationsEncrypted:
		return fmt.Sprintf("%s: a @triageOperations class is not Encrypted: its routes' bodies are a settle's party and trust, which the generated handlers read as plain JSON", set.Name)
	}
	return ""
}

// CheckTriageOperations runs the checks of the API's @triageOperations
// set alone (checkTriageOperations), which Run runs among the rest. The
// loader runs it before it fills the set (triageops.Expand), which it runs
// before Run, so the operations it adds meet every check an authored
// operation meets.
func CheckTriageOperations(schema *ir.Schema, in Input) *Result {
	r := &Result{}
	checkTriageOperations(schema, in, r)
	return r
}

// checkTriageOperations validates the API's @triageOperations set (#746),
// before the loader fills it: the set's own form and options, one set per
// API, the API's database (in.Database), which declares the conversations
// and the party table the set names, the type refersTo names, and the
// names, routes and MCP handles the expansion adds. Run runs it again after
// the expansion, which it does not refuse: an operation the expansion added
// is the set's own, and a name whose definition it added is its own.
func checkTriageOperations(schema *ir.Schema, in Input, r *Result) {
	var sets []*ir.OperationSet
	for _, set := range schema.OperationSets {
		if set.IsTriageOperations() {
			sets = append(sets, set)
		}
	}
	if len(sets) == 0 {
		return
	}
	if schema.Kind != ir.SchemaKindAPI {
		for _, set := range sets {
			r.errorf("", "%s: @triageOperations is only allowed in an API schema (this schema has kind %s)", set.Name, schema.Kind)
		}
		return
	}
	for _, set := range sets[1:] {
		r.errorf("", "%s: @triageOperations appears on %s too: an API serves its triage from one set, at /api/%s", set.Name, sets[0].Name, ir.TriagePath)
	}
	set := sets[0]
	failed := len(r.Errors)
	if set.Encrypted {
		r.errorf("", "%s", TriageOperationsMessage(set, TriageOperationsEncrypted))
	}
	for _, op := range set.Operations {
		if op.TriageOperation == nil {
			r.errorf("", "%s", TriageOperationsMessage(set, TriageOperationsMember))
			break
		}
	}
	if set.Public {
		r.errorf("", "%s: @publicRoute on a @triageOperations class would open the inbound path's triage to anyone: its routes need the triage permission (%s unless the class's @requirePermission names another)", set.Name, ir.TriagePermission)
	}
	if cfg := set.TriageOperations; cfg.RefersTo != "" {
		td := schema.Types[cfg.RefersTo]
		switch {
		case td == nil:
			r.errorf("", "%s: @triageOperations' refersTo names %s, which is no type of %s: name the API's type one of its operations lists the %s rows as", set.Name, cfg.RefersTo, schema.Name, cfg.Party)
		case td.Origin == ir.OriginTriage:
			r.errorf("", "%s: @triageOperations' refersTo names %s, a type the set adds: name the API's type one of its operations lists the %s rows as", set.Name, cfg.RefersTo, cfg.Party)
		}
	}
	if len(r.Errors) > failed {
		return
	}
	db, _, ok := schema.Database()
	if !ok {
		r.errorf("", "%s: @triageOperations(%s) runs the inbound path of the API's database, and %s connects to none: set its authDb, or give it one DB dependency, the DB service that declares %s and the @conversations tables", set.Name, set.TriageOperations.Party, schema.Name, set.TriageOperations.Party)
		return
	}
	if !triagePartyImportedFrom(schema, set, db, r) || in.Database == nil {
		// The loader reads the database for an API with such a set;
		// without it, the tables cannot be checked.
		return
	}
	if !triageTables(set, in.Database, r) {
		return
	}
	plan, err := triageops.PlanSet(set, in.Database, in.registry())
	if err != nil {
		r.errorf("", "%s: @triageOperations(%s): %v", set.Name, set.TriageOperations.Party, strings.TrimPrefix(err.Error(), "triageops: "+set.Name+": "))
		return
	}
	checkTriageNames(schema, set, plan, r)
	checkTriageRoutes(schema, set, plan, r)
}

// triagePartyImportedFrom reports whether the API imports set's party
// table from db, its database, refusing the set when it does not.
func triagePartyImportedFrom(schema *ir.Schema, set *ir.OperationSet, db string, r *Result) bool {
	party := set.TriageOperations.Party
	from := ""
	for _, imp := range schema.Imports {
		for _, name := range imp.Types {
			if name != party {
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
		r.errorf("", "%s: @triageOperations names %s, which %s does not import; import it from %s, its database", set.Name, party, schema.Name, db)
		return false
	}
	r.errorf("", "%s: @triageOperations names %s, which %s imports from %s, not from %s, its database (its authDb, or its one DB dependency)", set.Name, party, schema.Name, from, db)
	return false
}

// triageTables reports whether db declares the conversations, whose
// messages the inbound path holds in triage, and the party table set
// names, refusing the set when it does not.
func triageTables(set *ir.OperationSet, db *ir.Schema, r *Result) bool {
	party := set.TriageOperations.Party
	td := db.Types[party]
	switch {
	case td == nil:
		r.errorf("", "%s: @triageOperations names %s, which %s does not declare", set.Name, party, db.Name)
		return false
	case td.Party == nil:
		r.errorf("", "%s: @triageOperations names %s, which is no @party table of %s: a held message is settled onto a party row (D157)", set.Name, party, db.Name)
		return false
	case db.ConversationsTable(ir.MessagesPart) == nil:
		r.errorf("", "%s: @triageOperations(%s) serves the inbound path's triage, and %s declares no @conversations(%q) table, whose messages it holds (D157)", set.Name, party, db.Name, ir.MessagesPart)
		return false
	}
	return true
}

// checkTriageNames refuses a type or an enum the expansion adds whose name
// the API already declares or imports, an operation it adds whose name
// another operation of its namespace takes, and an MCP handle another tool
// takes. A definition or an operation the expansion added is its own.
func checkTriageNames(schema *ir.Schema, set *ir.OperationSet, plan *triageops.Plan, r *Result) {
	operations, handles := map[string]string{}, map[string]string{}
	for _, other := range schema.OperationSets {
		if other == nil || other.IsTriageOperations() {
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
			return td.Origin != ir.OriginTriage
		}
		if ed := schema.Enums[name]; ed != nil {
			return ed.Origin != ir.OriginTriage
		}
		return schema.Unions[name] != nil || schema.Scalars[name] != nil || schema.Inputs[name] != nil
	}
	for _, a := range plan.Added {
		if declared(a.Name) {
			r.errorf("", "%s: @triageOperations adds the %s %s, and %s already declares or imports one of that name; rename it", set.Name, a.Kind, a.Name, schema.Name)
		}
	}
	namespace := setNamespace(set.Name)
	for _, op := range plan.Operations {
		if other, taken := operations[namespace+"."+op.Name]; taken {
			r.errorf("", "%s: @triageOperations adds the operation %s to the namespace %s, where %s takes that name; rename that operation, or name the set for another namespace", set.Name, op.Name, namespace, other)
		}
		if op.MCP == nil || op.MCP.Hidden || op.MCP.Handle == "" {
			continue
		}
		if other, taken := handles[op.MCP.Handle]; taken {
			r.errorf("", "%s: @triageOperations adds the MCP tool %q, for %s, and %s's @mcp takes that handle; rename that handle (D11)", set.Name, op.MCP.Handle, op.Name, other)
		}
	}
}

// checkTriageRoutes refuses an operation the expansion adds whose method
// another operation of the API shares and whose path that operation's
// matches, as checkRowFeatureRoutes does.
func checkTriageRoutes(schema *ir.Schema, set *ir.OperationSet, plan *triageops.Plan, r *Result) {
	type route struct {
		owner, method, path string
	}
	var routes []route
	for _, other := range schema.OperationSets {
		if other == nil || other.IsTriageOperations() {
			continue
		}
		namespace := setNamespace(other.Name)
		for _, op := range other.Operations {
			if op == nil {
				continue
			}
			if method, path, ok := operationRoute(schema, namespace, op); ok {
				routes = append(routes, route{owner: other.Name + "." + op.Name, method: method, path: path})
			}
		}
	}
	for _, op := range plan.Operations {
		path := "/api/" + op.RestPath
		for _, other := range routes {
			if other.method != op.HTTPMethod || !routesMatch(path, other.path) {
				continue
			}
			r.errorf("", "%s: the operation %s it adds is routed at %s %s, which %s's %s %s also matches: move that route (#746)", set.Name, op.Name, op.HTTPMethod, path, other.owner, other.method, other.path)
		}
	}
}
