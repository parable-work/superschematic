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
// of each behavior on a field of the table, in field order and then
// declaration order, each with Origin OriginBehavior and
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

	// Permissions are the permissions the use's config names, sorted and
	// each once (a Workflow transition's permission), which the core
	// checks for the moves that name them. The permission catalog lists
	// each under the operation.
	Permissions []string `json:"permissions,omitempty" yaml:"permissions,omitempty"`
}

// IsBehaviorOperations reports whether the set is @behaviorOperations,
// whose operations the loader adds.
func (s *OperationSet) IsBehaviorOperations() bool {
	return s != nil && s.BehaviorOperations != nil
}
