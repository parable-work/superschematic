package apigen

import (
	"slices"
	"sort"

	"github.com/parable-work/superschematic/internal/generator/goutil"
	ir "github.com/parable-work/superschematic/ir"
)

// The runtime wiring of a Go server (D190, D192, D187, D180): an API whose
// database hosts the notifier, has a calendar source or a @searchable
// table, or keeps grants, gets wiring.go. Its Wire builds Deps.Notifier,
// Deps.Agents and Deps.Inbound over the database's ORM in D192's order,
// with the options the implementation's NewWiring answers; Authenticator
// is the API's auth step with the grants' and an agent run's tokens before
// it; Routes are the inbox, the relays' callbacks, the agent routes, the
// calendar and search; and each @worker of a queue the runtime handles has
// its queue built here, its handler the runtime's, so the Workers
// interface has no method for it. The entrypoints of a stack's servers,
// jobs and workers call them (servergen).

// WiringFunc is the function an implementation of an API whose Deps holds
// the wiring's options declares, of the generated WiringConstructor's
// signature.
const WiringFunc = "NewWiring"

// The runtime handlers of a @worker's queue (WorkerInfo.Runtime).
const (
	// RuntimeNotifier is the notifier's @notifier("dispatch") queue,
	// handled by notify.Notifier.WorkerQueue (D190).
	RuntimeNotifier = "notifier"
	// RuntimeAgents is the dispatcher's @agents("requests") queue, handled
	// by agents.Agents.WorkerQueue (D192).
	RuntimeAgents = "agents"
	// RuntimeRunRequests is D157's @runRequests queue, whose run requests
	// the dispatcher takes as they are (inbound.Inbound.RunQueue).
	RuntimeRunRequests = "runRequests"
	// RuntimeSearch is a @searchable declaration's queue, each message
	// handed to crosstable.IndexSearch (D187).
	RuntimeSearch = "search"
	// RuntimeAudience is an @audience declaration's queue (D156, #734),
	// each message handed to the ORM's ResolveAudience.
	RuntimeAudience = "audience"
)

// DepsWiring is what the runtime wiring of the API's database holds.
type DepsWiring struct {
	// Notifier is the database's @notifier("dispatch") queue, which Deps
	// holds the notifier of (Database.Notifications()); empty for a
	// database that hosts no notifier.
	Notifier string

	// Agents is the database's @agents("requests") queue, which Deps holds
	// the dispatcher of (Database.Orchestration()); empty for none.
	Agents string

	// Inbound reports whether Deps holds D157's inbound path: the database
	// has the conversations' tables and channel identities, and hosts the
	// notifier.
	Inbound bool

	// RunRequests is the database's @runRequests queue, which the
	// dispatcher takes through the inbound path; empty unless the database
	// has both.
	RunRequests string

	// Calendar and Search report whether the database has a calendar
	// source or a @searchable table (Database.CrossTables()); SearchQueues
	// are the @searchable declarations' queues, sorted.
	Calendar     bool
	Search       bool
	SearchQueues []string

	// AudienceQueues are the @audience declarations' queues (#734),
	// sorted: a @worker of one resolves each message's rows through the
	// ORM's ResolveAudience. The loader refuses a queue on a database
	// that lists sqlite, so these are a Postgres database's.
	AudienceQueues []string

	// Grants reports whether the database keeps @accessGrants, whose
	// tokens Authenticator establishes (session.GrantAuthenticator), on
	// Postgres or on SQLite (#703's AuthenticateGrant).
	Grants bool

	// Files reports whether wiring.go holds ProcessPendingFiles, the file
	// processor's pass (#603): the database declares @attachments and the
	// API fills a files slot (WithFiles).
	Files bool

	// SQLite reports whether the database's DB service lists sqlite first
	// (#717): Wire, Authenticator, Routes and the queues take the ORM's
	// SQLiteDatabase, which the entrypoints open over the SQLite file, and
	// a @searchable queue claims there (New<Queue>SQLiteQueue).
	SQLite bool
}

// WiringOf is the runtime wiring of db, the API's database, on SQLite
// when sqlite: nil for a database with none of it. The runtime is the
// same on either (#618, #717); only the ORM's type it takes differs.
func WiringOf(db *ir.Schema, sqlite bool) *DepsWiring {
	if db == nil {
		return nil
	}
	w := &DepsWiring{SQLite: sqlite}
	if db.UsesNotifications() {
		if dispatch := db.NotifierTable(ir.NotifierDispatch); dispatch != nil {
			w.Notifier = dispatch.Name
		}
	}
	if w.Notifier != "" {
		if requests := db.AgentsTable(ir.AgentRequests); requests != nil {
			w.Agents = requests.Name
		}
		w.Inbound = db.ConversationsTable(ir.ConversationsPart) != nil && db.ConversationsTable(ir.MessagesPart) != nil && db.ChannelIdentitiesTable() != nil
		if runs := db.RunRequestsQueue(); runs != nil && w.Agents != "" && w.Inbound {
			w.RunRequests = runs.Name
		}
	}
	for _, td := range db.Types {
		if td == nil {
			continue
		}
		if len(td.CalendarSources) > 0 {
			w.Calendar = true
		}
		if td.Searchable != nil {
			w.Search = true
			if q := td.Searchable.Queue; q != "" && !slices.Contains(w.SearchQueues, q) {
				w.SearchQueues = append(w.SearchQueues, q)
			}
		}
	}
	sort.Strings(w.SearchQueues)
	for _, td := range db.AudienceTables() {
		if q := td.Audience.Queue; q != "" && !slices.Contains(w.AudienceQueues, q) {
			w.AudienceQueues = append(w.AudienceQueues, q)
		}
	}
	sort.Strings(w.AudienceQueues)
	w.Grants = db.AccessGrantsTable() != nil
	if !w.Builds() && !w.Calendar && !w.Search && !w.Grants && len(w.AudienceQueues) == 0 {
		return nil
	}
	return w
}

// Builds reports whether Wire builds anything into Deps: the notifier,
// and the dispatcher and the inbound path with it.
func (w *DepsWiring) Builds() bool { return w != nil && w.Notifier != "" }

// Constructs reports whether the entrypoint asks the implementation's
// NewWiring for the wiring's options: Deps holds the notifier, or the
// database has a calendar or search. Grants alone take none.
func (w *DepsWiring) Constructs() bool {
	return w != nil && (w.Builds() || w.Calendar || w.Search)
}

// Authenticates reports whether Authenticator adds a step to the API's
// own: a grant's token, or a run's.
func (w *DepsWiring) Authenticates() bool { return w != nil && (w.Grants || w.Agents != "") }

// Serves reports whether Routes serves a route: the inbox and the relays'
// callbacks, the agent routes, the calendar or search.
func (w *DepsWiring) Serves() bool { return w != nil && (w.Builds() || w.Calendar || w.Search) }

// CrossTables reports whether the database's ORM has CrossTables (D187).
func (w *DepsWiring) CrossTables() bool { return w != nil && (w.Calendar || w.Search) }

// DatabaseType is the ORM's type Wire, Authenticator, Routes and the
// queues take: the SQLiteDatabase over a database that lists sqlite first
// (#717), the Database over a pool otherwise.
func (w *DepsWiring) DatabaseType() string {
	if w != nil && w.SQLite {
		return "*orm.SQLiteDatabase"
	}
	return "*orm.Database"
}

// RuntimeOf is the runtime handler of a @worker of queue: one of the
// Runtime constants, or "" for a queue whose messages the implementation
// handles.
func (w *DepsWiring) RuntimeOf(queue string) string {
	switch {
	case w == nil || queue == "":
		return ""
	case queue == w.Notifier:
		return RuntimeNotifier
	case queue == w.Agents:
		return RuntimeAgents
	case queue == w.RunRequests:
		return RuntimeRunRequests
	case slices.Contains(w.SearchQueues, queue):
		return RuntimeSearch
	case slices.Contains(w.AudienceQueues, queue):
		return RuntimeAudience
	}
	return ""
}

// QueueFunc is the function of the API package that builds the queue of
// a @worker whose handler is the runtime's: NotifyQueue.
func (w WorkerInfo) QueueFunc() string {
	return goutil.GoPublicIdentifier(w.Name) + "Queue"
}

// RuntimeWorkers are the API's @worker workers whose handler is the
// runtime's (WorkerInfo.Runtime), sorted by name.
func (o *APIOutput) RuntimeWorkers() []WorkerInfo {
	var out []WorkerInfo
	for _, w := range o.Workers {
		if w.Runtime != "" {
			out = append(out, w)
		}
	}
	return out
}

// HasWiring reports whether the API package holds wiring.go.
func (o *APIOutput) HasWiring() bool { return o.Deps.Wiring != nil }

// setWorkerRuntimes marks each @worker of a queue the runtime handles,
// whose Workers method the interface then leaves out.
func (o *APIOutput) setWorkerRuntimes() {
	for i := range o.Workers {
		if o.Workers[i].Behavior == nil {
			o.Workers[i].Runtime = o.Deps.Wiring.RuntimeOf(o.Workers[i].Queue)
		}
	}
}

// HasAudienceWorkers reports whether a @worker's queue is an @audience
// declaration's (#734), whose queue wiring.go claims itself.
func (o *APIOutput) HasAudienceWorkers() bool {
	for _, w := range o.Workers {
		if w.Runtime == RuntimeAudience {
			return true
		}
	}
	return false
}

// HasSearchWorkers reports whether a @worker's queue is a @searchable
// declaration's, whose queue wiring.go claims itself.
func (o *APIOutput) HasSearchWorkers() bool {
	for _, w := range o.Workers {
		if w.Runtime == RuntimeSearch {
			return true
		}
	}
	return false
}
