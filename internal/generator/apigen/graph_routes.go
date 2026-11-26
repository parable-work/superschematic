package apigen

import (
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/graphroutes"
	ir "github.com/parable-work/superschematic/ir"
)

// A version graph's operations (D138), the ones the loader adds to a
// @graphOperations set, are the build's to serve: the Go server's router
// mounts each at its route behind the steps any route runs, from the
// set's clause, on the handler factory of any route, whose call is a
// method of graphs.go rather than of the implementation. That method reads
// the root and the ref or the commit the request names through the ORM of
// the API's database, holds an operation that writes a change set to the
// set's row rule, runs the operation through the graph's generated facade
// (orm.<Graph>Graph) as the request's caller, and answers what it returns
// as the API's types hold it; the runtime's graphroutes package holds what
// every graph shares. The implementation the project writes has no method
// for them. The OpenAPI document, the SDKs, the permission catalog and the
// tool documents read them as any other operation, so they stay in
// APIOutput.Endpoints, and ImplementedOutput moves them to
// APIOutput.GraphEndpoints for the Go server's writers, with Graphs, what
// graphs.go is written from.

// GraphsInfo is what graphs.go is written from: each @graphOperations set
// of the API.
type GraphsInfo struct {
	// Database is the API's database, which declares each graph, and
	// DBTypes the name graphs.go imports its Go types module as.
	Database string
	DBTypes  string
	// Sets are the sets, in the order their first operation is routed.
	Sets []*GraphSetInfo
	// Lifts reports whether a set's row rule has an override permission.
	Lifts bool
}

// GraphSetInfo is one @graphOperations set as graphs.go serves it.
type GraphSetInfo struct {
	// Set is the set's class, Graph the graph's name and Root its root
	// type.
	Set   string
	Graph string
	Root  string
	// RootKey is the root's key field, RootKeyGo its member of the ORM's
	// filter and fields, and RootKeyParam the path parameter that holds it.
	RootKey      string
	RootKeyGo    string
	RootKeyParam string
	// Var is the runtime Set graphs.go declares, Routes the type that
	// serves the set's operations, Member the field of Graphs that holds
	// the graph's facade, and Facade the facade's type in the ORM.
	Var    string
	Routes string
	Member string
	Facade string
	// RootRepository, RefRepository and CommitRepository are the ORM's
	// getters of the root's and the graph's ref and commit tables.
	RootRepository   string
	RefRepository    string
	CommitRepository string
	// SnapshotFilter is the member of the commit table's filter that keeps
	// a commit with a snapshot entry.
	SnapshotFilter string
	// By is the set's row rule, ByMembers its members of the ref table's
	// filter, and Override the permission that lifts it.
	By        []string
	ByMembers []string
	Override  string
	// Kinds are the graph's member kinds, as its routes serve them.
	Kinds []graphroutes.Member
	// Endpoints are the set's operations, in route order.
	Endpoints []EndpointInfo
}

// graphInputs are the runtime's types of each operation's body.
var graphInputs = map[string]string{
	ir.GraphOpBranch:  "BranchInput",
	ir.GraphOpSave:    "SaveInput",
	ir.GraphOpArrange: "ArrangeInput",
	ir.GraphOpCommit:  "CommitInput",
	ir.GraphOpSeal:    "VersionInput",
	ir.GraphOpMerge:   "MergeInput",
	ir.GraphOpRebase:  "RebaseInput",
	ir.GraphOpRevert:  "RevertInput",
	ir.GraphOpDiscard: "VersionInput",
	ir.GraphOpRelease: "ReleaseInput",
}

// InputOf is the runtime's type of the body of op.
func (s *GraphSetInfo) InputOf(op string) string {
	return graphInputs[op]
}

// HasGraphEndpoints reports whether the Go server serves a graph
// operation (D138): one of its GraphEndpoints in the view
// ImplementedOutput returns, or of its Endpoints that it would move there.
// Its Config then takes the ORM of its database and the graphs' facades,
// and the module holds graphs.go.
func (o *APIOutput) HasGraphEndpoints() bool {
	return len(o.GraphEndpoints) > 0 || o.HasGraphOperations()
}

// graphDatabase is the database the endpoints' graph operations run on,
// the API's database, or "" when none is one.
func graphDatabase(endpoints []EndpointInfo) string {
	for _, ep := range endpoints {
		if op := ep.GraphOperation(); op != nil && op.Database != "" {
			return op.Database
		}
	}
	return ""
}

// planGraphs models graphs.go for the graph operations among endpoints,
// read from the API's database, which the generator was given. IR the
// loader did not fill, and a graph the Go server cannot serve, are errors
// that name the operation.
func planGraphs(output *APIOutput, endpoints []EndpointInfo) (*GraphsInfo, map[*ir.FieldDef]*GraphSetInfo, error) {
	dbName := graphDatabase(endpoints)
	if dbName == "" {
		return nil, nil, nil
	}
	db := output.graphSchemas[dbName]
	if db == nil {
		return nil, nil, fmt.Errorf("apigen: %s serves the operations of a version graph of %s, whose schema the generator was not given (Options.Dependencies), so the Go server cannot write their handlers (D138)", output.SchemaName, dbName)
	}
	if output.Deps.Database != dbName || output.Deps.ORMModule == "" {
		return nil, nil, fmt.Errorf("apigen: %s serves the operations of a version graph of %s, and the Go server's Deps hold the ORM of %q (D138)", output.SchemaName, dbName, output.Deps.Database)
	}
	alias := output.Deps.DatabaseTypesAlias
	if alias == "" || output.Deps.DatabaseTypes == "" {
		return nil, nil, fmt.Errorf("apigen: %s serves the operations of a version graph of %s, and its Deps name no Go types module of it (D138)", output.SchemaName, dbName)
	}
	info := &GraphsInfo{Database: dbName, DBTypes: alias}
	bySet := map[*ir.FieldDef]*GraphSetInfo{}
	byName := map[string]*GraphSetInfo{}
	for _, ep := range endpoints {
		op := ep.GraphOperation()
		if op == nil {
			continue
		}
		owner := ep.Namespace + "." + ep.Name
		set := graphSetOf(output.graphOwner, ep.Operation)
		if set == nil {
			return nil, nil, fmt.Errorf("apigen: graph operation %s belongs to no @graphOperations set of %s (D138)", owner, output.SchemaName)
		}
		owner = set.Name + "." + ep.Name
		if !slices.Contains(ir.GraphOperationNames, op.Operation) || op.Graph == "" || op.Type == "" || op.Key == "" || op.Database != dbName {
			return nil, nil, fmt.Errorf("apigen: %s is a graph operation (D138) whose marker is not whole (operation %q, graph %q, root %q, key %q, database %q); the loader fills it, so load the schema before generating", owner, op.Operation, op.Graph, op.Type, op.Key, op.Database)
		}
		served := byName[set.Name]
		if served == nil {
			var err error
			if served, err = graphSetInfo(db, set, op, owner); err != nil {
				return nil, nil, err
			}
			byName[set.Name] = served
			info.Sets = append(info.Sets, served)
			info.Lifts = info.Lifts || served.Override != ""
		}
		if !slices.ContainsFunc(ep.PathParams, func(p Param) bool { return p.Name == op.Key && p.IsUUID }) {
			return nil, nil, fmt.Errorf("apigen: graph operation %s is routed at %s %s, which has no UUID path parameter %s, the key of %s (D138)", owner, ep.Method, ep.Path, op.Key, op.Type)
		}
		ep.GraphRoutes = served.Routes
		ep.GraphMember = served.Member
		served.Endpoints = append(served.Endpoints, ep)
		bySet[ep.Operation] = served
	}
	return info, bySet, nil
}

// graphSetInfo models set, whose operation op names its graph, from db.
func graphSetInfo(db *ir.Schema, set *ir.OperationSet, op *ir.GraphOperation, owner string) (*GraphSetInfo, error) {
	graph, err := graphroutes.Graph(db, op.Type)
	if err != nil {
		return nil, fmt.Errorf("apigen: %s: %w", owner, err)
	}
	key := graphroutes.KeyField(graph.Root)
	if key == nil || key.Name != op.Key {
		return nil, fmt.Errorf("apigen: %s: %s is keyed by no field %s (D138)", owner, op.Type, op.Key)
	}
	if key.TypeRef.Name != "Identity.UUID" || key.TypeRef.IsArray || key.TypeRef.IsMap {
		return nil, fmt.Errorf("apigen: %s serves the operations of %s's version graph, whose key %s is a %s, and the Go facade of a version graph addresses a root by a UUID (D138)", owner, op.Type, key.Name, key.TypeRef.Name)
	}
	info := &GraphSetInfo{
		Set:              set.Name,
		Graph:            op.Graph,
		Root:             op.Type,
		RootKey:          key.Name,
		RootKeyGo:        codegen.ToPascalCase(key.Name),
		RootKeyParam:     op.Key,
		Var:              lowerFirst(set.Name) + "Graph",
		Routes:           lowerFirst(set.Name) + "Routes",
		Member:           op.Graph,
		Facade:           op.Graph + "Graph",
		RootRepository:   "Get" + op.Type + "Repository",
		RefRepository:    "Get" + op.Graph + "RefRepository",
		CommitRepository: "Get" + op.Graph + "CommitRepository",
		SnapshotFilter:   "ReferencedBy" + op.Graph + "SnapshotEntryCommit",
		Kinds:            graphroutes.Members(db, graph),
	}
	if cfg := set.GraphOperations; cfg != nil {
		info.By = slices.Clone(cfg.By)
		info.Override = cfg.OverridePermission
	}
	ref := db.Types[op.Graph+"Ref"]
	if ref == nil {
		return nil, fmt.Errorf("apigen: %s: %s declares no %sRef, the ref table of its %s graph; the loader adds it (D17)", owner, db.Name, op.Graph, op.Graph)
	}
	for _, name := range info.By {
		var field *ir.FieldDef
		for _, fd := range ref.Fields {
			if fd.Name == name {
				field = fd
			}
		}
		if field == nil {
			return nil, fmt.Errorf("apigen: %s: its row rule names %s, which is no member of %sRef (D138)", owner, name, op.Graph)
		}
		member := codegen.ToPascalCase(name)
		if graphroutes.RelationTarget(db, field) != nil {
			member += "ID"
		}
		info.ByMembers = append(info.ByMembers, member)
	}
	return info, nil
}

// graphSetOf is the @graphOperations set of schema that holds op, or nil.
func graphSetOf(schema *ir.Schema, op *ir.FieldDef) *ir.OperationSet {
	if schema == nil || op == nil {
		return nil
	}
	for _, set := range schema.OperationSets {
		if set != nil && set.IsGraphOperations() && slices.Contains(set.Operations, op) {
			return set
		}
	}
	return nil
}

// lowerFirst is name with its first letter lower-cased.
func lowerFirst(name string) string {
	if name == "" {
		return ""
	}
	return strings.ToLower(name[:1]) + name[1:]
}
