package tsrestgen

import (
	"fmt"
	"slices"

	"github.com/parable-work/superschematic/internal/generator/tsgen"
)

// The runtime pieces a TypeScript API's generated server wires itself
// (#702; D159, D153, D157, D152, D155, D156, D158): the providers its config
// names, the notifier, the inbound path and the agent dispatcher in Deps,
// over the repositories; the notifier's relay callbacks and inbox, the
// calendar's and search's routes and their MCP tools; the handlers of the
// notifier's dispatch queue and of a @searchable declaration's indexing
// queue; and the grants' authenticator and the access log around the
// router. providers.ts reads the providers, and runtime.ts builds Deps
// with the rest (withRuntime), mounts the routes (runtimeRoutes) and
// hands each such queue's message to its handler. An implementation adds
// to or overrides each piece's options through createRuntimeOptions, an
// export it may leave out.

// ProvidersFile is the module of the API package that reads the runtime
// providers of its slots (D159), which index.ts exports.
const ProvidersFile = "providers.ts"

// RuntimeFile is the module of the API package that builds Deps' runtime
// pieces and their routes, which index.ts exports. It replaces agents.ts.
const RuntimeFile = "runtime.ts"

// RuntimeOptionsFunc is the function an implementation may export, of the
// generated RuntimeOptionsConstructor's type: what the runtime pieces are
// built with beside what the entrypoint fills.
const RuntimeOptionsFunc = "createRuntimeOptions"

// The runtime pieces whose handler takes a @worker's messages
// (WorkerInfo.Runtime): the Workers interface has no method for such a
// worker.
const (
	// WorkerAgents is a worker of the agent dispatcher's queues (D183).
	WorkerAgents = "agents"
	// WorkerNotifier is a worker of the notifier's dispatch queue (D153).
	WorkerNotifier = "notifier"
	// WorkerSearch is a worker of a @searchable declaration's indexing
	// queue (D155).
	WorkerSearch = "search"
	// WorkerAudience is a worker of an @audience declaration's queue
	// (D156, #734), whose handler is audience.ts's.
	WorkerAudience = "audience"
)

// AudienceFile is the module of the API package that holds the handler of
// the database's @audience queues (#734).
const AudienceFile = "audience.ts"

// DepsProviders are the runtime providers Deps holds (D159): the API's
// provider slots, each with its interface, and the provider packages its
// config lists, which providers.ts imports.
type DepsProviders struct {
	Slots    []DepsProviderSlot
	Packages []DepsProviderPackage
}

// DepsProviderSlot is one provider slot: its config field and the
// interface it takes.
type DepsProviderSlot struct {
	Slot      string
	Interface string
}

// DepsProviderPackage is one provider package the API's config lists: its
// provider's name, the identifier providers.ts imports its default export
// as, and the path it imports it from, the package's directory relative
// to the API package's, which the dispatch layer sets.
type DepsProviderPackage struct {
	Name   string
	Ident  string
	Import string
}

// ChannelSlots are the slots that take a channel adapter, in order: the
// notifier sends on each, and the server mounts each one's relay
// callbacks.
func (p *DepsProviders) ChannelSlots() []string {
	if p == nil {
		return nil
	}
	var out []string
	for _, s := range p.Slots {
		if s.Interface == "channel" {
			out = append(out, s.Slot)
		}
	}
	return out
}

// DepsNotifier is the notifier Deps holds (D153): createNotifier over
// Deps.orm and the database's @notifier("dispatch") queue, with the
// providers' channel adapters.
type DepsNotifier struct {
	// Dispatch is the database's dispatch queue class.
	Dispatch string
}

// DispatchConst is the name the database's types export the dispatch
// queue's definition as.
func (n *DepsNotifier) DispatchConst() string { return tsgen.QueueConst(n.Dispatch) }

// DepsInbound is the inbound path Deps holds (D157): createInbound over
// Deps.orm, the notifier and the database's @runRequests queue.
type DepsInbound struct {
	// Runs is the database's @runRequests queue class.
	Runs string
}

// RunsConst is the name the database's types export the run requests
// queue's definition as.
func (i *DepsInbound) RunsConst() string { return tsgen.QueueConst(i.Runs) }

// DepsRoutes are the runtime's routes over the database the server mounts
// beside the router, and the grants and the access log around it.
type DepsRoutes struct {
	// Calendar is set for a database whose tables declare a
	// @calendarSource (D152): calendarRoutes, and the calendar MCP tool.
	Calendar bool

	// Search is set for a database with a @searchable table (D155):
	// searchRoutes, the search MCP tool, and SearchQueues' handler.
	Search bool

	// SearchQueues are the @searchable declarations' indexing queues, in
	// order: a @worker of one hands each message to indexSearch.
	SearchQueues []string

	// AudienceQueues are the @audience declarations' queues (D156,
	// #734), in order: a @worker of one hands each message to
	// resolveAudience, through audience.ts.
	AudienceQueues []string

	// Grants is set for a database with an @accessGrants table (D156): a
	// grant's token runs as its holder on the router's routes, through
	// grantAuthenticator around the implementation's authenticator.
	Grants bool

	// AccessLog is set for a database with an @accessLog table (D156):
	// the router records each call the log records there (accessLogOf).
	AccessLog bool

	// Files is set for a database with an @attachments table (D154): with
	// a files slot among the API's providers, runtime.ts exports
	// processPendingFiles, the file processor's pass over what the
	// attachments have pending (D159, #702).
	Files bool
}

// HasProviders reports whether Deps holds the runtime providers (D159).
func (o *APIOutput) HasProviders() bool { return o.Deps.Providers != nil && o.HasEnvConfig }

// HasNotifier reports whether Deps holds the notifier (D153).
func (o *APIOutput) HasNotifier() bool { return o.Deps.Notifier != nil }

// HasInbound reports whether Deps holds the inbound path (D157).
func (o *APIOutput) HasInbound() bool { return o.Deps.Inbound != nil }

// HasCalendar reports whether the server serves the calendar (D152).
func (o *APIOutput) HasCalendar() bool { return o.Deps.Routes.Calendar }

// HasSearch reports whether the server serves search across tables (D155).
func (o *APIOutput) HasSearch() bool { return o.Deps.Routes.Search }

// HasAudienceQueues reports whether the API package writes audience.ts:
// the database's @audience declarations name a queue (#734).
func (o *APIOutput) HasAudienceQueues() bool { return len(o.Deps.Routes.AudienceQueues) > 0 }

// HasFileProcessing reports whether runtime.ts exports processPendingFiles
// (D154, D159, #702): the database declares @attachments, and the API's
// config names a files slot, whose processors make the attachments' text
// and previews.
func (o *APIOutput) HasFileProcessing() bool {
	if !o.Deps.Routes.Files || !o.HasProviders() {
		return false
	}
	for _, s := range o.Deps.Providers.Slots {
		if s.Interface == "files" {
			return true
		}
	}
	return false
}

// HasRuntime reports whether the API package writes runtime.ts: Deps holds
// a piece the entrypoint builds (the notifier, the inbound path, the
// agent dispatcher), or the server serves the calendar or search.
func (o *APIOutput) HasRuntime() bool {
	return o.HasNotifier() || o.HasInbound() || o.HasAgents() || o.HasCalendar() || o.HasSearch() || o.HasFileProcessing()
}

// BuildsRuntime reports whether withRuntime adds a member to Deps, which
// the entrypoint builds over DepsBase.
func (o *APIOutput) BuildsRuntime() bool { return o.HasNotifier() || o.HasInbound() || o.HasAgents() }

// RuntimeMembers are the Deps members withRuntime builds, which DepsBase
// leaves out.
func (o *APIOutput) RuntimeMembers() []string {
	var out []string
	if o.HasNotifier() {
		out = append(out, "'notifier'")
	}
	if o.HasInbound() {
		out = append(out, "'inbound'")
	}
	if o.HasAgents() {
		out = append(out, "'agents'")
	}
	return out
}

// RuntimeImports are what runtime.ts imports of the runtime's
// RuntimeEntry.
func (o *APIOutput) RuntimeImports() []string {
	var out []string
	if o.HasAgents() {
		out = append(out, "AGENT_PERMISSIONS", "createAgents", "type AgentRunRequest", "type Agents")
	}
	if o.HasCalendar() {
		out = append(out, "calendarMcpTool", "calendarRoutes", "callCalendarTool")
	}
	if o.HasSearch() {
		out = append(out, "callSearchTool", "indexSearch", "searchMcpTool", "searchRoutes", "type SearchIndexing")
	}
	if o.HasInbound() {
		out = append(out, "createInbound")
	}
	if o.HasNotifier() {
		out = append(out, "createNotifier", "notifierRoutes", "notifierUpkeep", "type NoticeWork")
	}
	if o.HasFileProcessing() {
		out = append(out, "processFiles", "type FileProcessingRun")
	}
	slices.SortFunc(out, func(a, b string) int {
		key := func(s string) string {
			if len(s) > 5 && s[:5] == "type " {
				return s[5:]
			}
			return s
		}
		ka, kb := key(a), key(b)
		switch {
		case ka < kb:
			return -1
		case ka > kb:
			return 1
		}
		return 0
	})
	return out
}

// RuntimeQueueImports are the queue definitions runtime.ts imports from
// the database's ./queues entry, sorted.
func (o *APIOutput) RuntimeQueueImports() []string {
	var out []string
	if o.HasNotifier() {
		out = append(out, o.Deps.Notifier.DispatchConst())
	}
	if o.HasInbound() {
		out = append(out, o.Deps.Inbound.RunsConst())
	}
	if o.HasAgents() {
		out = append(out, o.Deps.Agents.RequestsConst())
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// HasRuntimeMCP reports whether runtime.ts serves MCP tools beside the
// router's: the calendar's or search's, on an API whose router serves an
// MCP endpoint.
func (o *APIOutput) HasRuntimeMCP() bool { return o.MCP != nil && (o.HasCalendar() || o.HasSearch()) }

// RuntimeWorkers are the API's @worker workers whose handler a runtime
// piece is, in order.
func (o *APIOutput) RuntimeWorkers(runtime string) []WorkerInfo {
	var out []WorkerInfo
	for _, w := range o.Workers {
		if w.Runtime == runtime {
			out = append(out, w)
		}
	}
	return out
}

// runtimeOf is the runtime piece whose handler takes a @worker of queue's
// messages, given deps: "" for one the implementation handles.
func runtimeOf(deps DepsInfo, queue string) string {
	switch {
	case queue == "":
		return ""
	case deps.Agents.Dispatches(queue):
		return WorkerAgents
	case deps.Notifier != nil && queue == deps.Notifier.Dispatch:
		return WorkerNotifier
	case slices.Contains(deps.Routes.SearchQueues, queue):
		return WorkerSearch
	case slices.Contains(deps.Routes.AudienceQueues, queue):
		return WorkerAudience
	}
	return ""
}

// RuntimeEntry is the entry of the HTTP runtime that deps.ts and
// runtime.ts import the runtime pieces from: ./postgres over a pool, and
// ./d1 over a SqlDatabase, a D1 binding on Workers or a SQLite file under
// stack dev, which imports no driver (D189, #702).
func (o *APIOutput) RuntimeEntry() string {
	if o.DepsSQLite() {
		return "/d1"
	}
	return "/postgres"
}

// checkRuntimeDeps refuses runtime pieces Deps cannot hold: each runs over
// the repositories of the API's database, on Postgres or on SQLite and D1
// (D189, #702), and the inbound path hands a user's reply to the notifier.
func (o *APIOutput) checkRuntimeDeps(deps DepsInfo) error {
	var piece string
	switch {
	case deps.Notifier != nil:
		piece = "the notifier (D153)"
	case deps.Inbound != nil:
		piece = "the inbound path (D157)"
	case deps.Routes.Calendar:
		piece = "the calendar's routes (D152)"
	case deps.Routes.Search:
		piece = "search across tables (D155)"
	case deps.Routes.Grants || deps.Routes.AccessLog:
		piece = "the grants and the access log (D156)"
	case deps.Routes.Files && deps.Providers != nil:
		piece = "the file processor (D159)"
	default:
		return nil
	}
	if !depsORM(deps) {
		return fmt.Errorf("tsrestgen: %s's server wires %s over the repositories %s's TypeScript types export, and holds none", o.SchemaName, piece, deps.Database)
	}
	if deps.Inbound != nil && deps.Notifier == nil {
		return fmt.Errorf("tsrestgen: %s's Deps holds the inbound path (D157), which hands a user's reply to the notifier, and %s declares no notifier", o.SchemaName, deps.Database)
	}
	return nil
}

// DepsRuntimeTypes are the types deps.ts imports of the runtime's
// RuntimeEntry for the runtime pieces Deps holds and their options.
func (o *APIOutput) DepsRuntimeTypes() []string {
	var out []string
	if o.HasAgents() {
		out = append(out, "Agents", "AgentsOptions")
	}
	if o.HasCalendar() {
		out = append(out, "CalendarRoutesOptions")
	}
	if o.HasFileProcessing() {
		out = append(out, "FileProcessingOptions")
	}
	if o.HasInbound() {
		out = append(out, "Inbound", "InboundOptions")
	}
	if o.HasNotifier() {
		out = append(out, "Notifier", "NotifierOptions", "NotifierRoutesOptions")
	}
	if o.HasSearch() {
		out = append(out, "SearchOptions")
	}
	return out
}
