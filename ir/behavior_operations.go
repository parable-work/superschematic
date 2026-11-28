package ir

// A table's behavior operations (D103). An API serves them by marking a
// class with no methods @behaviorOperations(Type), from @superschematic/api:
//
//	@behaviorOperations(Order)
//	@requirePermission(["orders.write"])
//	export abstract class OrderActions {}
//
// The reader records the decorator as OperationSet.BehaviorOperations and
// the class's @auth, @requirePermission and @publicRoute as the set's Auth,
// Permissions and Public. The loader reads the API's database
// (Schema.Database), and fills the set with one operation for each operation
// of each behavior the table composes, implemented by it or on a field, in
// its list order and then declaration order, each with Origin
// OriginBehavior and
// FieldDef.BehaviorOperation set, and adds the types and enums they take and
// return, with the same Origin. The OpenAPI document, the SDKs, the
// permission catalog and the tool documents read them as any other
// operation. A server routes an operation whose BehaviorOperation is set to
// a generated handler that runs it through the behaviors' core: the
// implementation the project writes has no method for it.

// OriginBehavior is the Origin of each operation, type, field and enum the
// loader adds for a @behaviorOperations set (D103), so the schema writer
// writes the decorator in their place.
const OriginBehavior = "behavior"

// BehaviorInputArgument is the argument a behavior operation's parameters
// travel as: the request body, of the input type the loader adds. An
// operation whose parameters declare no property takes none.
const BehaviorInputArgument = "input"

// BehaviorOperationsConfig is @behaviorOperations on an operation set.
type BehaviorOperationsConfig struct {
	// Type is the table whose behaviors' operations the set serves, by
	// its name as the API imports it: a table of the API's database
	// (Schema.Database) one of whose fields is a behavior.
	Type string `json:"type" yaml:"type"`

	// MCP overrides MCP visibility and invocation policy for the
	// operations the set adds. More specific entries replace less specific
	// ones (Operations > Behaviors > set-level), so a set or behavior can
	// be hidden while selectively exposing individual operations.
	MCP *BehaviorOperationsMCP `json:"mcp,omitempty" yaml:"mcp,omitempty"`
}

// BehaviorOperationsMCP configures MCP visibility and invocation policy for
// a @behaviorOperations set.
type BehaviorOperationsMCP struct {
	// Hidden hides every operation in the set from MCP unless overridden
	// by a visible entry in Behaviors or Operations. Requires Reason.
	Hidden bool `json:"hidden,omitempty" yaml:"hidden,omitempty"`

	// Reason states why the set's operations are hidden from MCP.
	Reason string `json:"reason,omitempty" yaml:"reason,omitempty"`

	// InvocationPolicy sets the invocation policy for every visible
	// operation in the set.
	InvocationPolicy string `json:"invocationPolicy,omitempty" yaml:"invocationPolicy,omitempty"`

	// WriteInvocationPolicy sets the invocation policy for visible write
	// operations in the set (taking precedence over set-level
	// InvocationPolicy for writes while reads keep InvocationPolicy or the
	// behavior's default).
	WriteInvocationPolicy string `json:"writeInvocationPolicy,omitempty" yaml:"writeInvocationPolicy,omitempty"`

	// Behaviors overrides MCP settings for one behavior use of the table,
	// keyed by the use's member name (behaviorlayout.UseMember, e.g.
	// "comments", "recurrence", "status").
	Behaviors map[string]*BehaviorOperationMCP `json:"behaviors,omitempty" yaml:"behaviors,omitempty"`

	// Operations overrides MCP settings for individual operations, keyed
	// by "<use>.<op>" (e.g. "comments.list") or the expanded operation
	// name (e.g. "commentsList").
	Operations map[string]*BehaviorOperationMCP `json:"operations,omitempty" yaml:"operations,omitempty"`
}

// BehaviorOperationMCP overrides MCP visibility or invocation policy for a
// behavior use or individual behavior operation.
type BehaviorOperationMCP struct {
	// Hidden hides matching operation(s) from MCP when true. A non-nil
	// entry with Hidden false explicitly exposes operation(s) even when
	// the parent set or behavior is hidden.
	Hidden bool `json:"hidden,omitempty" yaml:"hidden,omitempty"`

	// Reason states why the operation(s) are hidden when Hidden is true.
	Reason string `json:"reason,omitempty" yaml:"reason,omitempty"`

	// InvocationPolicy sets the invocation policy for visible operation(s).
	InvocationPolicy string `json:"invocationPolicy,omitempty" yaml:"invocationPolicy,omitempty"`

	// WriteInvocationPolicy sets the invocation policy for visible write
	// operations of a behavior use (allowed in Behaviors, refused in
	// Operations).
	WriteInvocationPolicy string `json:"writeInvocationPolicy,omitempty" yaml:"writeInvocationPolicy,omitempty"`
}

// BehaviorOperation marks an operation the loader added for a
// @behaviorOperations set (D103): which operation of which behavior, on
// which field of which table, it runs.
type BehaviorOperation struct {
	// Database is the DB service the table belongs to, the API's
	// database.
	Database string `json:"database" yaml:"database"`

	// Type is the table's type name.
	Type string `json:"type" yaml:"type"`

	// Key is the name of the table's @key field: the route's path
	// parameter, and the operation's first argument, typed by the key.
	Key string `json:"key" yaml:"key"`

	// Use is the use's id ([BehaviorRef.UseID]): the anchor field's name.
	Use string `json:"use" yaml:"use"`

	// Behavior is the use's registered behavior name.
	Behavior string `json:"behavior" yaml:"behavior"`

	// Operation is the operation's name in the behavior's declaration.
	Operation string `json:"operation" yaml:"operation"`

	// Writes is the declaration's writes: the operation changes stored
	// state.
	Writes bool `json:"writes,omitempty" yaml:"writes,omitempty"`

	// Table is true for an operation of the table as a whole (D104), one
	// its declaration gives the scope schema: its route takes no key, and
	// the servers run it as a command without a row. Key still names the
	// table's @key field, which its result's keys are of.
	Table bool `json:"table,omitempty" yaml:"table,omitempty"`

	// Permissions are the permissions the operation checks, sorted and
	// each once (a Workflow transition's permission, which the core checks
	// for the moves that name it): the strings the use's config holds at
	// the members its declaration says the operation checks (D103's step
	// 5), or every permission the config names when the declaration says
	// none. The permission catalog lists each under the operation.
	Permissions []string `json:"permissions,omitempty" yaml:"permissions,omitempty"`
}

// IsBehaviorOperations reports whether the set is @behaviorOperations,
// whose operations the loader adds.
func (s *OperationSet) IsBehaviorOperations() bool {
	return s != nil && s.BehaviorOperations != nil
}
