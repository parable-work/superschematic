package rustrestgen

import (
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/rustgen"
	"github.com/parable-work/superschematic/internal/generator/rustutil"
	ir "github.com/parable-work/superschematic/ir"
)

// The runtime parts of an API's database that a generated Rust server
// builds itself (#620, #622, #617, #610): the notifier (D191) with its
// inbox and relay routes, the agent dispatcher (D193) with its routes and
// its MCP endpoint's call gate, the inbound path (D193), the calendar and
// search routes (D188), and the grants' authenticator and the access log
// (D181). src/wiring.rs builds them over the API's database, in the order
// they take each other, and hands each to the implementation's Wire, whose
// methods' defaults are the generated wiring, so an app changes or adds to
// a part where it needs to. Deps holds the notifier, the dispatcher and the
// inbound path, a server mounts their routes, and a @worker of a queue the
// runtime handles runs through them with no method of the Workers trait:
// the parts' queues, a @searchable declaration's, and an @audience
// declaration's (#734), whose worker resolves a changed parent's children.
// On Postgres, over the pool, or on the database's SQLite file when its DB
// service lists sqlite first (D116, #725): the parts are built over the
// file's Tables, the grants' authenticator and the access log are
// access::sqlite's, the calendar's and the search's routes and tools are
// their sqlite modules', and each queue is claimed on the file. An
// @audience queue is Postgres's alone (the loader refuses it on SQLite).

// WiringFile is the module of the API crate that builds the runtime parts.
const WiringFile = "wiring.rs"

// WiringFunc is the function an implementation of an API whose database
// has runtime parts declares (#620): the Wire that shapes them.
const WiringFunc = "create_wiring"

// The runtime a RuntimeWorker's queue is handled by.
const (
	RuntimeNotifier = "notifier"
	RuntimeAgents   = "agents"
	RuntimeRuns     = "runs"
	RuntimeSearch   = "search"
	RuntimeAudience = "audience"
)

// DepsWiring is what src/wiring.rs builds over the API's database.
type DepsWiring struct {
	// SQLite is whether the database's DB service lists sqlite first
	// (D116): the parts are then built over its SQLite file (#725).
	SQLite bool

	// Dispatch is the notifier's @notifier("dispatch") queue class, empty
	// for a database without the notifier, and DispatchConst the constant
	// its descriptor is in the database's Rust types.
	Dispatch      string
	DispatchConst string

	// Requests is the agent dispatcher's @agents("requests") queue, empty
	// without the orchestration.
	Requests      string
	RequestsConst string

	// RunRequests is the @runRequests queue (D157), which the inbound path
	// enqueues and the dispatcher takes, empty without one.
	RunRequests      string
	RunRequestsConst string

	// Inbound is whether the database holds the inbound path's tables:
	// the conversations, the messages and the channel identities, with
	// the run requests queue and the notifier it is built over.
	Inbound bool

	// Search are the queues of the database's @searchable declarations,
	// sorted (D155), each a SearchQueue.
	Search []SearchQueue

	// Audience are the queues of the database's @audience declarations
	// (D156, #734), sorted, whose worker resolves a changed parent's
	// children again a batch at a time (orm::resolve_audience).
	Audience []SearchQueue

	// Calendar is whether a table is a @calendarSource (D152).
	Calendar bool

	// Grants is whether the database holds @accessGrants and the UserRole
	// table its roles are rows of (D156), and AccessLog whether it holds
	// an @accessLog table.
	Grants    bool
	AccessLog bool
}

// SearchQueue is a @searchable declaration's queue, or an @audience
// declaration's: its class and its descriptor's constant.
type SearchQueue struct {
	Queue string
	Const string
}

// Notifier is whether the database has the notifier.
func (w *DepsWiring) Notifier() bool { return w != nil && w.Dispatch != "" }

// Agents is whether the database has the agent orchestration.
func (w *DepsWiring) Agents() bool { return w != nil && w.Requests != "" }

// wiringOf is the wiring of db, nil for a database with none of its parts;
// sqlite is whether its DB service lists sqlite first (#725).
func wiringOf(db *ir.Schema, sqlite bool) *DepsWiring {
	if db == nil {
		return nil
	}
	w := &DepsWiring{SQLite: sqlite}
	if t := db.NotifierTable(ir.NotifierDispatch); t != nil {
		w.Dispatch, w.DispatchConst = t.Name, rustgen.QueueConst(t.Name)
	}
	if t := db.AgentsTable(ir.AgentRequests); t != nil {
		w.Requests, w.RequestsConst = t.Name, rustgen.QueueConst(t.Name)
	}
	if t := db.RunRequestsQueue(); t != nil {
		w.RunRequests, w.RunRequestsConst = t.Name, rustgen.QueueConst(t.Name)
	}
	w.Inbound = w.Notifier() && w.RunRequests != "" && db.ConversationsTable(ir.ConversationsPart) != nil && db.ConversationsTable("messages") != nil && db.ChannelIdentitiesTable() != nil
	seen := map[string]bool{}
	for _, td := range db.SearchableTables() {
		if q := td.Searchable.Queue; q != "" && !seen[q] {
			seen[q] = true
			w.Search = append(w.Search, SearchQueue{Queue: q, Const: rustgen.QueueConst(q)})
		}
	}
	slices.SortFunc(w.Search, func(a, b SearchQueue) int { return strings.Compare(a.Queue, b.Queue) })
	for _, td := range db.AudienceTables() {
		if q := td.Audience.Queue; q != "" && !slices.ContainsFunc(w.Audience, func(a SearchQueue) bool { return a.Queue == q }) {
			w.Audience = append(w.Audience, SearchQueue{Queue: q, Const: rustgen.QueueConst(q)})
		}
	}
	slices.SortFunc(w.Audience, func(a, b SearchQueue) int { return strings.Compare(a.Queue, b.Queue) })
	for _, td := range db.Types {
		if td != nil && len(td.CalendarSources) > 0 {
			w.Calendar = true
		}
	}
	w.Grants = db.AccessGrantsTable() != nil && db.UserRoleTable() != nil
	w.AccessLog = db.AccessLogTable() != nil
	if !w.Notifier() && !w.Agents() && len(w.Search) == 0 && len(w.Audience) == 0 && !w.Calendar && !w.Grants && !w.AccessLog {
		return nil
	}
	return w
}

// HasRuntimeParts reports whether a Rust API over db builds its runtime
// parts, on either dialect.
func HasRuntimeParts(db *ir.Schema) bool { return wiringOf(db, false) != nil }

// RuntimeOf is the runtime that handles a message of queue, empty for a
// queue an implementation's Workers handle. A @runRequests queue is the
// dispatcher's when the database has the orchestration, and the
// implementation's otherwise.
func (w *DepsWiring) RuntimeOf(queue string) string {
	switch {
	case w == nil || queue == "":
		return ""
	case queue == w.Dispatch:
		return RuntimeNotifier
	case queue == w.Requests:
		return RuntimeAgents
	case queue == w.RunRequests && w.Agents() && w.Inbound:
		return RuntimeRuns
	case slices.ContainsFunc(w.Search, func(q SearchQueue) bool { return q.Queue == queue }):
		return RuntimeSearch
	case slices.ContainsFunc(w.Audience, func(q SearchQueue) bool { return q.Queue == queue }):
		return RuntimeAudience
	}
	return ""
}

// RuntimeQueues are the queues of db whose @worker the runtime handles
// (#620, #622, #617, #734): what a build's check of an implementation's
// Workers leaves out, on either dialect.
func RuntimeQueues(db *ir.Schema) map[string]bool {
	out := map[string]bool{}
	w := wiringOf(db, false)
	for _, q := range []string{w.dispatchOf(), w.requestsOf(), w.runRequestsOf()} {
		if q != "" && w.RuntimeOf(q) != "" {
			out[q] = true
		}
	}
	if w != nil {
		for _, q := range append(slices.Clone(w.Search), w.Audience...) {
			out[q.Queue] = true
		}
	}
	return out
}

func (w *DepsWiring) dispatchOf() string {
	if w == nil {
		return ""
	}
	return w.Dispatch
}

func (w *DepsWiring) requestsOf() string {
	if w == nil {
		return ""
	}
	return w.Requests
}

func (w *DepsWiring) runRequestsOf() string {
	if w == nil {
		return ""
	}
	return w.RunRequests
}

// HasWiring reports whether the crate writes src/wiring.rs: its Deps'
// database has a runtime part.
func (o *APIOutput) HasWiring() bool { return o.Deps != nil && o.Deps.Wiring != nil }

// Wiring is the crate's wiring, nil without one.
func (o *APIOutput) Wiring() *DepsWiring {
	if o.Deps == nil {
		return nil
	}
	return o.Deps.Wiring
}

// WiringAuthenticates reports whether src/wiring.rs wraps the
// implementation's authenticator: in the grants' authenticator, the
// access log, or both (#610). An API over the user model (D50) keeps the
// identity runtime's.
func (o *APIOutput) WiringAuthenticates() bool {
	w := o.Wiring()
	return w != nil && o.ChecksEndUsers() && (w.Grants || w.AccessLog)
}

// RoutesAuthenticator is the expression of Implementations' authenticator
// the wiring's routes establish their callers with (Wired::routes): the
// implementation's, wrapped, or the identity runtime's; None for an API no
// route of which needs a caller.
func (o *APIOutput) RoutesAuthenticator() string {
	switch {
	case o.Identity != nil:
		return "Some(implementations.authenticator.clone() as Arc<dyn Authenticator>)"
	case o.HasAuth:
		return "Some(implementations.authenticator.clone())"
	}
	return "None"
}

// setWiring reads the wiring of deps' database, on Postgres or on its
// SQLite file (#725), into deps, and moves the @worker classes of the
// queues the runtime handles out of Workers into RuntimeWorkers.
func (o *APIOutput) setWiring(deps *DepsInfo) error {
	deps.Wiring = nil
	workers := append(slices.Clone(o.Workers), o.RuntimeWorkers...)
	slices.SortFunc(workers, func(a, b WorkerInfo) int { return strings.Compare(a.Name, b.Name) })
	o.Workers, o.RuntimeWorkers = nil, nil
	if deps.Database != "" {
		deps.Wiring = wiringOf(o.databases[deps.Database], deps.SQLite)
	}
	for _, w := range workers {
		if runtime := deps.Wiring.RuntimeOf(w.Queue); runtime != "" {
			w.Runtime = runtime
			o.RuntimeWorkers = append(o.RuntimeWorkers, w)
			continue
		}
		w.Runtime = ""
		o.Workers = append(o.Workers, w)
	}
	if deps.Wiring != nil && deps.DatabaseTypesCrate == "" {
		return fmt.Errorf("rustrestgen: %s's server builds the runtime parts of %s (#620, #622), over its Rust types' repositories and queues, and its Deps names none of them", o.SchemaName, deps.Database)
	}
	return nil
}

// RunFunc is the method of Wired that runs a RuntimeWorker's queue:
// run_notice_work.
func (w WorkerInfo) RunFunc() string {
	return "run_" + rustutil.ToSnakeCase(w.Queue)
}

// WiringStruct is the struct the scaffold implements Wire with: ShopWiring.
func (s ImplementationScaffold) WiringStruct() string {
	return rustutil.ToPascalCase(s.SchemaName) + "Wiring"
}

// RunsRequests reports whether src/wiring.rs runs the run requests queue
// through the agent dispatcher.
func (w *DepsWiring) RunsRequests() bool { return w.RuntimeOf(w.runRequestsOf()) == RuntimeRuns }

// RunsRuntime reports whether a RuntimeWorker of the API runs through the
// part runtime names, so src/wiring.rs names what its run takes.
func (o *APIOutput) RunsRuntime(runtime string) bool {
	return slices.ContainsFunc(o.RuntimeWorkers, func(w WorkerInfo) bool { return w.Runtime == runtime })
}

// WiringQueueUse is src/wiring.rs's use of the runtime's queue module:
// the descriptors its parts and its search and audience workers parse, the
// error its workers' runs fail with, and the queue a search or an audience
// worker claims from on Postgres (on SQLite, a PartQueue over the file).
func (o *APIOutput) WiringQueueUse() string {
	w := o.Wiring()
	var names []string
	claims := o.RunsRuntime(RuntimeSearch) || o.RunsRuntime(RuntimeAudience)
	if claims && !w.SQLite {
		names = append(names, "PostgresQueue")
	}
	if w.Agents() || w.Notifier() || claims {
		names = append(names, "QueueDefinition")
	}
	if len(o.RuntimeWorkers) > 0 {
		names = append(names, "QueueError")
	}
	if len(names) == 0 {
		return ""
	}
	return rustutil.UseLine("crate::runtime::queue", names)
}

// WiringStoreUse is src/wiring.rs's use of the runtime's orm::store on a
// SQLite file: the PartQueue a search worker claims on the file (#725).
func (o *APIOutput) WiringStoreUse() string {
	if w := o.Wiring(); w == nil || !w.SQLite || !o.RunsRuntime(RuntimeSearch) {
		return ""
	}
	return "use crate::runtime::orm::store::{PartQueue, Store};"
}
