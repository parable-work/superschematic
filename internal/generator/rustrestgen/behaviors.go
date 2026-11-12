package rustrestgen

import (
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/behaviortypes"
	"github.com/parable-work/superschematic/internal/generator/rustutil"
	ir "github.com/parable-work/superschematic/ir"
)

// The behavior operations of a Rust API (D103): the operations the loader
// adds for its @behaviorOperations sets, one per operation of each behavior
// a table of its database composes, on a field or by implements. The
// router mounts each as it mounts an authored operation, behind its route's
// controls and with its arguments decoded into its Args struct, and its
// handler runs it through the behaviors' core: src/behaviors.rs hands it to
// superschematic-rules-adapter, with the core of
// superschematic-rules-behaviors and the database's layout document, in a
// transaction of the database's pool. An operation of the table as a whole
// (D104: Pool's pick, Lease's expireHolder), routed with no key, runs as a
// command without a row through the adapter's run_table_operation. The
// namespace traits have no method for it, and the scaffold none. Rust has
// no ORM: the crate calls the adapter itself.
//
// Its behavior workers (@behaviorWorker, D103) run a table's behavior
// queues the same way: src/behaviors.rs opens each queue on a pool of the
// database, as a queue that its handler completes itself, and its handler
// runs each claimed message through the adapter on a transaction of the
// pool, as the worker, with the claim spent in the same commit: a deadline
// of the table's deadline queue through run_deadline, a write of its
// change queue through run_change. The worker's main hands each queue and
// its handler to the HTTP runtime's claim loop; the Workers trait and the
// scaffold have no method for it.

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
	// parameter; empty for an operation of the table as a whole.
	KeyField string

	// Table is true for an operation of the table as a whole (D104), a
	// declaration's scope schema: its route names no row, and its handler
	// runs it as a command without a row (behaviors::run_table_operation).
	Table bool

	// Writes is the declaration's writes: the operation changes stored
	// state.
	Writes bool

	// Permissions are the permissions the use's config names, which the
	// core checks for the moves that name them.
	Permissions []string

	// Preconditions reports whether the operation's input carries the
	// caller's preconditions (D16, D104): on a table one of whose uses'
	// behaviors takes one, the loader gives every behavior operation's
	// input the member PreconditionsMember, of the table's
	// <Table>Preconditions type, each use's entry by its use id. The
	// handler takes it out of the input's JSON and hands it to the core
	// with the command, as the adapter's run_operation_with takes it.
	Preconditions bool
}

// PreconditionsMember is the member of a behavior operation's input that
// carries the caller's preconditions (D16, D104), a body member rather
// than a header, as every server and SDK reads it: the loader adds it to
// every behavior operation's input on a table one of whose uses' behaviors
// takes a precondition, typed <Table>Preconditions, and refuses a
// parameter of the name there.
const PreconditionsMember = "preconditions"

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
		Table:       op.Table,
		Permissions: op.Permissions,
	}
	if op.Table {
		// A command without a row (D104): its route names none, and its
		// input presents no preconditions, a row's.
		if len(info.PathArgs) > 0 || len(info.BodyArgs) > 0 || len(info.QueryArgs) > 0 || info.Page != nil {
			return nil, fmt.Errorf("rustrestgen: operation %s.%s runs %s's %s %s on the table as a whole (D104), whose argument is its input alone", endpoint.Namespace, endpoint.Name, op.Type, op.Use, op.Operation)
		}
		return b, nil
	}
	for _, arg := range info.PathArgs {
		if arg.Name == op.Key {
			b.KeyField = arg.Field
		}
	}
	for _, field := range endpoint.InputTypeFields {
		if field.Name == PreconditionsMember && field.Type == op.Type+"Preconditions" {
			b.Preconditions = true
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

// HasTableBehaviorOperations reports whether the router mounts a behavior
// operation of a table as a whole (D104): src/behaviors.rs then has
// run_table_operation, which that operation's handler calls.
func (o *APIOutput) HasTableBehaviorOperations() bool {
	for _, endpoint := range o.Endpoints {
		if endpoint.Behavior != nil && endpoint.Behavior.Table {
			return true
		}
	}
	return false
}

// HasBehaviorPreconditions reports whether a behavior operation the router
// mounts takes the caller's preconditions in its input (D16, D104):
// src/behaviors.rs then has run_operation_with_preconditions, which that
// operation's handler calls.
func (o *APIOutput) HasBehaviorPreconditions() bool {
	for _, endpoint := range o.Endpoints {
		if endpoint.Behavior != nil && endpoint.Behavior.Preconditions {
			return true
		}
	}
	return false
}

// HasBehaviors reports whether the crate runs anything through the
// behaviors' core (D103), a behavior operation or a behavior worker: it
// then has src/behaviors.rs and depends on the adapter and the behaviors'
// crate.
func (o *APIOutput) HasBehaviors() bool {
	return o.HasBehaviorOperations() || len(o.BehaviorWorkers) > 0
}

// ImplementedEndpoints are the mounted operations an implementation
// serves: every one but the behavior operations, which the router hands to
// the behaviors' core (D103).
func (o *APIOutput) ImplementedEndpoints() []EndpointInfo {
	return implemented(o.Endpoints)
}

// implemented is endpoints without the behavior operations and the table
// operations (D106), which the router serves itself.
func implemented(endpoints []EndpointInfo) []EndpointInfo {
	var out []EndpointInfo
	for _, endpoint := range endpoints {
		if !endpoint.RouterServed() {
			out = append(out, endpoint)
		}
	}
	return out
}

// BehaviorDatabase is the DB service the behavior operations and the
// behavior workers run on, the API's database; empty without one.
func (o *APIOutput) BehaviorDatabase() string {
	for _, endpoint := range o.Endpoints {
		if endpoint.Behavior != nil {
			return endpoint.Behavior.Database
		}
	}
	if len(o.BehaviorWorkers) > 0 && o.Deps != nil {
		return o.Deps.Database
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
// a refusal names it: the table, the field or the behavior it implements,
// and the behavior (Order's status, a Workflow; Ticket's Timers, which it
// implements), among others when there are more. Without operations it
// names the first behavior worker and its table.
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
		if len(o.BehaviorWorkers) == 0 {
			return ""
		}
		w := o.BehaviorWorkers[0]
		return w.Behavior.Table + "'s behaviors, which its @behaviorWorker " + w.Name + " runs"
	}
	use := first.Type + "'s " + first.Use + ", " + behaviortypes.An(first.Behavior)
	if first.Use == first.Behavior {
		// A use the table implements is the behavior's own (D103).
		use = first.Type + "'s " + first.Behavior + ", which it implements"
	}
	if more {
		use += ", among others"
	}
	return use
}

// behaviorWork says what the crate runs through the behaviors' core, as a
// refusal says it: the behavior operations of a use, or a behavior
// worker's table.
func (o *APIOutput) behaviorWork() string {
	if o.HasBehaviorOperations() {
		return "serves the behavior operations of " + o.behaviorUse()
	}
	return "runs " + o.behaviorUse()
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
// items, the HTTP runtime's and, for its behavior workers, the database's
// Rust types', in the order rustfmt sorts them: what its behavior
// operations name, and what its behavior workers name.
func (o *APIOutput) BehaviorUses() []string {
	adapter := []string{"postgres", "Caller", "Core", "Host", "Layout"}
	var runtime, types []string
	if o.HasBehaviorOperations() {
		adapter = append(adapter, "Error", "Refusal")
		runtime = append(runtime, "ApiError", "RequestContext")
	}
	if o.HasBehaviorPreconditions() || o.HasTableBehaviorOperations() {
		adapter = append(adapter, "ExecuteOptions")
	}
	if len(o.BehaviorWorkers) > 0 {
		adapter = append(adapter, "Claim")
		if o.HasDeadlineQueues() {
			adapter = append(adapter, "Deadline", "DeadlineRun")
		}
		if o.HasChangeQueues() {
			adapter = append(adapter, "TableChange")
		}
		// EventRun's operation answers the adapter's Error (D104).
		if o.EmitsEvents() && !o.HasBehaviorOperations() {
			adapter = append(adapter, "Error", "Refusal")
		}
		runtime = append(runtime, "BoxError")
		for _, w := range o.BehaviorWorkers {
			for _, q := range w.BehaviorQueues {
				types = append(types, q.Queue, q.Const)
			}
		}
	}
	pool := "use " + o.RuntimeCrateIdent + "::postgres::Pool;"
	if o.EmitsEvents() {
		// EventRun holds a transaction of the pool (D104).
		pool = rustutil.UseLine(o.RuntimeCrateIdent+"::postgres", []string{"tokio_postgres", "Pool"})
	}
	uses := []string{
		rustutil.UseLine(strings.ReplaceAll(RulesAdapterCrate, "-", "_"), adapter),
		pool,
	}
	if len(o.BehaviorWorkers) > 0 {
		uses = append(uses,
			"use "+o.RuntimeCrateIdent+"::queue::{PostgresQueue, QueueDefinition};",
			"use "+o.RuntimeCrateIdent+"::worker::{Claimed, SelfCompleting};",
		)
	}
	if len(runtime) > 0 {
		uses = append(uses, rustutil.UseLine(o.RuntimeCrateIdent, runtime))
	}
	slices.Sort(uses)
	if len(types) > 0 {
		// The crate's own, in a group of their own after the crates', the
		// types before the constants, as rustfmt orders them.
		slices.SortFunc(types, func(a, b string) int {
			if ca, cb := a == strings.ToUpper(a), b == strings.ToUpper(b); ca != cb {
				if ca {
					return 1
				}
				return -1
			}
			return strings.Compare(a, b)
		})
		uses = append(uses, "\n"+bracedUse("crate::"+DatabaseTypesModule, types))
	}
	return uses
}

// bracedUse is the use declaration of items of path in their order, as
// rustfmt lays it out: one item bare; a braced list on the use's line up to
// 98 columns; else each row of items, indented, up to 100 columns, on lines
// of their own.
func bracedUse(path string, items []string) string {
	if len(items) == 1 {
		return "use " + path + "::" + items[0] + ";"
	}
	if line := "use " + path + "::{" + strings.Join(items, ", ") + "};"; len(line) <= 98 {
		return line
	}
	var b strings.Builder
	b.WriteString("use " + path + "::{\n")
	row := ""
	for _, item := range items {
		next := item + ","
		if row != "" && 4+len(row)+1+len(next) > 100 {
			b.WriteString("    " + row + "\n")
			row = ""
		}
		if row != "" {
			row += " "
		}
		row += next
	}
	b.WriteString("    " + row + "\n};")
	return b.String()
}

// BehaviorWorkerCaller is the body of a behavior worker's caller function:
// the worker's subject and the permissions its decorator grants (D103).
func (w WorkerInfo) BehaviorWorkerCaller() string {
	permissions := "Vec::<String>::new()"
	if len(w.Behavior.Permissions) > 0 {
		quoted := make([]string, len(w.Behavior.Permissions))
		for i, p := range w.Behavior.Permissions {
			quoted[i] = rustString(p)
		}
		permissions = "[" + strings.Join(quoted, ", ") + "]"
	}
	return rustCall("    ", "Caller::new", []string{rustString(w.Behavior.Subject), permissions}, "")
}

// RunDeadlineCall is the handler's call of the adapter's run_deadline for
// the worker's table, as rustfmt lays it out.
func (w WorkerInfo) RunDeadlineCall() string {
	args := []string{"&host", "&*tx", rustString(w.Behavior.Table), "&deadline", w.CallerFunc() + "()", "Some(&claim)"}
	return rustCall("    ", "let run = postgres::run_deadline", args, ".await?;")
}

// RunChangeCall is the handler's call of the adapter's run_change for the
// worker's table, as rustfmt lays it out.
func (w WorkerInfo) RunChangeCall() string {
	args := []string{"&host", "&*tx", rustString(w.Behavior.Table), "&change", w.CallerFunc() + "()", "Some(&claim)"}
	return rustCall("    ", "let run = postgres::run_change", args, ".await?;")
}

// ModuleDoc is the paragraph of src/behaviors.rs's module doc that says
// what the behavior worker runs and how (D103), its lines of "//! " text
// wrapped at 80 columns: the table's deadlines and the writes of its rows
// its behaviors hear, each queue's function and handler, which its main
// hands to the HTTP runtime's claim loop.
func (w WorkerInfo) ModuleDoc(schemaName, database string) string {
	var runs, queues, handlers []string
	for _, q := range w.BehaviorQueues {
		queues = append(queues, q.Queue)
		handlers = append(handlers, "`"+q.Func+"` with `"+q.Handler+"`")
		switch {
		case q.Changes():
			runs = append(runs, "the writes of its rows "+w.hearers()+" hear")
		case q.Events():
			runs = append(runs, "the events "+w.emitters()+" about its rows")
		default:
			runs = append(runs, "its deadlines")
		}
	}
	queue := "queue " + queues[0]
	if len(queues) > 1 {
		queue = "queues " + andList(queues)
	}
	handler := "the handler"
	if len(handlers) > 1 {
		handler = "the handlers"
	}
	if w.Behavior.EventQueue != "" {
		// An emitting table's events reach the project's Workers (D104).
		text := fmt.Sprintf("The behavior worker %s of %s (D103, D104): %s's behavior queues, %s, each message claimed from %s's %s as it falls due and run on a transaction of the pool, as the worker, its message completed in the same commit: a deadline or a change through the behaviors' core by superschematic-rules-adapter, and an event handed to the Workers method of its use and kind, %s, which the implementation writes. Its main runs %s as %s in the HTTP runtime's claim loop.",
			w.Name, schemaName, w.Behavior.Table, andList(runs), database, queue, andList(w.eventMethods()), andList(handlers), handler)
		return docLines("//!", text, 80)
	}
	text := fmt.Sprintf("The behavior worker %s of %s (D103): %s's behavior queues, %s, each message claimed from %s's %s as it falls due and run through the behaviors' core by superschematic-rules-adapter on a transaction of the pool, as the worker, its message completed in the same commit. Its main runs %s as %s in the HTTP runtime's claim loop; no implementation method serves it.",
		w.Name, schemaName, w.Behavior.Table, andList(runs), database, queue, andList(handlers), handler)
	return docLines("//!", text, 80)
}

// eventMethods are the Workers methods the worker's events go to, each in
// backticks (D104).
func (w WorkerInfo) eventMethods() []string {
	var methods []string
	for _, h := range w.EventHandlers() {
		methods = append(methods, "`"+h.Fn+"`")
	}
	return methods
}

// emitters names whose behaviors emit the events of the worker's table,
// with the verb (D104): its uses' behaviors that declare events, each once
// ("Schedules emits", "Schedules and Pool emit").
func (w WorkerInfo) emitters() string {
	var behaviors []string
	for _, h := range w.EventHandlers() {
		if !slices.Contains(behaviors, h.Behavior) {
			behaviors = append(behaviors, h.Behavior)
		}
	}
	switch len(behaviors) {
	case 0:
		return "its behaviors emit"
	case 1:
		return behaviors[0] + " emits"
	}
	return andList(behaviors) + " emit"
}

// hearers names whose behaviors hear the writes of the worker's table:
// its own ("its behaviors"), the tables' that listen through a relation of
// it ("Project's behaviors"), or both ("its behaviors and Project's").
func (w WorkerInfo) hearers() string {
	var listening []string
	for _, table := range w.Behavior.HeardBy {
		listening = append(listening, table+"'s")
	}
	switch {
	case len(listening) == 0:
		return "its behaviors"
	case len(w.Behavior.Hearing) == 0:
		return andList(listening) + " behaviors"
	}
	return "its behaviors and " + andList(listening)
}

// andList joins words as a sentence lists them: "a", "a and b", "a, b and
// c".
func andList(words []string) string {
	if len(words) < 2 {
		return strings.Join(words, "")
	}
	return strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
}

// docLines wraps text into lines of prefix and words, each at most width
// columns unless one word is longer.
func docLines(prefix, text string, width int) string {
	var lines []string
	line := prefix
	for _, word := range strings.Fields(text) {
		if len(line)+1+len(word) > width && line != prefix {
			lines = append(lines, line)
			line = prefix
		}
		line += " " + word
	}
	return strings.Join(append(lines, line), "\n")
}

// HasDeadlineQueues reports whether a behavior worker runs a table's
// deadline queue, whose messages src/behaviors.rs reads with deadline_of.
func (o *APIOutput) HasDeadlineQueues() bool {
	return o.hasQueueOfKind(ir.BehaviorQueueDeadlines)
}

// HasChangeQueues reports whether a behavior worker runs a table's change
// queue, whose messages src/behaviors.rs reads with change_of (D103).
func (o *APIOutput) HasChangeQueues() bool {
	return o.hasQueueOfKind(ir.BehaviorQueueChanges)
}

// FuncSignature is the signature of the function that opens the queue,
// through its opening brace.
func (q BehaviorQueue) FuncSignature() string {
	return rustFn("", "pub fn "+q.Func, []string{"pool: &Pool"}, "Result<SelfCompleting<PostgresQueue<"+q.Queue+">>, BoxError>")
}

// HandlerSignature is the signature of the function that runs a message
// claimed from the queue, through its opening brace.
func (q BehaviorQueue) HandlerSignature() string {
	return rustFn("", "pub async fn "+q.Handler, []string{"pool: &Pool", "claimed: Claimed<" + q.Queue + ">"}, "Result<(), BoxError>")
}

// rustCall is a call of fn with args, indented by indent, as rustfmt lays
// it out: on one line when its arguments take 60 columns at most
// (fn_call_width) and the line 100, each argument on a line of its own
// otherwise. A tail that is a method call (".await?;") follows on a line
// of its own after a call laid out so, and on the call's line after one
// that fits; any other tail follows the call.
func rustCall(indent, fn string, args []string, tail string) string {
	joined := strings.Join(args, ", ")
	line := indent + fn + "(" + joined + ")" + tail
	if len(joined) <= 60 && len(line) <= 100 {
		return line
	}
	var b strings.Builder
	b.WriteString(indent + fn + "(\n")
	for _, arg := range args {
		b.WriteString(indent + "    " + arg + ",\n")
	}
	b.WriteString(indent + ")")
	if strings.HasPrefix(tail, ".") {
		b.WriteString("\n" + indent + tail)
	} else {
		b.WriteString(tail)
	}
	return b.String()
}

// BehaviorCall is the handler's call of the behavior operation the
// endpoint runs, as rustfmt lays it out: behaviors::run_operation with the
// router's Behaviors, the request's context, the table, the row's key from
// its Args, the use, the operation and its input, or, for an operation
// whose parameters declare no property, the empty object the Go and
// TypeScript servers send. An operation whose input carries the caller's
// preconditions calls behaviors::run_operation_with_preconditions, which
// takes them out of it (D16, D104); one of the table as a whole calls
// behaviors::run_table_operation, with no key (D104).
func (e EndpointInfo) BehaviorCall() string {
	b := e.Behavior
	params := "&Value::Object(Default::default())"
	if e.Input != nil {
		params = "&args.input"
	}
	run := "run_operation"
	if b.Preconditions {
		run = "run_operation_with_preconditions"
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
	if b.Table {
		run = "run_table_operation"
		args = slices.Delete(args, 3, 4)
	}
	head := indent + "let result: Result<" + e.OutputRustType + ", ApiError> = behaviors::" + run + "("
	return head + "\n" + indent + indent + strings.Join(args, ",\n"+indent+indent) + ",\n" + indent + ")\n" + indent + ".await;"
}

// checkBehaviorDeps refuses Deps that cannot run the crate's behavior
// operations or its behavior workers: none, or none naming the database's
// layout and the crates.
func (o *APIOutput) checkBehaviorDeps(deps *DepsInfo) error {
	if !o.HasBehaviors() {
		return nil
	}
	db := o.BehaviorDatabase()
	if db == "" && deps != nil {
		db = deps.Database
	}
	if deps == nil || deps.Behaviors == nil {
		return fmt.Errorf("rustrestgen: %s %s (D103), which run through %s and %s on the layout of %s's tables, and its Deps names neither", o.SchemaName, o.behaviorWork(), RulesAdapterCrate, RulesBehaviorsCrate, db)
	}
	if deps.Database != db {
		return fmt.Errorf("rustrestgen: %s %s (D103), on tables of %s, which run on its Deps' pool, and its Deps holds a pool of %q", o.SchemaName, o.behaviorWork(), db, deps.Database)
	}
	if deps.Behaviors.Layout == "" || deps.Behaviors.AdapterPath == "" || deps.Behaviors.BehaviorsPath == "" {
		return fmt.Errorf("rustrestgen: %s %s (D103), and its Deps lacks the layout of %s's tables or the paths of %s and %s", o.SchemaName, o.behaviorWork(), db, RulesAdapterCrate, RulesBehaviorsCrate)
	}
	return nil
}
