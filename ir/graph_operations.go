package ir

// A version graph's operations as routes (D138). An API serves them by
// marking a class with no methods @graphOperations(Root, { ... }), from
// @superschematic/api:
//
//	@graphOperations(Recipe, { permissions: { release: ["recipes.release"] }, by: ["createdBy"] })
//	@requirePermission(["recipes.write"])
//	export abstract class RecipeOperations {}
//
// The reader records the decorator as OperationSet.GraphOperations and the
// class's @auth, @requirePermission and @publicRoute as the set's Auth,
// Permissions and Public. The loader reads the API's database
// (Schema.Database), and fills the set with one operation for each
// operation of the graph the set serves, in the order of
// [GraphOperationNames], each with Origin OriginGraphOperations and
// FieldDef.GraphOperation set, and adds the types and enums they take and
// return, with the same Origin. The OpenAPI document, the SDKs, the
// permission catalog and the tool documents read them as any other
// operation. A server whose generator writes graph handlers routes an
// operation whose GraphOperation is set to one, which runs it through the
// graph's generated facade; any other keeps the operation a method of the
// implementation's interface, to be written by hand.

// OriginGraphOperations is the Origin of each operation, type, field and
// enum the loader adds for a @graphOperations set (D138), so the schema
// writer writes the decorator in their place.
const OriginGraphOperations = "graphOperations"

// GraphInputArgument is the argument a graph operation's body travels as:
// the request body, of the input type the loader adds. A GET takes none.
const GraphInputArgument = "input"

// The operations of a version graph a @graphOperations set serves, by the
// names the decorator's operations and permissions take.
const (
	GraphOpBranch        = "branch"
	GraphOpSave          = "save"
	GraphOpArrange       = "arrange"
	GraphOpCommit        = "commit"
	GraphOpSeal          = "seal"
	GraphOpMerge         = "merge"
	GraphOpRebase        = "rebase"
	GraphOpRevert        = "revert"
	GraphOpDiscard       = "discard"
	GraphOpHistory       = "history"
	GraphOpCompose       = "compose"
	GraphOpRelease       = "release"
	GraphOpReleased      = "released"
	GraphOpMaterialize   = "materialize"
	GraphOpDiff          = "diff"
	GraphOpEntityHistory = "entityHistory"
)

// GraphOperationNames are the operations a @graphOperations set serves
// when it narrows none, in the order the loader adds them.
var GraphOperationNames = []string{
	GraphOpBranch, GraphOpSave, GraphOpArrange, GraphOpCommit, GraphOpSeal,
	GraphOpMerge, GraphOpRebase, GraphOpRevert, GraphOpDiscard, GraphOpHistory,
	GraphOpCompose, GraphOpRelease, GraphOpReleased, GraphOpMaterialize,
	GraphOpDiff, GraphOpEntityHistory,
}

// GraphOperationsOwned are the operations that write a change set, which
// the set's row rule keeps to the ref's creator (D138). A revert is among
// them: it writes the change set's rows and commits on it, as a save and
// a commit do.
var GraphOperationsOwned = []string{
	GraphOpSave, GraphOpArrange, GraphOpCommit, GraphOpSeal, GraphOpRebase, GraphOpRevert, GraphOpDiscard,
}

// GraphOperationsByCreatedBy is the one member a @graphOperations set's
// by names: the ref's creator.
const GraphOperationsByCreatedBy = "createdBy"

// GraphOperationsConfig is @graphOperations on an operation set.
type GraphOperationsConfig struct {
	// Type is the root of the version graph the set serves, by its name
	// as the API imports it: a @versionGraph table of the API's database
	// (Schema.Database).
	Type string `json:"type" yaml:"type"`

	// Operations narrows the set to the operations it lists, each one of
	// [GraphOperationNames]. Empty serves every one.
	Operations []string `json:"operations,omitempty" yaml:"operations,omitempty"`

	// Permissions names, by operation, the permissions that replace the
	// set's own (OperationSet.Permissions) for that operation.
	Permissions map[string][]string `json:"permissions,omitempty" yaml:"permissions,omitempty"`

	// By is the row rule: the members of a change set's ref that name who
	// may write it. ["createdBy"] keeps the operations that write a change
	// set ([GraphOperationsOwned]) to the ref's creator. Empty admits
	// every caller the route admits.
	By []string `json:"by,omitempty" yaml:"by,omitempty"`

	// OverridePermission is the permission whose holder By does not hold
	// back.
	OverridePermission string `json:"overridePermission,omitempty" yaml:"overridePermission,omitempty"`
}

// Serves reports whether the set serves the operation name: every one of
// [GraphOperationNames] when Operations is empty, else those it lists.
func (c *GraphOperationsConfig) Serves(name string) bool {
	if c == nil {
		return false
	}
	if len(c.Operations) == 0 {
		for _, known := range GraphOperationNames {
			if known == name {
				return true
			}
		}
		return false
	}
	for _, listed := range c.Operations {
		if listed == name {
			return true
		}
	}
	return false
}

// GraphOperation marks an operation the loader added for a
// @graphOperations set (D138): which operation of which graph it runs.
type GraphOperation struct {
	// Database is the DB service the graph belongs to, the API's
	// database.
	Database string `json:"database" yaml:"database"`

	// Graph is the graph's name ([TypeDef.VersionGraphName]), which
	// prefixes its generated types.
	Graph string `json:"graph" yaml:"graph"`

	// Type is the root's type name.
	Type string `json:"type" yaml:"type"`

	// Key is the name of the root's @key field: the route's first path
	// parameter, and the operation's first argument, typed by the key.
	Key string `json:"key" yaml:"key"`

	// Operation is the operation's name, one of [GraphOperationNames].
	Operation string `json:"operation" yaml:"operation"`

	// By is the set's row rule when the operation writes a change set
	// ([GraphOperationsOwned]): the members of the ref that name who may
	// write it. Empty for every other operation, and without a rule.
	By []string `json:"by,omitempty" yaml:"by,omitempty"`

	// OverridePermission is the permission whose holder By does not hold
	// back; empty without By.
	OverridePermission string `json:"overridePermission,omitempty" yaml:"overridePermission,omitempty"`
}

// IsGraphOperations reports whether the set is @graphOperations, whose
// operations the loader adds.
func (s *OperationSet) IsGraphOperations() bool {
	return s != nil && s.GraphOperations != nil
}

// TakesSetClause reports whether the set's class takes the user clause
// (@auth, @requirePermission, @publicRoute), which each operation the
// loader adds takes as its own: a @behaviorOperations set (D103) and a
// @graphOperations set (D138).
func (s *OperationSet) TakesSetClause() bool {
	return s.IsBehaviorOperations() || s.IsGraphOperations()
}
