package rustrestgen

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/graphroutes"
	"github.com/parable-work/superschematic/internal/generator/rustutil"
	ir "github.com/parable-work/superschematic/ir"
)

// The graph operations of a Rust API (D138). An operation the loader adds
// for a @graphOperations set has no method in its namespace's trait: the
// router's handler hands what it decoded to src/graphs.rs, which reads
// the root and the ref or the commit the request names through the
// database's repositories, holds an operation that writes a change set to
// the set's row rule, runs the operation through the graph's facade in the
// database's Rust types as the request's caller, and answers what it
// returns as the API's types hold it. What every graph shares is the HTTP
// runtime's graph_routes module.

// GraphsFile is the file of an API crate that serves its graph
// operations.
const GraphsFile = "graphs.rs"

// GraphInfo is a graph operation as the crate serves it.
type GraphInfo struct {
	// Set is the set's class, and Operation the graph's operation.
	Set       string
	Operation string
	// Database, Graph and Type are the loader's marker: the API's
	// database, the graph's name and its root.
	Database string
	Graph    string
	Type     string
	// Owned reports whether the operation writes a change set, which the
	// set's row rule holds.
	Owned bool
	// The Args' fields of the path's and the query's parameters.
	RootField   string
	RefField    string
	CommitField string
	KindField   string
	KeyField    string
	FromField   string
	ToField     string
	// SetInfo is the set as src/graphs.rs serves it.
	SetInfo *GraphSetInfo
}

// GraphSetInfo is one @graphOperations set as src/graphs.rs serves it.
type GraphSetInfo struct {
	// Set is the set's class, Graph the graph's name and Root its root
	// type.
	Set   string
	Graph string
	Root  string
	// Fn is the function of src/graphs.rs that reads the set's literal,
	// Field the field of Graphs that holds the graph's facade, and Facade
	// the facade's type in the database's Rust types.
	Fn     string
	Field  string
	Facade string
	// Literal is the set as the runtime's GraphSet reads it, in JSON, as a
	// Rust raw string literal.
	Literal string
	// RootKeyField is the root's key's field of its filter.
	RootKeyField string
	// RootRepository, RefRepository and CommitRepository are the
	// repositories' methods of the root's, the ref and the commit tables.
	RootRepository   string
	RefRepository    string
	CommitRepository string
	// By are the ref filter's fields of the set's row rule, and Override
	// the permission that lifts it.
	By       []string
	Override string
	// Kinds are each member kind's descriptor name, its tree member in
	// the API's types and its field of the facade's tree.
	Kinds []GraphKindInfo
}

// GraphKindInfo is one member kind of a graph as src/graphs.rs reads a
// tree of it.
type GraphKindInfo struct {
	Kind   string
	Member string
	Field  string
}

// graphLiteral is the runtime's GraphSet, the members the TypeScript
// router's literal has.
type graphLiteral struct {
	Graph string             `json:"graph"`
	Root  string             `json:"root"`
	Kinds []graphLiteralKind `json:"kinds"`
}

type graphLiteralKind struct {
	Kind      string                 `json:"kind"`
	Member    string                 `json:"member"`
	Fields    []string               `json:"fields"`
	Relations []graphroutes.Relation `json:"relations,omitempty"`
}

// graphInfo is the graph operation endpoint runs, nil for every other.
// info must hold its arguments (builder.params). IR the loader did not
// fill, and a graph the Rust server cannot serve, are errors that name the
// operation.
func (b *builder) graphInfo(endpoint apigen.EndpointInfo, info EndpointInfo, sets map[string]*GraphSetInfo) (*GraphInfo, error) {
	op := endpoint.GraphOperation()
	if op == nil {
		return nil, nil
	}
	owner := info.Namespace + "." + info.Name
	if !slices.Contains(ir.GraphOperationNames, op.Operation) || op.Graph == "" || op.Type == "" || op.Key == "" || op.Database == "" {
		return nil, fmt.Errorf("rustrestgen: %s is a graph operation (D138) whose marker is not whole (operation %q, graph %q, root %q, key %q, database %q); the loader fills it, so load the schema before generating", owner, op.Operation, op.Graph, op.Type, op.Key, op.Database)
	}
	set := graphSetOf(b.schema, endpoint.Operation)
	if set == nil {
		return nil, fmt.Errorf("rustrestgen: graph operation %s belongs to no @graphOperations set (D138)", owner)
	}
	g := &GraphInfo{Set: set.Name, Operation: op.Operation, Database: op.Database, Graph: op.Graph, Type: op.Type, Owned: len(op.By) > 0}
	fields := map[string]*string{op.Key: &g.RootField, "ref": &g.RefField, "commit": &g.CommitField, "kind": &g.KindField, "key": &g.KeyField, "from": &g.FromField, "to": &g.ToField}
	for _, arg := range append(slices.Clone(info.PathArgs), info.QueryArgs...) {
		if field, ok := fields[arg.Name]; ok {
			*field = arg.Field
		}
	}
	if g.RootField == "" {
		return nil, fmt.Errorf("rustrestgen: graph operation %s is routed at %s %s, which has no path parameter %s, the key of %s (D138)", owner, info.Method, info.Path, op.Key, op.Type)
	}
	if served := sets[set.Name]; served != nil {
		g.SetInfo = served
		return g, nil
	}
	db := b.deps[op.Database]
	if db == nil {
		return nil, fmt.Errorf("rustrestgen: %s serves the operations of %s's version graph, of %s, whose schema the generator was not given (Options.Dependencies) (D138)", owner, op.Type, op.Database)
	}
	served, err := graphSetInfo(db, set, op, owner)
	if err != nil {
		return nil, err
	}
	sets[set.Name] = served
	g.SetInfo = served
	return g, nil
}

// graphSetInfo models set, whose operation op names its graph, from db.
func graphSetInfo(db *ir.Schema, set *ir.OperationSet, op *ir.GraphOperation, owner string) (*GraphSetInfo, error) {
	graph, err := graphroutes.Graph(db, op.Type)
	if err != nil {
		return nil, fmt.Errorf("rustrestgen: %s: %w", owner, err)
	}
	key := graphroutes.KeyField(graph.Root)
	if key == nil || key.Name != op.Key {
		return nil, fmt.Errorf("rustrestgen: %s: %s is keyed by no field %s (D138)", owner, op.Type, op.Key)
	}
	if key.TypeRef.Name != "Identity.UUID" || key.TypeRef.IsArray || key.TypeRef.IsMap {
		return nil, fmt.Errorf("rustrestgen: %s serves the operations of %s's version graph, whose key %s is a %s, and the Rust facade of a version graph addresses a root by a UUID (D138)", owner, op.Type, key.Name, key.TypeRef.Name)
	}
	members := graphroutes.Members(db, graph)
	literal := graphLiteral{Graph: op.Graph, Root: op.Type}
	info := &GraphSetInfo{
		Set:              set.Name,
		Graph:            op.Graph,
		Root:             op.Type,
		Fn:               rustutil.ToSnakeCase(set.Name) + "_graph",
		Field:            rustutil.EscapeKeyword(rustutil.ToSnakeCase(op.Graph)),
		Facade:           op.Graph + "Graph",
		RootKeyField:     rustutil.EscapeKeyword(rustutil.ToSnakeCase(key.Name)),
		RootRepository:   rustutil.EscapeKeyword(rustutil.ToSnakeCase(op.Type)),
		RefRepository:    rustutil.ToSnakeCase(op.Graph + "Ref"),
		CommitRepository: rustutil.ToSnakeCase(op.Graph + "Commit"),
	}
	for _, member := range members {
		literal.Kinds = append(literal.Kinds, graphLiteralKind{Kind: member.Kind, Member: member.Tree, Fields: member.Fields, Relations: member.Relations})
		info.Kinds = append(info.Kinds, GraphKindInfo{Kind: member.Kind, Member: member.Tree, Field: rustutil.EscapeKeyword(codegen.ToSnakeCase(member.Kind))})
	}
	data, err := json.Marshal(literal)
	if err != nil {
		return nil, err
	}
	info.Literal = rustutil.RawString(string(data))
	if cfg := set.GraphOperations; cfg != nil {
		info.Override = cfg.OverridePermission
		ref := db.Types[op.Graph+"Ref"]
		for _, name := range cfg.By {
			var field *ir.FieldDef
			if ref != nil {
				for _, fd := range ref.Fields {
					if fd.Name == name {
						field = fd
					}
				}
			}
			if field == nil {
				return nil, fmt.Errorf("rustrestgen: %s: its row rule names %s, which is no member of %sRef (D138)", owner, name, op.Graph)
			}
			column := name
			if graphroutes.RelationTarget(db, field) != nil {
				column = graphroutes.RelationMember(name)
			}
			info.By = append(info.By, rustutil.EscapeKeyword(rustutil.ToSnakeCase(column)))
		}
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

// HasGraphOperations reports whether the router serves a version graph's
// operations (D138): the crate then has src/graphs.rs, Implementations
// holds the Graphs its handlers run them on, and the crate depends on the
// database's Rust types for their facades and repositories.
func (o *APIOutput) HasGraphOperations() bool {
	return len(o.GraphEndpoints()) > 0
}

// GraphEndpoints are the graph operations the router serves, in route
// order.
func (o *APIOutput) GraphEndpoints() []EndpointInfo {
	var out []EndpointInfo
	for _, endpoint := range o.Endpoints {
		if endpoint.Graph != nil {
			out = append(out, endpoint)
		}
	}
	return out
}

// GraphSets are the @graphOperations sets the router serves, in the order
// their first operation is routed.
func (o *APIOutput) GraphSets() []*GraphSetInfo {
	var out []*GraphSetInfo
	for _, endpoint := range o.GraphEndpoints() {
		if !slices.Contains(out, endpoint.Graph.SetInfo) {
			out = append(out, endpoint.Graph.SetInfo)
		}
	}
	return out
}

// GraphFacades are the sets' facades, each once: the field of Graphs that
// holds it and its type.
func (o *APIOutput) GraphFacades() []*GraphSetInfo {
	var out []*GraphSetInfo
	seen := map[string]bool{}
	for _, set := range o.GraphSets() {
		if !seen[set.Field] {
			seen[set.Field] = true
			out = append(out, set)
		}
	}
	return out
}

// GraphsLift reports whether a set's row rule has an override permission.
func (o *APIOutput) GraphsLift() bool {
	return slices.ContainsFunc(o.GraphSets(), func(set *GraphSetInfo) bool { return set.Override != "" })
}

// GraphDatabase is the DB service the graph operations run on, the API's
// database; empty without one.
func (o *APIOutput) GraphDatabase() string {
	for _, endpoint := range o.GraphEndpoints() {
		return endpoint.Graph.Database
	}
	return ""
}

// GraphArgs are the Args structs of the graph operations, sorted, as
// src/graphs.rs imports them.
func (o *APIOutput) GraphArgs() []string {
	var names []string
	for _, endpoint := range o.GraphEndpoints() {
		if endpoint.HasArgs() {
			names = append(names, endpoint.ArgsName)
		}
	}
	slices.Sort(names)
	return names
}

// GraphsInterfacesUse is src/graphs.rs's import of the graph operations'
// Args structs.
func (o *APIOutput) GraphsInterfacesUse() string {
	return rustutil.UseLine("crate::interfaces", o.GraphArgs())
}

// checkGraphDeps refuses graph operations the crate cannot serve: an API
// with no database of its own in Deps, one whose operations are of another
// DB service, a database on a SQLite file, and a database whose Rust
// types are not generated.
func (o *APIOutput) checkGraphDeps() error {
	if !o.HasGraphOperations() {
		return nil
	}
	db := o.GraphDatabase()
	switch {
	case o.Deps == nil || o.Deps.Database == "":
		return fmt.Errorf("rustrestgen: %s serves the operations of a version graph of %s (D138), and its Deps hold no database; set its authDb to %s, or depend on it", o.SchemaName, db, db)
	case o.Deps.Database != db:
		return fmt.Errorf("rustrestgen: %s serves the operations of a version graph of %s (D138), and its Deps hold %s's pool", o.SchemaName, db, o.Deps.Database)
	case o.Deps.SQLite:
		return fmt.Errorf("rustrestgen: %s serves the operations of a version graph of %s (D138), which lists sqlite first, and the Rust server runs a graph's facade on Postgres alone so far", o.SchemaName, db)
	case o.Deps.DatabaseTypesCrate == "":
		return fmt.Errorf("rustrestgen: %s serves the operations of a version graph of %s (D138), whose facade is in its Rust types, and %s generates none; enable outputs.types.rust in %s's config", o.SchemaName, db, db, db)
	}
	return nil
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

// graphInput is the runtime's type of the body of the graph operation op,
// the template function graphInput.
func graphInput(op string) string {
	return graphInputs[op]
}
