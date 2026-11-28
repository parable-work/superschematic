package verify

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/loader/rowfeatureops"
	ir "github.com/parable-work/superschematic/ir"
)

// RowFeatureOperationsProblem is a refusal of a @rowFeatureOperations
// set's own form (#723) that the TypeScript reader reports at its node and
// the pass for every form.
type RowFeatureOperationsProblem int

const (
	// RowFeatureOperationsMember is a member of the class, or an authored
	// operation in the set: the loader adds the set's operations.
	RowFeatureOperationsMember RowFeatureOperationsProblem = iota
	// RowFeatureOperationsAuthenticated is an Authenticated base, which
	// the IR keeps only on the operations, so only the TypeScript reader
	// sees it.
	RowFeatureOperationsAuthenticated
	// RowFeatureOperationsEncrypted is an Encrypted base.
	RowFeatureOperationsEncrypted
)

// RowFeatureOperationsMessage words a refusal of set's own form.
func RowFeatureOperationsMessage(set *ir.OperationSet, problem RowFeatureOperationsProblem) string {
	switch problem {
	case RowFeatureOperationsMember:
		typeName := ""
		if set.RowFeatureOperations != nil {
			typeName = set.RowFeatureOperations.Type
		}
		return fmt.Sprintf("%s: a @rowFeatureOperations class has no members: the loader adds one operation for each operation of %s's references and files the set serves, and the generated server runs it; an operation that needs code of the project's own is a method of another class, which calls the database's references and attachments", set.Name, typeName)
	case RowFeatureOperationsAuthenticated:
		return fmt.Sprintf("%s: a @rowFeatureOperations class does not extend Authenticated: write @auth on the class", set.Name)
	case RowFeatureOperationsEncrypted:
		return fmt.Sprintf("%s: a @rowFeatureOperations class is not Encrypted: its routes' bodies are references and files, which the generated handlers read as plain JSON", set.Name)
	}
	return ""
}

// CheckRowFeatureOperations runs the checks of the API's
// @rowFeatureOperations sets alone (checkRowFeatureOperations), which Run
// runs among the rest. The loader runs it before it fills the sets
// (rowfeatureops.Expand), which it runs before Run, so the operations it
// adds meet every check an authored operation meets.
func CheckRowFeatureOperations(schema *ir.Schema, in Input) *Result {
	r := &Result{}
	checkRowFeatureOperations(schema, in, r)
	return r
}

// plannedRowSet is a set whose table passed its checks, and what the
// expansion adds for it.
type plannedRowSet struct {
	set  *ir.OperationSet
	plan *rowfeatureops.Plan
}

// checkRowFeatureOperations validates the API's @rowFeatureOperations sets
// (#723), before the loader fills them: each set's own form and options;
// the API's database (in.Database) and the table the set names, which
// offers each operation the options list and at least one; the bucket the
// file operations read; one set per table; and the names, routes and MCP
// handles the expansion adds. Run runs it again after the expansion,
// which it does not refuse: an operation the expansion added is the set's
// own, and a name whose definition it added is its own.
func checkRowFeatureOperations(schema *ir.Schema, in Input, r *Result) {
	var sets []*ir.OperationSet
	for _, set := range schema.OperationSets {
		if set.IsRowFeatureOperations() {
			sets = append(sets, set)
		}
	}
	if len(sets) == 0 {
		return
	}
	if schema.Kind != ir.SchemaKindAPI {
		for _, set := range sets {
			r.errorf("", "%s: @rowFeatureOperations is only allowed in an API schema (this schema has kind %s)", set.Name, schema.Kind)
		}
		return
	}
	var formed []*ir.OperationSet
	for _, set := range sets {
		failed := len(r.Errors)
		if set.Encrypted {
			r.errorf("", "%s", RowFeatureOperationsMessage(set, RowFeatureOperationsEncrypted))
		}
		for _, op := range set.Operations {
			if op.RowFeatureOperation == nil {
				r.errorf("", "%s", RowFeatureOperationsMessage(set, RowFeatureOperationsMember))
				break
			}
		}
		if set.Public {
			switch {
			case set.Auth:
				r.errorf("", "%s: @publicRoute contradicts @auth on the class: a public route needs no caller", set.Name)
			case len(set.Permissions) > 0:
				r.errorf("", "%s: @publicRoute contradicts @requirePermission on the class: a public route needs no caller", set.Name)
			case set.ServiceCallers != nil:
				r.errorf("", "%s: @publicRoute contradicts @%s on the class: a public route needs no caller", set.Name, serviceClauseDecorator(set.ServiceCallers.Mode))
			}
		}
		checkRowFeatureOptions(set, r)
		if len(r.Errors) == failed {
			formed = append(formed, set)
		}
	}
	if len(formed) == 0 {
		return
	}
	db, ok := rowFeatureDatabase(schema, formed, r)
	if !ok {
		return
	}
	reg := in.registry()
	served := map[string]*ir.OperationSet{}
	var planned []plannedRowSet
	for _, set := range formed {
		typeName := set.RowFeatureOperations.Type
		if !rowFeatureTableImportedFrom(schema, set, db, r) {
			continue
		}
		if in.Database == nil {
			// The loader reads the database for an API with such a set;
			// without it, the table cannot be checked.
			continue
		}
		if !rowFeatureTable(set, in.Database, r) {
			continue
		}
		if other := served[typeName]; other != nil {
			r.errorf("", "%s: @rowFeatureOperations names %s, which %s names too: an API serves a table's references and files from one set", set.Name, typeName, other.Name)
			continue
		}
		served[typeName] = set
		if !rowFeatureBucket(schema, set, in.Database, r) {
			continue
		}
		plan, err := rowfeatureops.PlanSet(set, schema, in.Database, reg)
		if err != nil {
			r.errorf("", "%s: @rowFeatureOperations(%s): %v", set.Name, typeName, strings.TrimPrefix(err.Error(), "rowfeatureops: "+set.Name+": "))
			continue
		}
		planned = append(planned, plannedRowSet{set: set, plan: plan})
	}
	checkRowFeatureNames(schema, planned, r)
	checkRowFeatureRoutes(schema, planned, r)
}

// checkRowFeatureOptions refuses what a set's options cannot say: an
// operation that is none of the set's, one listed twice, and a permission
// list for an operation the set does not serve, an empty one, or one
// beside @publicRoute.
func checkRowFeatureOptions(set *ir.OperationSet, r *Result) {
	cfg := set.RowFeatureOperations
	known := strings.Join(ir.RowFeatureOperationNames, ", ")
	seen := map[string]bool{}
	for _, name := range cfg.Operations {
		switch {
		case !slices.Contains(ir.RowFeatureOperationNames, name):
			r.errorf("", "%s: @rowFeatureOperations lists the operation %q, which is none of a table's references and files: its operations are %s", set.Name, name, known)
		case seen[name]:
			r.errorf("", "%s: @rowFeatureOperations lists the operation %s twice", set.Name, name)
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
		case !slices.Contains(ir.RowFeatureOperationNames, name):
			r.errorf("", "%s: @rowFeatureOperations names permissions for %q, which is none of its operations: they are %s", set.Name, name, known)
		case !cfg.Serves(name):
			r.errorf("", "%s: @rowFeatureOperations names permissions for %s, which the set does not serve: its operations lists %s", set.Name, name, strings.Join(cfg.Operations, ", "))
		case len(cfg.Permissions[name]) == 0 || slices.Contains(cfg.Permissions[name], ""):
			r.errorf("", "%s: @rowFeatureOperations names no permission for %s: permissions.%s lists the permissions the operation needs, at least one", set.Name, name, name)
		case set.Public:
			r.errorf("", "%s: @rowFeatureOperations names permissions for %s, and @publicRoute on the class opens every route it serves: a public route needs no caller", set.Name, name)
		}
	}
}

// rowFeatureDatabase returns the API's database (ir.Schema.Database),
// refusing each set when the API connects to none, or to several DB
// dependencies and names no authDb.
func rowFeatureDatabase(schema *ir.Schema, sets []*ir.OperationSet, r *Result) (string, bool) {
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
		typeName := set.RowFeatureOperations.Type
		if deps > 1 {
			r.errorf("", "%s: @rowFeatureOperations(%s) runs %s's references and files on the API's database, and %s names %d DB dependencies and no authDb: set its authDb to the one that declares %s", set.Name, typeName, typeName, schema.Name, deps, typeName)
			continue
		}
		r.errorf("", "%s: @rowFeatureOperations(%s) runs %s's references and files on the API's database, and %s connects to none: set its authDb, or give it one DB dependency, the DB service that declares %s", set.Name, typeName, typeName, schema.Name, typeName)
	}
	return "", false
}

// rowFeatureTableImportedFrom reports whether the API imports set's table
// from db, its database, refusing the set when it does not.
func rowFeatureTableImportedFrom(schema *ir.Schema, set *ir.OperationSet, db string, r *Result) bool {
	typeName := set.RowFeatureOperations.Type
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
		r.errorf("", "%s: @rowFeatureOperations names %s, which %s does not import; import it from %s, its database", set.Name, typeName, schema.Name, db)
		return false
	}
	r.errorf("", "%s: @rowFeatureOperations names %s, which %s imports from %s, not from %s, its database (its authDb, or its one DB dependency)", set.Name, typeName, schema.Name, from, db)
	return false
}

// rowFeatureTable reports whether the type set names is a table of db
// that offers each operation the set's options list, and at least one:
// keyed by one field that is not named as a file's path parameter. It
// refuses the set when it is not.
func rowFeatureTable(set *ir.OperationSet, db *ir.Schema, r *Result) bool {
	cfg := set.RowFeatureOperations
	typeName := cfg.Type
	td := db.Types[typeName]
	switch {
	case td == nil:
		r.errorf("", "%s: @rowFeatureOperations names %s, which %s does not declare", set.Name, typeName, db.Name)
		return false
	case td.Role != ir.RoleDBTable || td.JsonField:
		r.errorf("", "%s: @rowFeatureOperations names %s, which is not a table of %s: a set serves a table marked @references or @attachments, or one a reference may point at (D154)", set.Name, typeName, db.Name)
		return false
	}
	var keys []string
	for _, fd := range td.Fields {
		if fd != nil && fd.Key {
			keys = append(keys, fd.Name)
		}
	}
	switch {
	case len(keys) == 0:
		r.errorf("", "%s: @rowFeatureOperations names %s, which has no @key field: its operations' routes address a row by its key", set.Name, typeName)
		return false
	case len(keys) > 1:
		r.errorf("", "%s: @rowFeatureOperations names %s, whose key is %d fields (%s): its operations' routes address a row by one", set.Name, typeName, len(keys), strings.Join(keys, ", "))
		return false
	case keys[0] == ir.RowFeatureFileParam:
		r.errorf("", "%s: @rowFeatureOperations names %s, whose key field is named %s, as the path parameter of a file's routes is: rename the key, or serve its files from code of the project's own", set.Name, typeName, keys[0])
		return false
	}
	offered := 0
	for _, op := range ir.RowFeatureOperationNames {
		if rowfeatureops.Offers(db, td, op) {
			offered++
		}
	}
	if offered == 0 {
		r.errorf("", "%s: @rowFeatureOperations names %s, which keeps no references (@references), holds no files (@attachments) and is no table a reference may point at: mark it @references or @attachments (D154)", set.Name, typeName)
		return false
	}
	ok := true
	for _, op := range cfg.Operations {
		if slices.Contains(ir.RowFeatureOperationNames, op) && !rowfeatureops.Offers(db, td, op) {
			why := map[string]string{
				ir.RowFeatureReferences:  "which keeps no references: mark it @references",
				ir.RowFeatureBacklinks:   "which no reference may point at: a target is a plain table of one key, of a database one of whose tables is marked @references or keeps a conversation (Comments)",
				ir.RowFeatureAttachments: "which holds no files: mark it @attachments",
			}[ir.RowFeatureOf(op)]
			r.errorf("", "%s: @rowFeatureOperations lists %s, and %s is a table %s (D154)", set.Name, op, typeName, why)
			ok = false
		}
	}
	return ok
}

// rowFeatureBucket reports whether a set that serves a file operation
// reads a bucket the API lists: the one its options name, or the API's
// one bucket. It refuses the set when it does not, and a bucket named for
// a set that serves none.
func rowFeatureBucket(schema *ir.Schema, set *ir.OperationSet, db *ir.Schema, r *Result) bool {
	cfg := set.RowFeatureOperations
	td := db.Types[cfg.Type]
	files := false
	for _, op := range ir.RowFeatureOperationNames {
		if ir.RowFeatureOperationTakesBucket(op) && cfg.Serves(op) && rowfeatureops.Offers(db, td, op) {
			files = true
		}
	}
	var listed []string
	for _, b := range schema.Buckets {
		listed = append(listed, b.Name)
	}
	switch {
	case cfg.Bucket != "" && !slices.Contains(listed, cfg.Bucket):
		if len(listed) == 0 {
			r.errorf("", "%s: @rowFeatureOperations names the bucket %s, and %s lists no bucket: add it to the API's buckets", set.Name, cfg.Bucket, schema.Name)
		} else {
			r.errorf("", "%s: @rowFeatureOperations names the bucket %s, which is none of %s's buckets (%s)", set.Name, cfg.Bucket, schema.Name, strings.Join(listed, ", "))
		}
		return false
	case !files && cfg.Bucket != "":
		r.errorf("", "%s: @rowFeatureOperations names the bucket %s, and the set serves no operation that reads one: take bucket off its options", set.Name, cfg.Bucket)
		return false
	case files && cfg.Bucket == "" && len(listed) == 0:
		r.errorf("", "%s: @rowFeatureOperations(%s) serves %s's files, which an upload puts in a bucket, and %s lists none: add the Bucket service that holds them to the API's buckets", set.Name, cfg.Type, cfg.Type, schema.Name)
		return false
	case files && cfg.Bucket == "" && len(listed) > 1:
		r.errorf("", "%s: @rowFeatureOperations(%s) serves %s's files, and %s lists %d buckets (%s): name the one that holds them, { bucket: %q }", set.Name, cfg.Type, cfg.Type, schema.Name, len(listed), strings.Join(listed, ", "), listed[0])
		return false
	}
	return true
}

// checkRowFeatureNames refuses a type or an enum the expansion adds whose
// name the API already declares or imports, an operation it adds whose
// name another operation of its namespace takes, and an MCP handle longer
// than @mcp accepts or one another tool takes. A definition or an
// operation the expansion added is its own, and every set adds the same
// types.
func checkRowFeatureNames(schema *ir.Schema, planned []plannedRowSet, r *Result) {
	operations, handles := map[string]string{}, map[string]string{}
	for _, set := range schema.OperationSets {
		if set == nil || set.IsRowFeatureOperations() {
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
			return td.Origin != ir.OriginRowFeatures
		}
		if ed := schema.Enums[name]; ed != nil {
			return ed.Origin != ir.OriginRowFeatures
		}
		return schema.Unions[name] != nil || schema.Scalars[name] != nil || schema.Inputs[name] != nil
	}
	reported := map[string]bool{}
	for _, p := range planned {
		typeName := p.set.RowFeatureOperations.Type
		for _, a := range p.plan.Added {
			if declared(a.Name) && !reported[a.Name] {
				reported[a.Name] = true
				r.errorf("", "%s: @rowFeatureOperations(%s) adds the %s %s, and %s already declares or imports one of that name; rename it", p.set.Name, typeName, a.Kind, a.Name, schema.Name)
			}
		}
		namespace := setNamespace(p.set.Name)
		for _, op := range p.plan.Operations {
			owner := p.set.Name + "." + op.Name
			if other, taken := operations[namespace+"."+op.Name]; taken {
				r.errorf("", "%s: @rowFeatureOperations(%s) adds the operation %s to the namespace %s, where %s takes that name; rename that operation, or name the set for another namespace", p.set.Name, typeName, op.Name, namespace, other)
			}
			operations[namespace+"."+op.Name] = owner
			if op.MCP == nil || op.MCP.Hidden || op.MCP.Handle == "" {
				continue
			}
			handle := op.MCP.Handle
			if len(handle) > ir.MCPHandleMaxLength {
				r.errorf("", "%s: the MCP handle of %s's %s, %q, is longer than %d characters (D11)", p.set.Name, typeName, op.Name, handle, ir.MCPHandleMaxLength)
			}
			if other, taken := handles[handle]; taken {
				r.errorf("", "%s: @rowFeatureOperations(%s) adds the MCP tool %q, for %s's %s, and %s's @mcp takes that handle; rename that handle (D11)", p.set.Name, typeName, handle, typeName, op.Name, other)
			}
			handles[handle] = owner
		}
	}
}

// checkRowFeatureRoutes refuses an operation the expansion adds whose
// method another operation of the API shares and whose path that
// operation's matches, as checkGraphRoutes does.
func checkRowFeatureRoutes(schema *ir.Schema, planned []plannedRowSet, r *Result) {
	type route struct {
		owner, method, path string
	}
	var routes []route
	for _, set := range schema.OperationSets {
		if set == nil || set.IsRowFeatureOperations() {
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
				r.errorf("", "%s: the operation %s it adds is routed at %s %s, which %s's %s %s also matches: move that route, or leave %s out of the set's operations (#723)", p.set.Name, op.Name, op.HTTPMethod, path, other.owner, other.method, other.path, op.Name)
			}
			routes = append(routes, route{owner: owner, method: op.HTTPMethod, path: path})
		}
	}
}
