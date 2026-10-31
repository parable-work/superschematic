package rustrestgen

import (
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/behaviortypes"
	"github.com/parable-work/superschematic/internal/generator/rustutil"
)

// The behavior operations of a Rust API (D103): the operations the loader
// adds for its @behaviorOperations sets, one per operation of each behavior
// on a field of a table of its database. The router mounts each as it
// mounts an authored operation, behind its route's controls and with its
// arguments decoded into its Args struct, and its handler runs it through
// the behaviors' core: src/behaviors.rs hands it to
// superschematic-rules-adapter, with the core of
// superschematic-rules-behaviors and the database's layout document, in a
// transaction of the database's pool. The namespace traits have no method
// for it, and the scaffold none. Rust has no ORM: the crate calls the
// adapter itself.

// The crates src/behaviors.rs runs the operations with, which the API
// crate depends on by path: they are unpublished, so a Rust server with
// behavior operations builds only in a checkout ([paths]
// rules_adapter_rust and rules_behaviors_rust).
const (
	RulesAdapterCrate   = "superschematic-rules-adapter"
	RulesBehaviorsCrate = "superschematic-rules-behaviors"
)

// RulesSerdeVersion is the serde crate src/behaviors.rs bounds its
// generic arguments with, the version the adapter takes.
const RulesSerdeVersion = "1.0.228"

// BehaviorInfo is the behavior operation an endpoint runs (D103):
// ir.BehaviorOperation as the router's handler names it.
type BehaviorInfo struct {
	// Database is the DB service whose table the operation runs on, the
	// API's database, and Type the table's type name.
	Database string
	Type     string

	// Use is the use's id, the field the behavior is on; Behavior the
	// use's registered behavior name; Operation the operation's name in
	// the behavior's declaration.
	Use       string
	Behavior  string
	Operation string

	// KeyField is the Args field of the row's key, the route's path
	// parameter.
	KeyField string

	// Writes is the declaration's writes: the operation changes stored
	// state.
	Writes bool

	// Permissions are the permissions the use's config names, which the
	// core checks for the moves that name them.
	Permissions []string
}

// DepsBehaviors is what the API crate runs its behavior operations with,
// which the dispatch layer sets in its Deps (D103): the layout document
// of its database's tables whose fields are behaviors
// (internal/generator/behaviorlayout, indented as the Go ORM's RulesLayout
// holds it), which src/behaviors.rs embeds, and the directories, relative
// to the API crate's, of superschematic-rules-adapter and
// superschematic-rules-behaviors, which its Cargo.toml depends on.
type DepsBehaviors struct {
	Layout        string
	AdapterPath   string
	BehaviorsPath string
}

// behaviorInfo is the behavior operation endpoint runs, nil for an
// authored one. info must hold its path arguments (builder.params): the
// key is one of them.
func behaviorInfo(endpoint apigen.EndpointInfo, info EndpointInfo) (*BehaviorInfo, error) {
	op := endpoint.BehaviorOperation()
	if op == nil {
		return nil, nil
	}
	b := &BehaviorInfo{
		Database:    op.Database,
		Type:        op.Type,
		Use:         op.Use,
		Behavior:    op.Behavior,
		Operation:   op.Operation,
		Writes:      op.Writes,
		Permissions: op.Permissions,
	}
	for _, arg := range info.PathArgs {
		if arg.Name == op.Key {
			b.KeyField = arg.Field
		}
	}
	if b.KeyField == "" {
		return nil, fmt.Errorf("rustrestgen: operation %s.%s runs %s's %s %s (D103), and its route has no path parameter %s, the key of %s", endpoint.Namespace, endpoint.Name, op.Type, op.Use, op.Operation, op.Key, op.Type)
	}
	if len(info.BodyArgs) > 0 || len(info.QueryArgs) > 0 || info.Page != nil {
		return nil, fmt.Errorf("rustrestgen: operation %s.%s runs %s's %s %s (D103), whose arguments are its key and its input alone", endpoint.Namespace, endpoint.Name, op.Type, op.Use, op.Operation)
	}
	return b, nil
}

// HasBehaviorOperations reports whether the router mounts a behavior
// operation (D103): the crate then has src/behaviors.rs, Implementations
// holds the Behaviors its handlers run on, and the crate depends on the
// adapter and the behaviors' crate.
func (o *APIOutput) HasBehaviorOperations() bool {
	for _, endpoint := range o.Endpoints {
		if endpoint.Behavior != nil {
			return true
		}
	}
	return false
}

// ImplementedEndpoints are the mounted operations an implementation
// serves: every one but the behavior operations, which the router hands to
// the behaviors' core (D103).
func (o *APIOutput) ImplementedEndpoints() []EndpointInfo {
	return implemented(o.Endpoints)
}

// implemented is endpoints without the behavior operations.
func implemented(endpoints []EndpointInfo) []EndpointInfo {
	var out []EndpointInfo
	for _, endpoint := range endpoints {
		if endpoint.Behavior == nil {
			out = append(out, endpoint)
		}
	}
	return out
}

// BehaviorDatabase is the DB service the behavior operations run on, the
// API's database; empty without one.
func (o *APIOutput) BehaviorDatabase() string {
	for _, endpoint := range o.Endpoints {
		if endpoint.Behavior != nil {
			return endpoint.Behavior.Database
		}
	}
	return ""
}

// BehaviorTypes are the tables whose behavior operations the router
// mounts, in the order it mounts them, each once.
func (o *APIOutput) BehaviorTypes() []string {
	var types []string
	for _, endpoint := range o.Endpoints {
		if endpoint.Behavior != nil && !slices.Contains(types, endpoint.Behavior.Type) {
			types = append(types, endpoint.Behavior.Type)
		}
	}
	return types
}

// behaviorUse names the first use whose operations the router mounts, as
// a refusal names it: the table, the field and the behavior (Order's
// status, a Workflow, among others when there are more).
func (o *APIOutput) behaviorUse() string {
	var first *BehaviorInfo
	more := false
	for _, endpoint := range o.Endpoints {
		b := endpoint.Behavior
		switch {
		case b == nil:
		case first == nil:
			first = b
		case b.Type != first.Type || b.Use != first.Use:
			more = true
		}
	}
	if first == nil {
		return ""
	}
	use := first.Type + "'s " + first.Use + ", " + behaviortypes.An(first.Behavior)
	if more {
		use += ", among others"
	}
	return use
}

// RulesLayout is the layout document as the Rust raw string literal
// src/behaviors.rs's RULES_LAYOUT is.
func (o *APIOutput) RulesLayout() string {
	if o.Deps == nil || o.Deps.Behaviors == nil {
		return `""`
	}
	return rustutil.RawString(strings.TrimSuffix(o.Deps.Behaviors.Layout, "\n"))
}

// BehaviorUses are src/behaviors.rs's use declarations of the adapter's
// items and the HTTP runtime's, in the order rustfmt sorts them.
func (o *APIOutput) BehaviorUses() []string {
	uses := []string{
		"use " + strings.ReplaceAll(RulesAdapterCrate, "-", "_") + "::{postgres, Caller, Core, Error, Host, Layout, Refusal};",
		"use " + o.RuntimeCrateIdent + "::postgres::Pool;",
		"use " + o.RuntimeCrateIdent + "::{ApiError, RequestContext};",
	}
	slices.Sort(uses)
	return uses
}

// BehaviorCall is the handler's call of the behavior operation the
// endpoint runs, as rustfmt lays it out: behaviors::run_operation with the
// router's Behaviors, the request's context, the table, the row's key from
// its Args, the use, the operation and its input, or, for an operation
// whose parameters declare no property, the empty object the Go and
// TypeScript servers send.
func (e EndpointInfo) BehaviorCall() string {
	b := e.Behavior
	params := "&Value::Object(Default::default())"
	if e.Input != nil {
		params = "&args.input"
	}
	const indent = "    "
	args := []string{
		"&state.implementations.behaviors",
		"&ctx",
		rustString(b.Type),
		"&args." + b.KeyField,
		rustString(b.Use),
		rustString(b.Operation),
		params,
	}
	head := indent + "let result: Result<" + e.OutputRustType + ", ApiError> = behaviors::run_operation("
	return head + "\n" + indent + indent + strings.Join(args, ",\n"+indent+indent) + ",\n" + indent + ")\n" + indent + ".await;"
}

// checkBehaviorDeps refuses Deps that cannot run the crate's behavior
// operations: none, or none naming the database's layout and the crates.
func (o *APIOutput) checkBehaviorDeps(deps *DepsInfo) error {
	if !o.HasBehaviorOperations() {
		return nil
	}
	db := o.BehaviorDatabase()
	if deps == nil || deps.Behaviors == nil {
		return fmt.Errorf("rustrestgen: %s serves the behavior operations of %s (D103), which run through %s and %s on the layout of %s's tables, and its Deps names neither", o.SchemaName, o.behaviorUse(), RulesAdapterCrate, RulesBehaviorsCrate, db)
	}
	if deps.Database != db {
		return fmt.Errorf("rustrestgen: %s serves the behavior operations of %s (D103), on tables of %s, which run on its Deps' pool, and its Deps holds a pool of %q", o.SchemaName, o.behaviorUse(), db, deps.Database)
	}
	if deps.Behaviors.Layout == "" || deps.Behaviors.AdapterPath == "" || deps.Behaviors.BehaviorsPath == "" {
		return fmt.Errorf("rustrestgen: %s serves the behavior operations of %s (D103), and its Deps lacks the layout of %s's tables or the paths of %s and %s", o.SchemaName, o.behaviorUse(), db, RulesAdapterCrate, RulesBehaviorsCrate)
	}
	return nil
}
