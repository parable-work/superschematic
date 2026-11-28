package generator

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/tsrestgen"
	"github.com/parable-work/superschematic/internal/generator/tsutil"
	ir "github.com/parable-work/superschematic/ir"
)

// typeScriptProviders is what a TypeScript API's Deps holds of its runtime
// providers (D159, #702): the slots its @envVars class names, and the provider
// packages its config lists, each imported by its directory relative to
// the API package's. A provider of a Rust crate alone has no package a
// TypeScript server links, and providersFrom refuses a slot that names it
// at start. Nil for an API whose config names no slot.
func (r run) typeScriptProviders() (*tsrestgen.DepsProviders, error) {
	slots, err := r.Schema.ProviderSlotsOf()
	if err != nil || len(slots) == 0 {
		return nil, err
	}
	out := &tsrestgen.DepsProviders{}
	for _, slot := range slices.Sorted(maps.Keys(slots)) {
		out.Slots = append(out.Slots, tsrestgen.DepsProviderSlot{Slot: slot, Interface: slots[slot]})
	}
	providers := slices.Clone(r.Schema.Providers)
	slices.SortFunc(providers, func(a, b *ir.RuntimeProvider) int { return strings.Compare(a.Name, b.Name) })
	apiDir := APIDir(r.Options.OutputRoot, r.Config.Name)
	idents := map[string]bool{}
	for _, p := range providers {
		if p == nil || p.Package == "" {
			continue
		}
		if p.Dir == "" {
			return nil, fmt.Errorf("generator: the TypeScript server of %s links runtime provider %s, whose directory the loader did not read", r.Config.Name, p.Name)
		}
		rel, err := naming.RelPath(apiDir, p.Dir)
		if err != nil {
			return nil, err
		}
		rel = filepath.ToSlash(rel)
		if !strings.HasPrefix(rel, ".") {
			rel = "./" + rel
		}
		ident := tsutil.ToCamelCase(p.Name) + "Provider"
		for i := 2; idents[ident]; i++ {
			ident = fmt.Sprintf("%sProvider%d", tsutil.ToCamelCase(p.Name), i)
		}
		idents[ident] = true
		out.Packages = append(out.Packages, tsrestgen.DepsProviderPackage{Name: p.Name, Ident: ident, Import: rel})
	}
	return out, nil
}

// typeScriptRuntime sets what deps holds of the runtime pieces db, the
// API's database, declares, which the generated server wires over its
// repositories, on Postgres or, from the runtime's ./d1 entry, on SQLite
// and D1 (D189, #702): the notifier of its @notifier("dispatch") queue
// (D153); the inbound path of its conversations and its @runRequests
// queue (D157); the calendar of its @calendarSource tables (D152); search
// across its @searchable tables and their indexing queues (D155); and its
// @accessGrants and @accessLog tables (D156).
func typeScriptRuntime(deps *tsrestgen.DepsInfo, db *ir.Schema) {
	if db == nil {
		return
	}
	if dispatch := db.NotifierTable(ir.NotifierDispatch); dispatch != nil {
		deps.Notifier = &tsrestgen.DepsNotifier{Dispatch: dispatch.Name}
		if runs := db.RunRequestsQueue(); runs != nil && db.ConversationsTable(ir.ConversationsPart) != nil {
			deps.Inbound = &tsrestgen.DepsInbound{Runs: runs.Name}
		}
	}
	for _, td := range db.SearchableTables() {
		deps.Routes.Search = true
		if q := td.Searchable.Queue; q != "" && !slices.Contains(deps.Routes.SearchQueues, q) {
			deps.Routes.SearchQueues = append(deps.Routes.SearchQueues, q)
		}
	}
	slices.Sort(deps.Routes.SearchQueues)
	for _, td := range db.AudienceTables() {
		if q := td.Audience.Queue; q != "" && !slices.Contains(deps.Routes.AudienceQueues, q) {
			deps.Routes.AudienceQueues = append(deps.Routes.AudienceQueues, q)
		}
	}
	slices.Sort(deps.Routes.AudienceQueues)
	for _, td := range db.Types {
		if td != nil && len(td.CalendarSources) > 0 {
			deps.Routes.Calendar = true
		}
	}
	for _, td := range db.Types {
		if td != nil && td.Attachments != nil {
			deps.Routes.Files = true
		}
	}
	deps.Routes.Grants = db.AccessGrantsTable() != nil
	deps.Routes.AccessLog = db.AccessLogTable() != nil
}

// typeScriptRuntimeQueues are the queues of db, schema, whose @worker a
// runtime piece runs in TypeScript beside the dispatcher's: the notifier's
// dispatch queue (D153), the @searchable declarations' queues (D155) and
// the @audience declarations' (D156, #734),
// whichever dialect the database's DB service lists first (#702).
// typeScriptRuntime sets the same for the API's own build.
func (r run) typeScriptRuntimeQueues(db string, schema *ir.Schema) []string {
	deps := tsrestgen.DepsInfo{}
	typeScriptRuntime(&deps, schema)
	var out []string
	if deps.Notifier != nil {
		out = append(out, deps.Notifier.Dispatch)
	}
	out = append(out, deps.Routes.SearchQueues...)
	return append(out, deps.Routes.AudienceQueues...)
}
