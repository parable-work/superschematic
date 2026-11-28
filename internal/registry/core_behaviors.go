package registry

import (
	"embed"
	"encoding/json"
	"path"
	"slices"
)

// coreBehaviorFiles holds the declarations of the behaviors the core
// declares, one JSON file each (D16), under bare
// names: every behavior @superschematic/engine ships, every one the
// optional @superschematic/engine-workqueue package does, and the commerce
// behaviors the optional @superschematic/engine-commerce package does
// (D70), and the behaviors the behaviors' core alone implements
// (RulesPackage, D104). Each package's Rust crate (D58:
// runtime/engine/rust/behaviors, runtime/engine-workqueue/rust,
// runtime/engine-commerce/rust, and the core's
// runtime/engine/rust/rules-behaviors, which reads its copy in place)
// carries a copy of the declarations it implements, which `superschematic
// behaviors --package <name> --out` writes and CI checks
// (docs/extension-model.md section 3.16).
//
//go:embed behaviors/*.behavior.json
var coreBehaviorFiles embed.FS

// The npm packages that ship the core's behaviors, which name them in a
// behavior's spec and the engine document (enginedoc).
const (
	// EnginePackage is @superschematic/engine, whose engine registers its
	// behaviors when it opens.
	EnginePackage = "@superschematic/engine"
	// WorkQueuePackage is @superschematic/engine-workqueue, whose
	// behaviors (in the engine's addon, by name) a deployment registers
	// with the engine to run them.
	WorkQueuePackage = "@superschematic/engine-workqueue"
	// CommercePackage is @superschematic/engine-commerce, whose behaviors
	// (D70: Stock, Calendar, Holds and Payments) a deployment registers
	// with the engine to run them, as it registers the work queue's.
	CommercePackage = "@superschematic/engine-commerce"
	// RulesPackage is @superschematic/rules, the npm package that ships the
	// behaviors' core (runtime/rules/typescript): the package of a behavior
	// the core alone implements, which no engine registers (D104).
	RulesPackage = "@superschematic/rules"
)

// coreBehaviors are the core's behavior declarations, by file name, with
// the package that implements each, in the order New registers them.
var coreBehaviors = []struct{ file, pkg string }{
	{"workflow", EnginePackage},
	{"comments", EnginePackage},
	{"revisions", EnginePackage},
	{"dependencies", EnginePackage},
	{"links", EnginePackage},
	{"rollups", EnginePackage},
	{"search", EnginePackage},
	{"reactions", EnginePackage},
	{"constants", EnginePackage},
	{"variants", EnginePackage},
	{"branches", EnginePackage},
	{"staterules", EnginePackage},
	{"approvals", EnginePackage},
	{"numbering", EnginePackage},
	{"timers", EnginePackage},
	{"derived", EnginePackage},
	{"provenance", EnginePackage},
	{"lease", WorkQueuePackage},
	{"assignment", WorkQueuePackage},
	{"queue", WorkQueuePackage},
	{"presence", WorkQueuePackage},
	{"blueprint", WorkQueuePackage},
	{"budget", WorkQueuePackage},
	{"retries", WorkQueuePackage},
	{"stock", CommercePackage},
	{"calendar", CommercePackage},
	{"holds", CommercePackage},
	{"payments", CommercePackage},
	{"seats", RulesPackage},
	{"reconcile", RulesPackage},
	{"schedules", RulesPackage},
	{"pool", RulesPackage},
	{"recurrence", RulesPackage},
	{"aliased", RulesPackage},
}

// EngineImplements reports whether an engine implements the behavior:
// every behavior but one whose package is RulesPackage, which the
// behaviors' core alone runs, on a table (D104).
func EngineImplements(b Behavior) bool {
	return b.Package != RulesPackage
}

// coreBehaviorSpecs returns the core's BehaviorSpecs. A declaration that
// does not read is a defect of the build, as a core decorator that does not
// register is.
func coreBehaviorSpecs() []BehaviorSpec {
	specs := make([]BehaviorSpec, 0, len(coreBehaviors))
	for _, behavior := range coreBehaviors {
		data, err := coreBehaviorFiles.ReadFile(path.Join("behaviors", behavior.file+".behavior.json"))
		if err != nil {
			panic("registry: core behavior " + behavior.file + ": " + err.Error())
		}
		specs = append(specs, BehaviorSpec{Package: behavior.pkg, Declaration: data})
	}
	return specs
}

// RulesBehaviors are the behaviors the behaviors' core runs (D103): those
// superschematic-rules-behaviors registers (its NAMES), in its order, the
// engine's order of the first six, then the work queue's order of the
// four D104's step 2 ported in its second batch (Lease, Presence, Budget,
// Retries), then Seats and Reconcile, which D104's step 3 built on the core
// alone (RulesPackage), so a build-time table may compose them, then
// Schedules, D104's step 3's second part, then Pool, the rest of its step
// 3, then Rollups and Dependencies, the first of D103's step 4's waves
// after D104's, then Numbering and Revisions, its next, then Comments, the
// wave after, then Derived and Provenance (D73), the wave after that, then
// Stock and Holds (D70), the commerce wave's first, which meet through the
// contract Supply, then Calendar, its timed supply, then Recurrence
// (D152), which the core alone implements, as Schedules is. Of these, a table
// implements one that names no value field (implements Timers<T, C>, and
// Constants, StateRules and Approvals since D104's step 2, Lease,
// Presence, Budget and Retries since its second batch, Seats since its
// step 3, Schedules since its second part, and Pool since the rest of it)
// and carries one that does on a field (status:
// Workflow<C>, and observed: Reconcile<C> beside it since D104's step 3,
// and number: Numbering<C> since D103's step 4, which ports the engine's
// remaining behaviors in waves after them, and implements Rollups,
// Dependencies, Revisions, Comments, Derived, Provenance, Stock, Holds,
// Calendar and Payments<T, C> since that step too). A test holds the
// list to the behaviors each transcript of runtime/engine/testdata/rules
// records.
var RulesBehaviors = []string{"Workflow", "Reactions", "Constants", "StateRules", "Approvals", "Timers", "Lease", "Presence", "Budget", "Retries", "Seats", "Reconcile", "Schedules", "Pool",
	// D103's step 4, its later waves, in the order they were ported.
	"Rollups", "Dependencies", "Numbering", "Revisions", "Comments", "Derived", "Provenance",
	"Stock", "Holds", "Calendar", "Payments",
	// D152: dated occurrences of a recurrence, the core's alone.
	"Recurrence",
	// Blueprint (D149), the one behavior ported with no state of its own:
	// a create stamps the template's rows in another table.
	"Blueprint",
	// D147's Aliased, the core's alone.
	"Aliased",
}

// tableCreatesRows are the behaviors a table's create of a row runs that
// create rows of another table in the create's change set, each with why:
// Blueprint's children (D149). Such a create is no upsert's insert, so a
// table that implements one has no upserts in any ORM. Per-behavior code,
// as tableLeavesOut is, until step 5 derives it from the behavior's class
// (D103).
var tableCreatesRows = map[string]string{
	"Blueprint": "a create of the row stamps a row of the child table per step of its template, in the create's change set (D149)",
}

// TableCreatesRows reports whether a table's use of the behavior name
// creates rows of another table when a row is created (tableCreatesRows).
func TableCreatesRows(name string) bool {
	return tableCreatesRows[name] != ""
}

// tableLeavesOut are the operations of a behavior's declaration that a
// table's use of it leaves out, by behavior and operation, each with why:
// one the engine serves and the behaviors' core declares none of (D104's
// step 4). Per-behavior code, as BehaviorListens is, until step 5 derives
// it from the behavior's class (D103).
var tableLeavesOut = map[string]map[string]string{
	"Dependencies": {
		"listDependents": "the rows a row blocks are a query over every table's blockers, which the behaviors' core cannot enumerate, so the engine serves it alone",
	},
	// What a table's Comments leaves out in a build: streamed replies wait
	// for a table's API to serve signals, which development mode's server
	// does on the core; and code anchors, which the core's comments module
	// leaves out too (LEFT_OUT_OPERATIONS; D77, D103's step 4). Read state
	// and follows are a table's since #713: markRead and follow are
	// operations, and a reader's unread is the use's view.
	"Comments": {
		"begin":       commentsStreamed,
		"appendDraft": commentsStreamed,
		"finish":      commentsStreamed,
		"interrupt":   commentsStreamed,
		"moveAnchor":  commentsAnchors,
	},
}

// Why a table's Comments leaves out each part a build cannot serve yet
// (D103's step 4): the core runs streamed replies, which a table's
// generated API does not serve; and the core's own words for what it runs
// no command for yet.
const (
	commentsStreamed   = "a streamed reply (begin, appendDraft, finish, interrupt and the config's composeTimeoutMs and maxDrafts) waits for a table's generated API to serve the core's signals (D77's primitive 2), which development mode's event stream does"
	commentsDeliveries = "claiming and settling deliveries (claimDeliveries, settleDeliveries and the config's deliverPermission, maxAttempts, retryBackoffMs and deliveryRetentionMs) and searchMessages each run across a namespace, which waits for Comments' operations of the namespace on the core (D77's primitive 3); a table's Comments hands its deliveries to the notifier as events instead, with events: true (#713), and the inbox is the notifier's (D153)"
	commentsAnchors    = "moving a code block's anchor (moveAnchor and anchorPermission) waits for code anchors on the core (D77's step 5, D103's step 4)"
)

// tableArmsNoDeadlines are the behaviors whose declarations arm deadlines
// and whose use on a table arms none, each with why: Comments arms a
// composing message's idle deadline from a streamed reply, which a table
// leaves out (commentsStreamed), so a table's Comments keeps no deadline
// queue for a worker to run. Per-behavior code, as tableLeavesOut is, until
// step 5 derives it from the behavior's class (D103).
var tableArmsNoDeadlines = map[string]string{
	"Comments": commentsStreamed,
}

// TableArmsDeadlines reports whether a table's use of the behavior name
// arms deadlines: its declaration says so (armsDeadlines), and a table
// leaves out none of the operations that arm them (tableArmsNoDeadlines).
func TableArmsDeadlines(name string, armsDeadlines bool) bool {
	return armsDeadlines && tableArmsNoDeadlines[name] == ""
}

// RefusedMember is a member of a behavior's config that a table's use of it
// refuses, and why.
type RefusedMember struct {
	Member string
	Why    string
}

// tableRefusesMembers are the members of a behavior's config that a table's
// use of it refuses, by behavior, in the core's order, each with why: what
// the behaviors' core cannot run yet (D103's step 4). Per-behavior code, as
// tableLeavesOut is, until step 5 derives it from the behavior's class.
var tableRefusesMembers = map[string][]RefusedMember{
	"Comments": {
		{"composeTimeoutMs", commentsStreamed},
		{"maxDrafts", commentsStreamed},
		{"deliverPermission", commentsDeliveries},
		{"maxAttempts", commentsDeliveries},
		{"retryBackoffMs", commentsDeliveries},
		{"deliveryRetentionMs", commentsDeliveries},
		{"anchorPermission", commentsAnchors},
	},
}

// TableRefusesMembers are the members of the behavior name's config a
// table's use of it refuses, in the core's order, each with why; none for
// a behavior whose every member a table takes.
func TableRefusesMembers(name string) []RefusedMember {
	return tableRefusesMembers[name]
}

// TableLeavesOut is why a table's use of the behavior name leaves its
// operation out, or "" for one the behaviors' core runs: the layout lists
// no such operation, so neither ORM binds it, and @behaviorOperations
// serves none of it, of which the build warns (D104's step 4).
func TableLeavesOut(name, operation string) string {
	return tableLeavesOut[name][operation]
}

// RunsOnTheCore reports whether the behaviors' core runs the behavior name
// (RulesBehaviors).
func RunsOnTheCore(name string) bool {
	return slices.Contains(RulesBehaviors, name)
}

// RunsOnTheCore reports whether a core runs the behavior: the stock core,
// one of RulesBehaviors; a project's, the core's behaviors and the crates
// of the behaviors a class or an extension declares with a Rust crate that
// implements the core's hooks (D103's step 5), which the project's rules
// crate assembles; or any core, a declarative behavior, whose declaration
// states its rules in the vocabulary the core interprets and which a host
// hands its core whole (D103's step 5).
func (b Behavior) RunsOnTheCore() bool {
	return RunsOnTheCore(b.Name) || b.Rules.Declared() || b.Declarative()
}

// BehaviorReads are the tables a use of the behavior name with config
// reads beside its own row's, which a table that composes no behavior may
// be (D152): Recurrence's holiday table (holidays.table) and its relative
// anchor's target (relative.type), in that order, each once; none for any
// other behavior or a config that does not read. The behaviors' layout
// carries each, read-only, so an adapter answers the core's reads of it.
// Per-behavior code, as BehaviorListens is, until step 5 derives it from
// the behavior's class (D103).
func BehaviorReads(name string, config json.RawMessage) []string {
	if name != "Recurrence" || len(config) == 0 {
		return nil
	}
	var parsed struct {
		Holidays *struct {
			Table string `json:"table"`
		} `json:"holidays"`
		Relative *struct {
			Type string `json:"type"`
		} `json:"relative"`
	}
	if json.Unmarshal(config, &parsed) != nil {
		return nil
	}
	var out []string
	if parsed.Holidays != nil && parsed.Holidays.Table != "" {
		out = append(out, parsed.Holidays.Table)
	}
	if parsed.Relative != nil && parsed.Relative.Type != "" && !slices.Contains(out, parsed.Relative.Type) {
		out = append(out, parsed.Relative.Type)
	}
	return out
}

// BehaviorListen is a table whose writes a use of another table hears
// through a relation of that table (D103): Reactions' allTerminal over
// Task's project hears Task through task.project.
type BehaviorListen struct {
	Type string // the table whose writes are heard: Task
	Link string // its to-one relation to the listening table: project
}

// BehaviorListens are the tables a use of the behavior name with config
// listens to, each once, in the config's order: the objects its
// declaration's listen references reach (ReferenceListen), each one's
// schema and link, which D103's step 5 derives from the declaration where
// per-behavior code read Reactions' config before. Reactions' are its
// allTerminal and anyTerminal rules ({ schema, link }), rule by rule. None
// for a behavior that is not registered or names no listen, or a config
// that does not read.
func (r *Registry) BehaviorListens(name string, config json.RawMessage) []BehaviorListen {
	behavior, ok := r.Behavior(name)
	if !ok || len(config) == 0 {
		return nil
	}
	listens := behavior.ReferencesOf(ReferenceListen)
	if len(listens) == 0 {
		return nil
	}
	var decoded any
	if json.Unmarshal(config, &decoded) != nil {
		return nil
	}
	// Each reference's values, in the config's order: a list's item by
	// item, with every reference's values within one item before the
	// next, as a rule's allTerminal then its anyTerminal.
	var heard []orderedValue
	for _, ref := range listens {
		heard = append(heard, configValuesOrdered(decoded, ref.Path)...)
	}
	slices.SortStableFunc(heard, func(a, b orderedValue) int { return slices.Compare(a.order, b.order) })
	var out []BehaviorListen
	for _, h := range heard {
		object, _ := h.value.(map[string]any)
		schema, _ := object["schema"].(string)
		link, _ := object["link"].(string)
		if schema == "" || link == "" {
			continue
		}
		listen := BehaviorListen{Type: schema, Link: link}
		if !slices.Contains(out, listen) {
			out = append(out, listen)
		}
	}
	return out
}
