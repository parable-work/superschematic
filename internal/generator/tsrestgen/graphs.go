package tsrestgen

import (
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/graphroutes"
	ir "github.com/parable-work/superschematic/ir"
)

// VersionGraphEntry is the entry of a database's types package that
// exports each version graph's facade, as tsgen writes it.
const VersionGraphEntry = "/versiongraph"

// GraphRoute is a graph operation (D138) as the router mounts it: the
// operation of a version graph of the API's database, which the loader
// added for a @graphOperations set, run by the runtime's handler of the
// operation over RouterOptions.orm and the graph's facade in
// RouterOptions.graphs.
type GraphRoute struct {
	// Set is the @graphOperations set that serves the operation.
	Set string
	// Database, Graph, Type and Operation are the loader's marker: the
	// API's database, the graph's name, its root and the operation.
	Database  string
	Graph     string
	Type      string
	Operation string
	// Member is the member of RouterOptions.graphs that holds the graph's
	// facade, and Var the constant of router.ts that holds the graph as
	// the handlers read it.
	Member string
	Var    string
	// By is the row rule of an operation that writes a change set, and
	// Override the permission whose holder it does not hold back.
	By       []string
	Override string
}

// Handler is the runtime's handler of the operation: graphBranch,
// graphSave, graphEntityHistory.
func (r *GraphRoute) Handler() string {
	return "graph" + codegen.ToPascalCase(r.Operation)
}

// Literal is the operation as router.ts's table of them holds it, a
// GraphOperation: { graph: recipeGraph, operation: 'save', by:
// ['createdBy'], overridePermission: 'recipes.admin' }.
func (r *GraphRoute) Literal() string {
	fields := []string{"graph: " + r.Var, "operation: " + tsString(r.Operation)}
	if len(r.By) > 0 {
		fields = append(fields, "by: "+tsStringList(r.By))
		if r.Override != "" {
			fields = append(fields, "overridePermission: "+tsString(r.Override))
		}
	}
	return "{ " + strings.Join(fields, ", ") + " }"
}

// GraphSetInfo is a version graph a @graphOperations set of the API
// serves (D138), as router.ts declares it for the runtime's handlers and
// takes its facade.
type GraphSetInfo struct {
	// Set is the @graphOperations set, Database the API's database, Graph
	// the graph's name and Root its root type.
	Set      string
	Database string
	Graph    string
	Root     string
	// Var is the constant of router.ts that holds the graph (recipeGraph),
	// and Member the member of RouterOptions.graphs that holds its facade
	// (recipe).
	Var    string
	Member string
	// Facade is the facade's class in the database's types package
	// (RecipeGraph).
	Facade string
	// Repository, Refs and Commits are the members of the repositories
	// that hold the root's, the graph's ref and the graph's commit tables.
	Repository string
	Refs       string
	Commits    string
	// Key is the root's key field.
	Key string
	// Kinds are the graph's member kinds.
	Kinds []graphroutes.Member
}

// Literal is the graph as router.ts holds it, a GraphSet.
func (g GraphSetInfo) Literal() string {
	var b strings.Builder
	fmt.Fprintf(&b, "{\n  graph: %s,\n  root: %s,\n  repository: %s,\n  key: %s,\n  refs: %s,\n  commits: %s,\n  kinds: [\n",
		tsString(g.Graph), tsString(g.Root), tsString(g.Repository), tsString(g.Key), tsString(g.Refs), tsString(g.Commits))
	for _, kind := range g.Kinds {
		fmt.Fprintf(&b, "    { kind: %s, member: %s, fields: %s", tsString(kind.Kind), tsString(kind.Tree), tsStringList(kind.Fields))
		if len(kind.Relations) > 0 {
			relations := make([]string, 0, len(kind.Relations))
			for _, relation := range kind.Relations {
				relations = append(relations, fmt.Sprintf("{ member: %s, field: %s, key: %s }", tsString(relation.Member), tsString(relation.Field), tsString(relation.Key)))
			}
			fmt.Fprintf(&b, ", relations: [%s]", strings.Join(relations, ", "))
		}
		b.WriteString(" },\n")
	}
	b.WriteString("  ],\n}")
	return b.String()
}

// GraphRoutes are the graph operations the router mounts (D138), in the
// order of Endpoints.
func (o *APIOutput) GraphRoutes() []*GraphRoute {
	var out []*GraphRoute
	for _, endpoint := range o.Endpoints {
		if endpoint.Graph != nil {
			out = append(out, endpoint.Graph)
		}
	}
	return out
}

// HasGraphs reports whether the API serves a version graph's operations
// (D138), which the router runs over RouterOptions.orm and
// RouterOptions.graphs.
func (o *APIOutput) HasGraphs() bool {
	return len(o.GraphSets) > 0
}

// GraphHandlers are the runtime's handlers the router mounts its graph
// operations with, each once, sorted.
func (o *APIOutput) GraphHandlers() []string {
	handlers := map[string]struct{}{}
	for _, route := range o.GraphRoutes() {
		handlers[route.Handler()] = struct{}{}
	}
	return sortedKeys(handlers)
}

// GraphFacades are the facade classes router.ts imports from the
// database's types package, sorted.
func (o *APIOutput) GraphFacades() []string {
	facades := map[string]struct{}{}
	for _, set := range o.GraphSets {
		facades[set.Facade] = struct{}{}
	}
	return sortedKeys(facades)
}

// GraphFacadeModule is the module router.ts imports the facades' types
// from: the database's types package's ./versiongraph entry.
func (o *APIOutput) GraphFacadeModule() string {
	return o.Deps.DatabaseTypesPackage + VersionGraphEntry
}

// checkGraphDeps refuses deps for an API that serves a version graph's
// operations (D138) when they hold no repositories, as checkBehaviorDeps
// does for a behavior operation: the router reads the root and the
// graph's refs through RouterOptions.orm, and takes each graph's facade
// from the same types package.
func (o *APIOutput) checkGraphDeps(deps DepsInfo) error {
	if len(o.GraphSets) == 0 || depsORM(deps) {
		return nil
	}
	set := o.GraphSets[0]
	why := fmt.Sprintf("enable outputs.types.typescript in %s's config", set.Database)
	if deps.Dialect == SQLiteDialect {
		why = fmt.Sprintf("%s lists sqlite first, and the repositories and the graph's facade run on Postgres alone (D138)", set.Database)
	}
	return fmt.Errorf("tsrestgen: %s serves the operations of %s's version graph (@graphOperations on %s), which run through Deps.orm and the graph's facade, both of which %s's TypeScript types export for Postgres; %s", o.SchemaName, set.Root, set.Set, set.Database, why)
}

// graphEndpoint is a graph operation (D138) as the router mounts it: its
// route, its parameters, its input and its result, decoded as an
// implemented endpoint's, and the graph it runs on. Its types are
// rendered by a builder of their own, so they add no import to
// interfaces.ts, which has no method for it, and none but its input's
// validators to router.ts, whose handler hands the runtime the decoded
// request whole. The loader fills the marker; IR that did not go through
// it is refused here, naming the operation.
func (b *builder) graphEndpoint(ep apigen.EndpointInfo, op *ir.GraphOperation, output *APIOutput) (EndpointInfo, error) {
	own := newBuilder(b.schema, b.opts)
	endpoint, err := own.endpoint(ep)
	if err != nil {
		return endpoint, err
	}
	owner := behaviorSet(b.schema, ep.Operation)
	if !slices.Contains(ir.GraphOperationNames, op.Operation) || op.Type == "" || op.Graph == "" || op.Key == "" || op.Database == "" {
		return endpoint, fmt.Errorf("tsrestgen: %s.%s is a graph operation (D138) whose marker is not whole (operation %q, graph %q, root %q, key %q, database %q); the loader fills it, so load the schema before generating", owner, ep.Operation.Name, op.Operation, op.Graph, op.Type, op.Key, op.Database)
	}
	if !slices.ContainsFunc(endpoint.PathParams, func(p ParamInfo) bool { return p.Name == op.Key }) {
		return endpoint, fmt.Errorf("tsrestgen: graph operation %s.%s is routed at %s %s, which has no path parameter %s, the key of %s", owner, ep.Operation.Name, endpoint.Method, endpoint.Path, op.Key, op.Type)
	}
	set, err := b.graphSet(owner, op, output)
	if err != nil {
		return endpoint, err
	}
	endpoint.Graph = &GraphRoute{
		Set:       owner,
		Database:  op.Database,
		Graph:     op.Graph,
		Type:      op.Type,
		Operation: op.Operation,
		Member:    set.Member,
		Var:       set.Var,
		By:        op.By,
		Override:  op.OverridePermission,
	}
	for pkg, symbols := range own.validatorImports {
		for symbol := range symbols {
			b.addImport(b.validatorImports, pkg, symbol)
		}
	}
	return endpoint, nil
}

// graphSet is the graph the marker op names, as router.ts declares it:
// the one output already holds, or a new one read from the API's
// database.
func (b *builder) graphSet(owner string, op *ir.GraphOperation, output *APIOutput) (*GraphSetInfo, error) {
	for i := range output.GraphSets {
		if output.GraphSets[i].Graph == op.Graph {
			return &output.GraphSets[i], nil
		}
	}
	db := databaseOf(b.schema, b.opts)
	if db == nil || db.Name != op.Database {
		return nil, fmt.Errorf("tsrestgen: %s serves the operations of %s's version graph, of %s, and the API's database is not loaded: Options.Dependencies holds no %s", owner, op.Type, op.Database, op.Database)
	}
	graph, err := graphroutes.Graph(db, op.Type)
	if err != nil {
		return nil, fmt.Errorf("tsrestgen: %s: %w", owner, err)
	}
	member := lowerFirst(op.Graph)
	set := GraphSetInfo{
		Set:        owner,
		Database:   op.Database,
		Graph:      op.Graph,
		Root:       op.Type,
		Var:        member + "Graph",
		Member:     member,
		Facade:     op.Graph + "Graph",
		Repository: lowerFirst(op.Type),
		Refs:       lowerFirst(op.Graph + "Ref"),
		Commits:    lowerFirst(op.Graph + "Commit"),
		Key:        op.Key,
		Kinds:      graphroutes.Members(db, graph),
	}
	for _, name := range []string{set.Var, set.Member, set.Facade, set.Repository, set.Refs, set.Commits} {
		if !identifierPattern.MatchString(name) {
			return nil, fmt.Errorf("tsrestgen: %s serves the operations of %s's version graph, and %q is no identifier the router can name it by", owner, op.Type, name)
		}
	}
	output.GraphSets = append(output.GraphSets, set)
	return &output.GraphSets[len(output.GraphSets)-1], nil
}
