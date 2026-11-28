// Package graphops adds a version graph's operations to an API
// (D138, #592). An API marks a class with no members
// @graphOperations(Root, { ... }), from @superschematic/api, naming a
// @versionGraph table of its database:
//
//	@graphOperations(Recipe, { permissions: { release: ["recipes.release"] }, by: ["createdBy"] })
//	@requirePermission(["recipes.write"])
//	export abstract class RecipeOperations {}
//
// Once the set passes its checks (verify.CheckGraphOperations), and before
// the rest of the verification pass, the loader fills it here with one
// operation for each operation of the graph the set serves, in the order
// of ir.GraphOperationNames, each routed under the root's plural and its
// key (POST recipes/{id}/refs, named branch, PUT
// recipes/{id}/refs/{ref}/edits, named save), taking the root's key, the
// ref or the commit its path names and an input of the operation's body
// and returning its result, with generated @docs and an @mcp handle
// (recipe_branch), or a hidden @mcp when the set's @requireService admits
// only calling services (D37), the set's clause as its own, with the
// permissions the set names for the operation in place of the class's, and
// the marker ir.FieldDef.GraphOperation. The input and result types, and
// the enums they name, are the graph's: a ref, a commit and a release as
// the generated facade returns them, and a row and an edit of each member
// typed from the member's content fields, the ones the facade's typed
// upsert writes. Each is named from the graph's name (RecipeGraphRef,
// RecipeGraphStepRow). So every check the pass makes of an authored
// operation holds for these, and the OpenAPI document, the SDKs, the
// permission catalog and the tool documents read them as any other; only a
// server whose generator writes graph handlers routes them to one, and
// any other keeps each a method of the implementation's interface.
package graphops

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/graphdesc"
	"github.com/parable-work/superschematic/internal/generator/graphroutes"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// The scalars an added type names beside the graph's own.
const (
	int64Scalar    = "Generic.Int64"
	jsonScalar     = "Generic.JSON"
	dateTimeScalar = "Temporal.DateTime"
)

// The path parameters an operation's route names beside the root's key.
const (
	RefParam    = "ref"
	CommitParam = "commit"
	KindParam   = "kind"
	EntityParam = "key"
)

// PathParams are the path parameters the routes name beside the root's
// key, which the root's key may not be named as.
var PathParams = []string{RefParam, CommitParam, KindParam, EntityParam}

// TreeMembers are the members of a tree beside its kinds' rows, which no
// member's tree member may be named as.
var TreeMembers = []string{"contentHash", "findings"}

// Prefix is the name every type and enum the expansion adds for the graph
// named graph starts with (RecipeGraph).
func Prefix(graph string) string {
	return graph + "Graph"
}

// TypeName is the name of the type or enum the expansion adds for the
// graph named graph under suffix (RecipeGraphRef).
func TypeName(graph, suffix string) string {
	return Prefix(graph) + suffix
}

// RowType, RowInputType and RowEditsType name a member's row, the value an
// upsert writes of it and its edits (RecipeGraphStepRow,
// RecipeGraphStepRowInput, RecipeGraphStepRowEdits).
func RowType(graph, member string) string { return Prefix(graph) + member + "Row" }

// RowInputType is the name of the value an upsert of member writes.
func RowInputType(graph, member string) string { return RowType(graph, member) + "Input" }

// RowEditsType is the name of member's edits.
func RowEditsType(graph, member string) string { return RowType(graph, member) + "Edits" }

// Member is one member kind of a set's graph as the routes serve it, and
// Relation a field of one that holds a to-one relation's key
// (graphroutes, which the TypeScript server's router reads too).
type (
	Member   = graphroutes.Member
	Relation = graphroutes.Relation
)

// TreeMember is the member of a tree and of a graph's edits that holds
// the rows of the member type typeName, as the generated facade names it
// (step, lineItem).
func TreeMember(typeName string) string {
	return graphroutes.TreeMember(typeName)
}

// RootPath is the route every operation of the root's graph sits under,
// below the API's prefix: the root's plural and its key (recipes/{id}).
func RootPath(root, key string) string {
	return codegen.ToPlural(codegen.ToKebabCase(root)) + "/{" + key + "}"
}

// route is where one operation is served: its method and its path below
// RootPath.
type route struct {
	method, path string
}

// routes are the operations' routes, by operation.
var routes = map[string]route{
	ir.GraphOpBranch:        {"POST", "/refs"},
	ir.GraphOpSave:          {"PUT", "/refs/{" + RefParam + "}/edits"},
	ir.GraphOpArrange:       {"POST", "/refs/{" + RefParam + "}/arrange"},
	ir.GraphOpCommit:        {"POST", "/refs/{" + RefParam + "}/commit"},
	ir.GraphOpSeal:          {"POST", "/refs/{" + RefParam + "}/seal"},
	ir.GraphOpMerge:         {"POST", "/refs/{" + RefParam + "}/merge"},
	ir.GraphOpRebase:        {"POST", "/refs/{" + RefParam + "}/rebase"},
	ir.GraphOpRevert:        {"POST", "/refs/{" + RefParam + "}/revert"},
	ir.GraphOpDiscard:       {"POST", "/refs/{" + RefParam + "}/discard"},
	ir.GraphOpHistory:       {"GET", "/refs/{" + RefParam + "}/history"},
	ir.GraphOpCompose:       {"GET", "/refs/{" + RefParam + "}/tree"},
	ir.GraphOpRelease:       {"POST", "/release"},
	ir.GraphOpReleased:      {"GET", "/released"},
	ir.GraphOpMaterialize:   {"GET", "/commits/{" + CommitParam + "}"},
	ir.GraphOpDiff:          {"GET", "/diff"},
	ir.GraphOpEntityHistory: {"GET", "/entities/{" + KindParam + "}/{" + EntityParam + "}/history"},
}

// RestPath is the route of the graph operation op of root, below the
// API's prefix (recipes/{id}/refs/{ref}/edits).
func RestPath(root, key, op string) string {
	return RootPath(root, key) + routes[op].path
}

// Method is the HTTP method of the graph operation op.
func Method(op string) string {
	return routes[op].method
}

// Handle is the MCP handle of the graph's op (recipe_entity_history).
func Handle(graph, op string) string {
	return codegen.ToSnakeCase(graph) + "_" + codegen.ToSnakeCase(op)
}

// Capability is the @docs capability of the graph's op
// (recipe.graph.entity-history).
func Capability(graph, op string) string {
	return codegen.ToKebabCase(graph) + ".graph." + codegen.ToKebabCase(op)
}

// Title is the @docs title of the graph's op (Recipe graph: entity
// history).
func Title(graph, op string) string {
	return graph + " graph: " + strings.ReplaceAll(codegen.ToKebabCase(op), "-", " ")
}

// RequireServiceToolReason is the @mcp hidden reason of an operation the
// expansion adds to set, a set whose @requireService admits only calling
// services: no end user's agent can call its tool (D37).
func RequireServiceToolReason(set string) string {
	return "Only the services that @requireService on " + set + " admits call it (D37)."
}

// descriptions say what each operation does, as its comment, its @docs
// description and its tool's.
var descriptions = map[string]string{
	ir.GraphOpBranch:        "Creates a change set of the ref from names, or of the root's primary line without one, which it creates, named main, for a root that has none.",
	ir.GraphOpSave:          "Applies edits to a change set at version: each kind's upserts, then its deletes, then its unsets.",
	ir.GraphOpArrange:       "Puts one parent's children of a kind in the order keys gives, on a change set at version.",
	ir.GraphOpCommit:        "Commits a change set at version: a commit with a patch for each entity it changed.",
	ir.GraphOpSeal:          "Commits a change set at version when it has changes, and seals it: the ref then refuses writes.",
	ir.GraphOpMerge:         "Merges the change set's head into the ref into names, or into its parent, at that ref's version. With conflicts left after resolutions it answers them and writes nothing.",
	ir.GraphOpRebase:        "Moves a change set at version onto its parent's head. With conflicts left after resolutions it answers them and writes nothing.",
	ir.GraphOpRevert:        "Writes the rows that make a change set at version compose to the tree of toCommit, and commits them.",
	ir.GraphOpDiscard:       "Discards a ref at version, which frees its name.",
	ir.GraphOpHistory:       "Lists the commits a ref wrote, newest first.",
	ir.GraphOpCompose:       "Reads a ref's tree: its base commit's tree with the ref's own rows laid over it, with its content hash and the problems found in it.",
	ir.GraphOpRelease:       "Points the root's release at a tagged commit, at the pointer's version: 0 for the root's first release.",
	ir.GraphOpReleased:      "Reads the root's release pointer and the tree of the commit it names.",
	ir.GraphOpMaterialize:   "Reads a commit's tree.",
	ir.GraphOpDiff:          "Lists the entities the trees of two commits differ on.",
	ir.GraphOpEntityHistory: "Lists the commits of the root's primary line that changed one entity, newest first, each with what it did to it.",
}

// Reads reports whether the graph operation op writes nothing: its route
// is a GET.
func Reads(op string) bool {
	return routes[op].method == "GET"
}

// Owned reports whether the graph operation op writes a change set, which
// a set's row rule keeps to the ref's creator (D138).
func Owned(op string) bool {
	return slices.Contains(ir.GraphOperationsOwned, op)
}

// Permissions are the permissions the operation op of set needs: the list
// the set's permissions names for it, or the class's @requirePermission.
func Permissions(set *ir.OperationSet, op string) []string {
	if own, ok := set.GraphOperations.Permissions[op]; ok {
		return slices.Clone(own)
	}
	return slices.Clone(set.Permissions)
}

// Added is a type or an enum the expansion adds for one set: its kind
// ("type" or "enum"), its name, and what it is of the graph.
type Added struct {
	Kind string
	Name string
	For  string
}

// Plan is what the expansion adds for one @graphOperations set: its
// operations, and the types and enums they take and return, in the order
// they are first named.
type Plan struct {
	// Graph is the graph's name, and Root and Key its root type and the
	// root's key field.
	Graph string
	Root  string
	Key   string
	// Members are the graph's member kinds, in descriptor order.
	Members    []Member
	Operations []*ir.FieldDef
	Types      []*ir.TypeDef
	Enums      []*ir.EnumDef
	// Added lists every type and enum, in the order they are first named.
	Added []Added
	// Scalars are the scalar names the operations and types name.
	Scalars []string
}

// Offers reports whether the graph can run op at all: arrange places the
// rows of a kind whose order is a string key, which a graph with no such
// kind has none of.
func Offers(members []Member, op string) bool {
	if op != ir.GraphOpArrange {
		return true
	}
	return slices.ContainsFunc(members, func(m Member) bool { return m.Ordered })
}

// PlanSet works out what the expansion adds for set, a @graphOperations
// set whose root db, the API's database, declares. It assumes the root is
// one (verify.CheckGraphOperations): a @versionGraph table of db keyed by
// one field. It leaves out arrange for a graph none of whose kinds orders
// by a string key, unless the set lists it, which the checks refuse. It
// fails when the graph's descriptor cannot be read, or two of the names
// it adds are one.
func PlanSet(set *ir.OperationSet, db *ir.Schema, reg *registry.Registry) (*Plan, error) {
	cfg := set.GraphOperations
	g, err := graphroutes.Graph(db, cfg.Type)
	if err != nil {
		return nil, fmt.Errorf("graphops: %s: %w", set.Name, err)
	}
	key := graphroutes.KeyField(g.Root)
	if key == nil {
		return nil, fmt.Errorf("graphops: %s: %s has no @key field", set.Name, g.Root.Name)
	}
	p := &Plan{Graph: g.Name, Root: g.Root.Name, Key: key.Name, Members: graphroutes.Members(db, g)}
	b := &builder{plan: p, db: db, graph: g, named: map[string]string{}, id: key.TypeRef.Name, actor: key.TypeRef.Name}
	if ref := db.Types[g.Name+"Ref"]; ref != nil {
		// The ids of the graph's own rows and the actor of its writes, as
		// the graph's ref table holds them.
		for _, fd := range ref.Fields {
			switch fd.Name {
			case "id":
				b.id = fd.TypeRef.Name
			case "createdBy":
				b.actor = fd.TypeRef.Name
			}
		}
	}
	b.useScalar(key.TypeRef.Name)
	policy := reg.ToolInvocationPolicy()
	for _, op := range ir.GraphOperationNames {
		if !cfg.Serves(op) || (len(cfg.Operations) == 0 && !Offers(p.Members, op)) {
			continue
		}
		p.Operations = append(p.Operations, b.operationOf(set, key, op, policy))
	}
	if b.err != nil {
		return nil, fmt.Errorf("graphops: %s: %w", set.Name, b.err)
	}
	return p, nil
}

// operationOf is the operation the expansion adds for op, named by it and
// marked with it.
func (b *builder) operationOf(set *ir.OperationSet, key *ir.FieldDef, op string, policy registry.ToolInvocationPolicy) *ir.FieldDef {
	cfg := set.GraphOperations
	graph, root := b.plan.Graph, b.plan.Root
	permissions := Permissions(set, op)
	out := &ir.FieldDef{
		Name:       op,
		Comment:    descriptions[op],
		Required:   true,
		HTTPMethod: Method(op),
		RestPath:   RestPath(root, key.Name, op),
		Arguments: []*ir.ArgumentDef{{
			Name:     key.Name,
			Comment:  "The key of the " + root + " row whose graph the operation runs on.",
			TypeRef:  ir.TypeRef{Name: key.TypeRef.Name},
			Required: true,
		}},
		Auth:        set.Auth,
		Permissions: permissions,
		Public:      set.Public,
		Docs: &ir.OperationDocs{
			Title:         Title(graph, op),
			Description:   descriptions[op],
			Capability:    Capability(graph, op),
			Lifecycle:     ir.DocsLifecycleActive,
			Visibility:    ir.DocsVisibilityPublic,
			MappingStatus: ir.DocsMappingStatusMapped,
		},
		MCP: &ir.OperationMCP{
			Handle:     Handle(graph, op),
			Invocation: ir.MCPInvocation{Key: policy.Key, Value: policy.Default},
		},
		Origin: ir.OriginGraphOperations,
		GraphOperation: &ir.GraphOperation{
			Database:  b.db.Name,
			Graph:     graph,
			Type:      root,
			Key:       key.Name,
			Operation: op,
		},
	}
	if Owned(op) && len(cfg.By) > 0 {
		out.GraphOperation.By = slices.Clone(cfg.By)
		out.GraphOperation.OverridePermission = cfg.OverridePermission
	}
	if callers := ir.EffectiveServiceCallers(set, out); callers != nil && callers.Mode == ir.ServiceCallersRequire {
		// Only a calling service reaches the route, so no end user's agent
		// can call the tool: the tool documents leave it out, and the
		// record says why, as @mcp({ hidden: true, reason }) would (D37).
		out.MCP = &ir.OperationMCP{Hidden: true, HiddenReason: RequireServiceToolReason(set.Name)}
	}
	if Reads(op) {
		out.Docs.ReplayMode = ir.DocsReplayModeReadOnly
	}
	b.operation = op
	path := func(name, comment, typeName string) {
		b.useScalar(typeName)
		out.Arguments = append(out.Arguments, &ir.ArgumentDef{Name: name, Comment: comment, TypeRef: ir.TypeRef{Name: typeName}, Required: true})
	}
	query := func(name, comment, typeName string) {
		b.useScalar(typeName)
		out.Arguments = append(out.Arguments, &ir.ArgumentDef{Name: name, Comment: comment, TypeRef: ir.TypeRef{Name: typeName}, Required: true, IsQuery: true})
	}
	input := func(fields ...*ir.FieldDef) {
		name := TypeName(graph, codegen.ToPascalCase(op)+"Input")
		b.addType(name, fmt.Sprintf("The body of the %s graph's %s.", graph, op), ir.RoleAPIInput, fields...)
		out.Arguments = append(out.Arguments, &ir.ArgumentDef{Name: ir.GraphInputArgument, TypeRef: ir.TypeRef{Name: name}, Required: true})
	}
	refParam := func(what string) {
		path(RefParam, what, b.id)
	}
	version := b.field("version", int64Scalar, true, "The ref's version as the caller read it: a write at another version is refused with version_conflict.")
	switch op {
	case ir.GraphOpBranch:
		input(
			b.field("from", b.id, false, "The ref to branch: a ref of this root. Absent, the root's primary line."),
			b.field("name", "string", true, "The change set's name, which no live ref of the root holds."),
		)
		out.TypeRef = b.ref()
	case ir.GraphOpSave:
		refParam("The change set to write.")
		input(version, b.fieldOf("edits", b.edits(), true, "The edits, per kind."))
		out.TypeRef = b.saveResult()
	case ir.GraphOpArrange:
		refParam("The change set to write.")
		keys := b.field("keys", b.id, true, "The entity keys of every live child of the parent in the ref's tree, in the order wanted.")
		keys.TypeRef.IsArray = true
		input(
			version,
			b.fieldOf("kind", b.kindEnum(), true, "The kind whose rows to place: one whose order is a string key."),
			b.field("parent", b.id, false, "The parent's entity key. Absent, the rows of the kind that have no parent."),
			keys,
		)
		out.TypeRef = b.saveResult()
	case ir.GraphOpCommit:
		refParam("The change set to commit.")
		input(
			version,
			b.field("message", "string", false, "The commit's message."),
			b.field("tag", "boolean", false, "Tags the commit, which takes the root's next sequence: a release names a tagged commit."),
		)
		out.TypeRef = b.commitResult()
	case ir.GraphOpSeal:
		refParam("The change set to seal.")
		input(version)
		out.TypeRef = b.commitResult()
	case ir.GraphOpMerge:
		refParam("The change set to merge: the source.")
		resolutions := b.fieldOf("resolutions", b.resolution(), false, "What settles each conflict an earlier call answered.")
		resolutions.TypeRef.IsArray = true
		input(
			b.field("into", b.id, false, "The ref to merge into: a ref of this root. Absent, the source's parent."),
			b.field("version", int64Scalar, true, "The version of the ref merged into, as the caller read it."),
			resolutions,
			b.field("message", "string", false, "The merge commit's message."),
			b.field("tag", "boolean", false, "Tags the merge commit, which takes the root's next sequence."),
		)
		out.TypeRef = b.mergeResult()
	case ir.GraphOpRebase:
		refParam("The change set to move onto its parent's head.")
		resolutions := b.fieldOf("resolutions", b.resolution(), false, "What settles each conflict an earlier call answered.")
		resolutions.TypeRef.IsArray = true
		input(version, resolutions)
		out.TypeRef = b.mergeResult()
	case ir.GraphOpRevert:
		refParam("The change set to write.")
		input(version, b.field("toCommit", b.id, true, "The commit whose tree the change set is to compose to."))
		out.TypeRef = b.commitResult()
	case ir.GraphOpDiscard:
		refParam("The ref to discard.")
		input(version)
		out.TypeRef = ir.TypeRef{Name: "boolean"}
	case ir.GraphOpHistory:
		refParam("The ref whose commits to list.")
		out.TypeRef = b.commit()
		out.TypeRef.IsArray = true
	case ir.GraphOpCompose:
		refParam("The ref whose tree to read.")
		out.TypeRef = b.tree()
	case ir.GraphOpRelease:
		input(
			b.field("commit", b.id, true, "The commit to release: a tagged commit of this root."),
			b.field("version", int64Scalar, true, "The release pointer's version as the caller read it: 0 for the root's first release."),
		)
		out.TypeRef = b.release()
	case ir.GraphOpReleased:
		out.TypeRef = b.released()
	case ir.GraphOpMaterialize:
		path(CommitParam, "The commit whose tree to read.", b.id)
		out.TypeRef = b.tree()
	case ir.GraphOpDiff:
		query("from", "The earlier commit.", b.id)
		query("to", "The later commit.", b.id)
		out.TypeRef = b.change()
		out.TypeRef.IsArray = true
	case ir.GraphOpEntityHistory:
		path(KindParam, "The entity's kind, as the graph names it: "+strings.Join(b.kinds(), ", ")+".", "string")
		path(EntityParam, "The entity's key.", b.id)
		out.TypeRef = b.entityChange()
		out.TypeRef.IsArray = true
	}
	return out
}

// Expand fills each @graphOperations set of api with the operations of
// its root's graph, read from db, the API's database, and adds the types
// and enums they take and return (D138). It assumes the sets passed
// verify.CheckGraphOperations, and does nothing for an API without such a
// set. It returns the scalar names the added types use, so the caller can
// hydrate any api did not hold; one only db declares is copied from it.
// It may run again on its own output.
func Expand(api, db *ir.Schema, reg *registry.Registry) ([]string, error) {
	var sets []*ir.OperationSet
	for _, set := range api.OperationSets {
		if set.IsGraphOperations() {
			sets = append(sets, set)
		}
	}
	if len(sets) == 0 || db == nil {
		return nil, nil
	}
	var scalars []string
	for _, set := range sets {
		plan, err := PlanSet(set, db, reg)
		if err != nil {
			return nil, err
		}
		set.Operations = plan.Operations
		if api.Types == nil {
			api.Types = map[string]*ir.TypeDef{}
		}
		if api.Enums == nil {
			api.Enums = map[string]*ir.EnumDef{}
		}
		for _, td := range plan.Types {
			td.Owner = api.Name
			api.Types[td.Name] = td
		}
		for _, ed := range plan.Enums {
			ed.Owner = api.Name
			api.Enums[ed.Name] = ed
		}
		for _, name := range plan.Scalars {
			if !slices.Contains(scalars, name) {
				scalars = append(scalars, name)
			}
		}
	}
	if api.Scalars == nil {
		api.Scalars = map[string]*ir.ScalarDef{}
	}
	for _, name := range scalars {
		if api.Scalars[name] != nil {
			continue
		}
		def := &ir.ScalarDef{Name: name}
		if declared := db.Scalars[name]; declared != nil {
			def = cloneScalar(declared)
		}
		api.Scalars[name] = def
	}
	return scalars, nil
}

// builder adds the types and enums of one set's operations to its plan,
// each once.
type builder struct {
	plan  *Plan
	db    *ir.Schema
	graph graphdesc.Graph
	// named are the names added, each with what it is of the graph.
	named map[string]string
	// id is the scalar of the graph's own ids (a ref's, a commit's, an
	// entity's key), and actor that of who wrote a row.
	id, actor string
	// operation is the operation the types being added are first for.
	operation string
	err       error
}

// claim records name as added for what, and reports whether it was not
// before. A name two different things of the graph take is the plan's
// error.
func (b *builder) claim(kind, name, what string) bool {
	if taken, ok := b.named[name]; ok {
		if taken != what && b.err == nil {
			b.err = fmt.Errorf("the %s graph would add two things named %s (%s; %s): rename the member type or the enum", b.plan.Graph, name, taken, what)
		}
		return false
	}
	b.named[name] = what
	b.plan.Added = append(b.plan.Added, Added{Kind: kind, Name: name, For: what})
	return true
}

// useScalar records a scalar the added types name; a primitive is none.
func (b *builder) useScalar(name string) {
	switch name {
	case "string", "number", "boolean":
		return
	}
	if !slices.Contains(b.plan.Scalars, name) {
		b.plan.Scalars = append(b.plan.Scalars, name)
	}
}

// field is a member of an added type that holds a scalar or a primitive.
func (b *builder) field(name, typeName string, required bool, comment string) *ir.FieldDef {
	b.useScalar(typeName)
	return &ir.FieldDef{Name: name, Comment: comment, TypeRef: ir.TypeRef{Name: typeName}, Required: required, Origin: ir.OriginGraphOperations}
}

// fieldOf is a member of an added type that holds another added type or
// enum.
func (b *builder) fieldOf(name string, ref ir.TypeRef, required bool, comment string) *ir.FieldDef {
	return &ir.FieldDef{Name: name, Comment: comment, TypeRef: ref, Required: required, Origin: ir.OriginGraphOperations}
}

// addType adds the type name of role with fields, once.
func (b *builder) addType(name, comment string, role ir.Role, fields ...*ir.FieldDef) ir.TypeRef {
	if b.claim("type", name, strings.TrimSuffix(comment, ".")) {
		b.plan.Types = append(b.plan.Types, &ir.TypeDef{Name: name, Comment: comment, Role: role, Origin: ir.OriginGraphOperations, Fields: fields})
	}
	return ir.TypeRef{Name: name}
}

// addEnum adds the enum name of the string values, once.
func (b *builder) addEnum(name, comment string, values []string) ir.TypeRef {
	if b.claim("enum", name, strings.TrimSuffix(comment, ".")) {
		ed := &ir.EnumDef{Name: name, Comment: comment, Origin: ir.OriginGraphOperations}
		for _, value := range values {
			ed.Values = append(ed.Values, ir.EnumValueDef{Name: enumMember(value), SerializedAs: value})
		}
		b.plan.Enums = append(b.plan.Enums, ed)
	}
	return ir.TypeRef{Name: name}
}

// enumMember is the member name of an enum value: its words in Pascal
// case (step, line_item and ADD are Step, LineItem and Add).
func enumMember(value string) string {
	return codegen.ToPascalCase(strings.ToLower(value))
}

func (b *builder) name(suffix string) string { return TypeName(b.plan.Graph, suffix) }

// kinds are the graph's kinds, in descriptor order.
func (b *builder) kinds() []string {
	out := make([]string, 0, len(b.plan.Members))
	for _, m := range b.plan.Members {
		out = append(out, m.Kind)
	}
	return out
}

// kindEnum is the enum of the graph's kinds.
func (b *builder) kindEnum() ir.TypeRef {
	return b.addEnum(b.name("Kind"), fmt.Sprintf("A kind of the %s graph's entities, as its descriptor names it.", b.plan.Graph), b.kinds())
}

// ref is the view of a ref the facade returns.
func (b *builder) ref() ir.TypeRef {
	g := b.plan.Graph
	return b.addType(b.name("Ref"), fmt.Sprintf("A line of the %s graph: a primary line when parent is absent, else a change set.", g), ir.RoleEmbeddedStruct,
		b.field("id", b.id, true, "The ref's id."),
		b.field("root", b.id, true, "The root the ref belongs to."),
		b.field("parent", b.id, false, "The ref a change set branched from; absent for a primary line."),
		b.field("base", b.id, false, "The commit a change set's rows are laid over; absent for none."),
		b.field("head", b.id, false, "The ref's last commit; absent before its first."),
		b.field("name", "string", true, "The ref's name, which one live ref of the root holds."),
		b.field("sealed", "boolean", true, "True once the ref is sealed: it refuses writes."),
		b.field("discarded", "boolean", true, "True once the ref is discarded."),
		b.field("version", int64Scalar, true, "The ref's version, which each write through it takes and moves."),
	)
}

// commit is the view of a commit the facade returns.
func (b *builder) commit() ir.TypeRef {
	g := b.plan.Graph
	return b.addType(b.name("Commit"), fmt.Sprintf("A commit of the %s graph: the exact row versions one ref sealed.", g), ir.RoleEmbeddedStruct,
		b.field("id", b.id, true, "The commit's id."),
		b.field("root", b.id, true, "The root the commit belongs to."),
		b.field("ref", b.id, true, "The ref that wrote the commit."),
		b.field("parent", b.id, false, "The commit before it; absent for a commit with none."),
		b.field("message", "string", true, "The commit's message, empty for none."),
		b.field("schemaEpoch", int64Scalar, true, "The schema epoch the commit was written at."),
		b.field("contentHash", "string", true, "The hash of the content of the commit's tree."),
		b.field("sequence", int64Scalar, false, "The root's sequence the commit took when it was tagged; absent for an untagged commit."),
		b.field("createdAt", dateTimeScalar, true, "When the commit was written."),
		b.field("createdBy", b.actor, true, "Who wrote the commit."),
		b.field("snapshot", "boolean", true, "True when the commit's whole tree is stored."),
	)
}

// release is the view of a release pointer the facade returns.
func (b *builder) release() ir.TypeRef {
	g := b.plan.Graph
	return b.addType(b.name("Release"), fmt.Sprintf("The released commit of one root of the %s graph.", g), ir.RoleEmbeddedStruct,
		b.field("id", b.id, true, "The pointer's id."),
		b.field("root", b.id, true, "The root the pointer belongs to."),
		b.field("commit", b.id, true, "The released commit."),
		b.field("version", int64Scalar, true, "The pointer's version, which a release takes and moves."),
	)
}

// finding is a problem compose found in a ref's tree.
func (b *builder) finding() ir.TypeRef {
	g := b.plan.Graph
	return b.addType(b.name("Finding"), fmt.Sprintf("A problem in a tree of the %s graph, such as a row whose parent is absent.", g), ir.RoleEmbeddedStruct,
		b.field("code", "string", true, "What is wrong: missing_parent, duplicate_entity_key, singleton, parent_cycle or order_out_of_range."),
		b.field("kind", "string", true, "The kind of the entity the finding is about."),
		b.field("entityKey", b.id, false, "The entity the finding is about; absent for a finding about a whole kind."),
		b.field("message", "string", true, "The finding, in words."),
	)
}

// content are the row members of member's content fields, each typed for
// the API: a scalar and a primitive as the member holds it, an enum of
// the database as a copy of it, a to-one relation as its key under
// <field>Id, and any other type as JSON.
func (b *builder) content(member graphdesc.Member, described graphdesc.Kind) []*ir.FieldDef {
	var out []*ir.FieldDef
	for _, fd := range graphroutes.ContentFields(b.db, member, described) {
		field := &ir.FieldDef{
			Name:       fd.Name,
			Comment:    fd.Comment,
			TypeRef:    fd.TypeRef,
			Required:   fd.Required,
			ValueRules: fd.Element(),
			Origin:     ir.OriginGraphOperations,
		}
		name := fd.TypeRef.Name
		switch target := graphroutes.RelationTarget(b.db, fd); {
		case target != nil:
			key := graphroutes.KeyField(target)
			field.Name = graphroutes.RelationMember(fd.Name)
			field.TypeRef = ir.TypeRef{Name: key.TypeRef.Name}
			if field.Comment == "" {
				field.Comment = fmt.Sprintf("The key of the %s the row's %s is.", target.Name, fd.Name)
			}
			b.useScalar(key.TypeRef.Name)
		case b.db.Enums[name] != nil:
			declared := b.db.Enums[name]
			copied := b.name(name)
			if b.claim("enum", copied, "A copy of the enum "+name) {
				ed := &ir.EnumDef{Name: copied, Comment: declared.Comment, Origin: ir.OriginGraphOperations}
				ed.Values = append(ed.Values, declared.Values...)
				b.plan.Enums = append(b.plan.Enums, ed)
			}
			field.TypeRef.Name = copied
		case name == "string", name == "number", name == "boolean":
		case b.db.Scalars[name] != nil:
			b.useScalar(name)
		default:
			// A struct of the database, which the API does not declare.
			field.TypeRef = ir.TypeRef{Name: jsonScalar}
			field.ValueRules = ir.ValueRules{}
			b.useScalar(jsonScalar)
		}
		out = append(out, field)
	}
	return out
}

// rows adds each member's row type, and returns one list member per kind,
// in descriptor order.
func (b *builder) rows() []*ir.FieldDef {
	g := b.plan.Graph
	var out []*ir.FieldDef
	for i, member := range b.graph.Members {
		typeName := member.Type.Name
		fields := []*ir.FieldDef{b.field("entityKey", b.id, true, "The entity's key, which its rows on every ref hold.")}
		fields = append(fields, b.content(member, b.graph.Descriptor.Kinds[i])...)
		ref := b.addType(RowType(g, typeName), fmt.Sprintf("A %s of the %s graph: the content of one entity in a tree.", typeName, g), ir.RoleEmbeddedStruct, fields...)
		ref.IsArray = true
		out = append(out, b.fieldOf(TreeMember(typeName), ref, true, fmt.Sprintf("The tree's %s rows, ordered by their order and then by entity key.", typeName)))
	}
	return out
}

// tree is a tree of the graph: each kind's rows, its content hash and the
// problems found in it.
func (b *builder) tree() ir.TypeRef {
	g := b.plan.Graph
	findings := b.fieldOf("findings", b.finding(), true, "The problems found in a ref's tree; none for a commit's.")
	findings.TypeRef.IsArray = true
	fields := append(b.rows(),
		b.field("contentHash", "string", true, "The hash of the tree's content."),
		findings,
	)
	return b.addType(b.name("Tree"), fmt.Sprintf("A tree of the %s graph: the live rows of each kind.", g), ir.RoleEmbeddedStruct, fields...)
}

// saved are the rows a save or an arrange wrote, per kind.
func (b *builder) saved() ir.TypeRef {
	g := b.plan.Graph
	return b.addType(b.name("Rows"), fmt.Sprintf("Rows of the %s graph, per kind.", g), ir.RoleEmbeddedStruct, b.rows()...)
}

// edits are the edits a save applies, per kind: each kind's upserts,
// deletes and unsets.
func (b *builder) edits() ir.TypeRef {
	g := b.plan.Graph
	var fields []*ir.FieldDef
	for i, member := range b.graph.Members {
		typeName := member.Type.Name
		content := []*ir.FieldDef{b.field("entityKey", b.id, false, "The entity to write; absent, the row is a new entity, whose key the database makes.")}
		content = append(content, b.content(member, b.graph.Descriptor.Kinds[i])...)
		upsert := b.addType(RowInputType(g, typeName), fmt.Sprintf("A %s an upsert writes on a ref of the %s graph.", typeName, g), ir.RoleAPIInput, content...)
		upsert.IsArray = true
		del := b.field("delete", b.id, false, "The entities to delete on the ref, by key.")
		del.TypeRef.IsArray = true
		unset := b.field("unset", b.id, false, "The entities whose row on the ref to remove, by key, so the ref reads each through its base again.")
		unset.TypeRef.IsArray = true
		edits := b.addType(RowEditsType(g, typeName), fmt.Sprintf("The edits of the %s rows of a ref of the %s graph: its upserts, then its deletes, then its unsets.", typeName, g), ir.RoleAPIInput,
			b.fieldOf("upsert", upsert, false, "The rows to write as the ref's own, each found by its entity key."),
			del,
			unset,
		)
		fields = append(fields, b.fieldOf(TreeMember(typeName), edits, false, fmt.Sprintf("The edits of the ref's %s rows.", typeName)))
	}
	return b.addType(b.name("Edits"), fmt.Sprintf("The edits a save applies to a ref of the %s graph, per kind.", g), ir.RoleAPIInput, fields...)
}

// saveResult is what a save and an arrange return.
func (b *builder) saveResult() ir.TypeRef {
	g := b.plan.Graph
	return b.addType(b.name("SaveResult"), fmt.Sprintf("A ref of the %s graph at its new version, and the rows written.", g), ir.RoleEmbeddedStruct,
		b.fieldOf("ref", b.ref(), true, "The ref at its new version."),
		b.fieldOf("saved", b.saved(), true, "The rows written, per kind: a save's upserts in edit order, an arrange's in the order wanted."),
	)
}

// commitResult is what a commit, a seal and a revert return.
func (b *builder) commitResult() ir.TypeRef {
	g := b.plan.Graph
	return b.addType(b.name("CommitResult"), fmt.Sprintf("A ref of the %s graph at its new version, and the commit written.", g), ir.RoleEmbeddedStruct,
		b.fieldOf("ref", b.ref(), true, "The ref at its new version."),
		b.fieldOf("commit", b.commit(), false, "The commit written; absent when there was nothing to commit."),
	)
}

// conflict is one unit both sides of a merge changed differently.
func (b *builder) conflict() ir.TypeRef {
	g := b.plan.Graph
	return b.addType(b.name("Conflict"), fmt.Sprintf("One unit both sides of a merge of the %s graph changed differently, or an edit against a delete (path \"\").", g), ir.RoleEmbeddedStruct,
		b.fieldOf("kind", b.kindEnum(), true, "The entity's kind."),
		b.field("entityKey", b.id, true, "The entity's key."),
		b.field("path", "string", true, "The unit: a column, or an entry of a keyed column; empty for the whole entity."),
		b.field("base", "string", false, "The unit's value in the merge base, as JSON text; absent where it is absent."),
		b.field("ours", "string", false, "The unit's value on the side merged into, as JSON text; absent where it is absent."),
		b.field("theirs", "string", false, "The unit's value on the side merged, as JSON text; absent where it is absent."),
		b.field("oursAuthor", b.actor, false, "Who wrote our side's row, when the kind names its author."),
		b.field("theirsAuthor", b.actor, false, "Who wrote their side's row, when the kind names its author."),
	)
}

// resolution settles one conflict.
func (b *builder) resolution() ir.TypeRef {
	g := b.plan.Graph
	take := b.addEnum(b.name("Take"), fmt.Sprintf("The side of a merge of the %s graph a resolution takes a unit from.", g), []string{"base", "ours", "theirs"})
	return b.addType(b.name("Resolution"), fmt.Sprintf("Settles the conflict at path of one entity of the %s graph: take the unit from one side, or give its value.", g), ir.RoleAPIInput,
		b.fieldOf("kind", b.kindEnum(), true, "The entity's kind."),
		b.field("entityKey", b.id, true, "The entity's key."),
		b.field("path", "string", true, "The conflict's path."),
		b.fieldOf("take", take, false, "The side to take the unit from; one of take and value."),
		b.field("value", "string", false, "The unit's value, as JSON text (\"null\" is a value); one of take and value."),
	)
}

// mergeResult is what a merge and a rebase return.
func (b *builder) mergeResult() ir.TypeRef {
	g := b.plan.Graph
	conflicts := b.fieldOf("conflicts", b.conflict(), true, "The conflicts left; when there is one, nothing was written.")
	conflicts.TypeRef.IsArray = true
	return b.addType(b.name("MergeResult"), fmt.Sprintf("The target of a merge of the %s graph, or the change set a rebase moved, and the commit written.", g), ir.RoleEmbeddedStruct,
		b.fieldOf("ref", b.ref(), true, "The ref written, at its new version; as it was when conflicts are left."),
		b.fieldOf("commit", b.commit(), false, "The commit written; absent when conflicts are left."),
		conflicts,
	)
}

// operationEnum is the enum of what a commit did to an entity.
func (b *builder) operationEnum() ir.TypeRef {
	return b.addEnum(b.name("ChangeOperation"), fmt.Sprintf("What a change of the %s graph did to an entity.", b.plan.Graph), []string{"ADD", "UPDATE", "DELETE"})
}

// change is one entity two commits differ on.
func (b *builder) change() ir.TypeRef {
	g := b.plan.Graph
	return b.addType(b.name("Change"), fmt.Sprintf("One entity two commits of the %s graph differ on.", g), ir.RoleEmbeddedStruct,
		b.fieldOf("kind", b.kindEnum(), true, "The entity's kind."),
		b.field("entityKey", b.id, true, "The entity's key."),
		b.fieldOf("operation", b.operationEnum(), true, "What the later tree did to the entity."),
		b.field("row", "string", false, "The entity's canonical row in the later tree, as JSON text; absent for a DELETE."),
	)
}

// entityChange is one commit that changed an entity.
func (b *builder) entityChange() ir.TypeRef {
	g := b.plan.Graph
	return b.addType(b.name("EntityChange"), fmt.Sprintf("One commit of the %s graph's primary line that changed an entity, and what it did to it.", g), ir.RoleEmbeddedStruct,
		b.fieldOf("commit", b.commit(), true, "The commit."),
		b.fieldOf("operation", b.operationEnum(), true, "What the commit did to the entity."),
	)
}

// released is a root's release pointer and the released tree.
func (b *builder) released() ir.TypeRef {
	g := b.plan.Graph
	return b.addType(b.name("Released"), fmt.Sprintf("A root's release pointer of the %s graph and the tree of the commit it names.", g), ir.RoleEmbeddedStruct,
		b.fieldOf("release", b.release(), true, "The release pointer."),
		b.fieldOf("tree", b.tree(), true, "The released tree."),
	)
}

// AddedNames are the names of every type and enum plan adds, sorted.
func AddedNames(plan *Plan) []string {
	out := make([]string, 0, len(plan.Added))
	for _, a := range plan.Added {
		out = append(out, a.Name)
	}
	sort.Strings(out)
	return out
}

// cloneScalar copies def, whose maps and lists the API's hydration may
// write, so the database's schema, which a build caches and shares, keeps
// its own.
func cloneScalar(def *ir.ScalarDef) *ir.ScalarDef {
	copied := *def
	if def.TypeMappings != nil {
		copied.TypeMappings = make(map[string]string, len(def.TypeMappings))
		for k, v := range def.TypeMappings {
			copied.TypeMappings[k] = v
		}
	}
	if def.ReservedWords != nil {
		copied.ReservedWords = append([]string(nil), def.ReservedWords...)
	}
	return &copied
}
