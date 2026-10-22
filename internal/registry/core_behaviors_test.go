package registry

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
)

// coreBehaviorNames are the behaviors New registers, sorted.
var coreBehaviorNames = []string{"Approvals", "Assignment", "Blueprint", "Branches", "Budget", "Calendar", "Comments", "Constants", "Dependencies", "Holds", "Lease", "Links", "Numbering",
	"Payments", "Presence", "Queue", "Reactions", "Retries", "Revisions", "Rollups", "Search", "StateRules", "Stock", "Timers", "Variants", "Workflow"}

// workQueueBehaviorNames are the core behaviors @superschematic/engine-workqueue
// implements, and commerceBehaviorNames the ones
// @superschematic/engine-commerce does (D70); the engine implements the
// rest.
var (
	workQueueBehaviorNames = []string{"Assignment", "Blueprint", "Budget", "Lease", "Presence", "Queue", "Retries"}
	commerceBehaviorNames  = []string{"Calendar", "Holds", "Payments", "Stock"}
)

// A registry with no extension declares the behaviors the engine, the
// work-queue package and the commerce package implement, as the core's,
// under bare names, each with the package that implements it, and holds
// each config to its schema.
func TestCoreBehaviors(t *testing.T) {
	reg := New(naming.Default())
	finalizeWithCoreGenerators(t, reg)
	if got := reg.BehaviorNames(); !slices.Equal(got, coreBehaviorNames) {
		t.Fatalf("BehaviorNames() = %v, want %v", got, coreBehaviorNames)
	}
	for _, name := range coreBehaviorNames {
		b, _ := reg.Behavior(name)
		if b.Extension != "" {
			t.Errorf("%s is registered by %q, want the core", name, b.Extension)
		}
		want := EnginePackage
		switch {
		case slices.Contains(workQueueBehaviorNames, name):
			want = WorkQueuePackage
		case slices.Contains(commerceBehaviorNames, name):
			want = CommercePackage
		}
		if b.Package != want {
			t.Errorf("%s is implemented by %q, want %q", name, b.Package, want)
		}
	}
	workflow, _ := reg.Behavior("Workflow")
	comments, _ := reg.Behavior("Comments")
	revisions, _ := reg.Behavior("Revisions")
	dependencies, _ := reg.Behavior("Dependencies")
	links, _ := reg.Behavior("Links")
	rollups, _ := reg.Behavior("Rollups")
	search, _ := reg.Behavior("Search")
	reactions, _ := reg.Behavior("Reactions")
	if !workflow.ConfigRequired() || comments.ConfigRequired() || revisions.ConfigRequired() || dependencies.ConfigRequired() || !links.ConfigRequired() ||
		!rollups.ConfigRequired() || !search.ConfigRequired() || !reactions.ConfigRequired() {
		t.Errorf("ConfigRequired: Workflow %v, Comments %v, Revisions %v, Dependencies %v, Links %v, Rollups %v, Search %v, Reactions %v; want true, false, false, false, true, true, true, true",
			workflow.ConfigRequired(), comments.ConfigRequired(), revisions.ConfigRequired(), dependencies.ConfigRequired(), links.ConfigRequired(),
			rollups.ConfigRequired(), search.ConfigRequired(), reactions.ConfigRequired())
	}
	if !slices.Equal(dependencies.Requires, []string{"Workflow"}) || len(links.Requires) != 0 || len(rollups.Requires) != 0 || !slices.Equal(reactions.Requires, []string{"Workflow"}) {
		t.Errorf("requires: Dependencies %v, Links %v, Rollups %v, Reactions %v; want [Workflow], none, none and [Workflow]",
			dependencies.Requires, links.Requires, rollups.Requires, reactions.Requires)
	}
	if len(rollups.Operations) != 0 || len(rollups.Fields) != 1 || rollups.Fields[0].Name != "values" {
		t.Errorf("Rollups: operations %v, fields %v; want none and values", rollups.Operations, rollups.Fields)
	}
	// Reactions adds no field and no operation: the engine's runner runs it.
	if len(reactions.Fields) != 0 || len(reactions.Operations) != 0 {
		t.Errorf("Reactions fields %v and operations %v, want none", reactions.Fields, reactions.Operations)
	}
	// The fields that move at a read, with no event of their own instance,
	// say so (D56): no other core field does.
	var volatile []string
	for _, name := range coreBehaviorNames {
		b, _ := reg.Behavior(name)
		for _, field := range b.Fields {
			if field.Volatile {
				volatile = append(volatile, BehaviorFieldPath(name, field.Name))
			}
		}
	}
	if want := []string{"Budget.meters", "Calendar.timeZone", "Calendar.hours", "Calendar.closures", "Dependencies.blocked", "Lease.active", "Links.targets", "Rollups.values",
		"Stock.low"}; !slices.Equal(volatile, want) {
		t.Errorf("volatile fields = %v, want %v", volatile, want)
	}
	if names := fieldNames(workflow.Fields); !slices.Equal(names, []string{"status", "enteredAt"}) {
		t.Errorf("Workflow fields = %v, want status and enteredAt", names)
	}
	var scopes []string
	for _, op := range links.Operations {
		scopes = append(scopes, op.Name+":"+op.Scope)
	}
	if want := []string{"link:", "unlink:", "listLinked:schema"}; !slices.Equal(scopes, want) {
		t.Errorf("Links operations and scopes = %v, want %v", scopes, want)
	}
	// Search's operations are all schema-level; only the embedder's
	// settleEmbeddings writes.
	var searchOps []string
	for _, op := range search.Operations {
		searchOps = append(searchOps, fmt.Sprintf("%s:%s:%v", op.Name, op.Scope, op.Writes))
	}
	if want := []string{"search:schema:false", "similar:schema:false", "staleEmbeddings:schema:false", "settleEmbeddings:schema:true"}; !slices.Equal(searchOps, want) || len(search.Fields) != 0 {
		t.Errorf("Search operations = %v and fields %v, want %v and no field", searchOps, search.Fields, want)
	}
	lease, _ := reg.Behavior("Lease")
	assignment, _ := reg.Behavior("Assignment")
	queue, _ := reg.Behavior("Queue")
	if lease.ConfigRequired() || assignment.ConfigRequired() || !queue.ConfigRequired() {
		t.Errorf("ConfigRequired: Lease %v, Assignment %v, Queue %v; want false, false, true", lease.ConfigRequired(), assignment.ConfigRequired(), queue.ConfigRequired())
	}
	if len(lease.Requires) != 0 || len(assignment.Requires) != 0 || !slices.Equal(queue.Requires, []string{"Workflow", "Lease"}) {
		t.Errorf("requires: Lease %v, Assignment %v, Queue %v; want none, none and [Workflow Lease]", lease.Requires, assignment.Requires, queue.Requires)
	}
	presence, _ := reg.Behavior("Presence")
	blueprint, _ := reg.Behavior("Blueprint")
	if !presence.ConfigRequired() || !blueprint.ConfigRequired() || len(presence.Requires) != 0 || len(blueprint.Requires) != 0 {
		t.Errorf("Presence and Blueprint: config required %v, %v, requires %v, %v; want true, true, none, none",
			presence.ConfigRequired(), blueprint.ConfigRequired(), presence.Requires, blueprint.Requires)
	}
	if len(blueprint.Operations) != 0 || len(blueprint.Fields) != 1 || blueprint.Fields[0].Name != "children" {
		t.Errorf("Blueprint = %+v, want no operation and one field, children", blueprint)
	}
	budget, _ := reg.Behavior("Budget")
	retries, _ := reg.Behavior("Retries")
	if !budget.ConfigRequired() || !retries.ConfigRequired() || len(budget.Requires) != 0 || !slices.Equal(retries.Requires, []string{"Workflow"}) {
		t.Errorf("Budget: config required %v, requires %v; Retries: config required %v, requires %v; want true, none, true, [Workflow]",
			budget.ConfigRequired(), budget.Requires, retries.ConfigRequired(), retries.Requires)
	}
	// Constants and Variants judge the fields a write stores: no field, no
	// operation, no requirement, and no veto code or precondition, since
	// what they refuse is an issue at a field (invalid_instance).
	constants, _ := reg.Behavior("Constants")
	variants, _ := reg.Behavior("Variants")
	for _, b := range []Behavior{constants, variants} {
		if b.Package != EnginePackage || !b.ConfigRequired() || len(b.Requires) != 0 || len(b.Fields) != 0 || len(b.Operations) != 0 ||
			len(b.Vetoes) != 0 || len(b.PreconditionSchema) != 0 || len(b.CreateParamsSchema) != 0 {
			t.Errorf("%s = %+v, want the engine's, a config, and no requirement, field, operation, veto or parameter", b.Name, b)
		}
	}
	// StateRules holds the own fields to the state of the type's Workflow:
	// no field, no operation, and Workflow required, since its guard reads
	// the status.
	stateRules, _ := reg.Behavior("StateRules")
	if stateRules.Package != EnginePackage || !stateRules.ConfigRequired() || !slices.Equal(stateRules.Requires, []string{"Workflow"}) || len(stateRules.Fields) != 0 ||
		len(stateRules.Operations) != 0 || len(stateRules.PreconditionSchema) != 0 || len(stateRules.CreateParamsSchema) != 0 {
		t.Errorf("StateRules = %+v, want the engine's, a config, Workflow required, and no field, operation or parameter", stateRules)
	}
	// Branches makes each instance a version graph's root: one field, the
	// release pointer's version, no requirement, and sixteen operations, all
	// of instance scope, of which the nine that write the graph write and
	// the seven that read it read.
	branches, _ := reg.Behavior("Branches")
	if branches.Package != EnginePackage || !branches.ConfigRequired() || len(branches.Requires) != 0 || len(branches.Fields) != 1 || branches.Fields[0].Name != "release" ||
		len(branches.PreconditionSchema) != 0 || len(branches.CreateParamsSchema) != 0 {
		t.Errorf("Branches = %+v, want the engine's, a config, the field release, and no requirement or parameter", branches)
	}
	var branchOps []string
	for _, op := range branches.Operations {
		if op.Scope != "" {
			t.Errorf("Branches.%s has scope %q, want instance", op.Name, op.Scope)
		}
		branchOps = append(branchOps, fmt.Sprintf("%s:%v", op.Name, op.Writes))
		// branch takes no fromRef to branch from the primary line, which is
		// how an instance older than its schema's Branches gets one.
		if op.Name == "branch" {
			var params struct {
				Required   []string                   `json:"required"`
				Properties map[string]json.RawMessage `json:"properties"`
			}
			if err := json.Unmarshal(op.ParamsSchema, &params); err != nil || !slices.Equal(params.Required, []string{"name"}) || params.Properties["fromRef"] == nil {
				t.Errorf("Branches.branch params = %s, want fromRef optional beside a required name", op.ParamsSchema)
			}
		}
	}
	if want := []string{"branch:true", "save:true", "commit:true", "seal:true", "merge:true", "rebase:true", "revert:true", "releaseCommit:true", "discard:true",
		"refs:false", "releases:false", "compose:false", "materialize:false", "released:false", "diff:false", "history:false"}; !slices.Equal(branchOps, want) {
		t.Errorf("Branches operations = %v, want %v", branchOps, want)
	}
	// Every work-queue operation writes but countClaimable and checkReserve,
	// which read; claimNext, countClaimable and expireHolder are schema-level.
	reads := []string{"countClaimable", "checkReserve"}
	schemaLevel := []string{"claimNext", "countClaimable", "expireHolder"}
	for _, b := range []Behavior{lease, assignment, queue, presence, budget, retries} {
		for _, op := range b.Operations {
			if op.Writes == slices.Contains(reads, op.Name) || (op.Scope == OperationScopeSchema) != slices.Contains(schemaLevel, op.Name) {
				t.Errorf("%s.%s: writes %v, scope %q", b.Name, op.Name, op.Writes, op.Scope)
			}
		}
	}
	// Lease alone takes a precondition, its token; the refusals a client
	// branches on carry codes.
	for _, b := range reg.Behaviors() {
		if (len(b.PreconditionSchema) > 0) != (b.Name == "Lease") {
			t.Errorf("%s preconditionSchema = %s", b.Name, b.PreconditionSchema)
		}
	}
	stock, _ := reg.Behavior("Stock")
	calendar, _ := reg.Behavior("Calendar")
	holds, _ := reg.Behavior("Holds")
	payments, _ := reg.Behavior("Payments")
	codes := func(b Behavior) []string {
		var out []string
		for _, veto := range b.Vetoes {
			out = append(out, veto.Code)
		}
		return out
	}
	for _, want := range []struct {
		behavior Behavior
		codes    []string
	}{
		{workflow, []string{"already_in_state", "terminal_state", "transition_not_allowed", "no_status"}},
		{dependencies, []string{"blocked", "already_blocking", "cycle", "gated"}},
		{links, []string{"no_revision", "no_release", "required_link", "required_target", "too_many", "not_linked"}},
		{rollups, []string{"not_held"}},
		{revisions, []string{"no_review", "not_pending"}},
		{retries, []string{"exhausted", "limits_fixed", "not_configured"}},
		{lease, []string{"held_by_another", "held_by_caller", "not_leased", "not_holder", "lapsed", "token_stale", "token_required", "max_expiries", "hold_limit_fixed", "not_configured"}},
		{assignment, []string{"assigned_to_another", "already_assigned", "not_assigned", "not_configured"}},
		{queue, []string{"not_claimable", "blocked", "stale_link", "claim_required"}},
		{presence, []string{"no_principal", "not_principal", "principal_fixed"}},
		{budget, []string{"over_limit", "not_leased", "scope_moved", "scope_reserved", "below_committed", "exceeds_reservation", "not_configured"}},
		{blueprint, []string{"stamped", "no_dependencies", "not_constant", "no_revision", "unreadable", "invalid_steps"}},
		{branches, []string{"version_conflict", "name_taken", "ref_sealed", "primary_merge_only", "nothing_to_commit", "entity_not_found", "invalid_tree",
			"merge_into_itself", "no_parent", "not_tagged", "walk_ceiling", "primary_line"}},
		{stateRules, []string{"missing_fields", "frozen_fields", "required_fields"}},
		{stock, []string{"not_requested", "unavailable", "held", "over_max", "below_held"}},
		{calendar, []string{"not_requested", "unavailable", "held", "outside_hours", "closed", "off_grid", "too_short", "too_long", "too_soon", "too_far",
			"has_bookings", "window_too_long", "no_closure"}},
		{holds, []string{"unavailable", "holds_missing", "held_fields", "committed", "too_many_items", "not_taking", "no_supply"}},
		{payments, []string{"nothing_due", "amount_locked", "currency_mismatch", "over_capture", "exceeds_refundable", "unpaid", "unauthorized",
			"payment_in_progress", "no_attempt", "not_linked", "key_reused", "attempt_closed", "not_configured"}},
	} {
		if got := codes(want.behavior); !slices.Equal(got, want.codes) {
			t.Errorf("%s veto codes = %v, want %v", want.behavior.Name, got, want.codes)
		}
	}

	for _, test := range []struct {
		behavior Behavior
		config   string
		want     string
	}{
		{workflow, `{"states": ["draft", "done"], "transitions": [{"from": "draft", "to": "done"}]}`, ""},
		{workflow, `{"states": ["draft", "review", "done"], "initial": "review", "transitions": [{"from": "review", "to": "done", "permission": "documents.publish"}]}`, ""},
		{workflow, ``, "behavior Workflow config: "},
		{workflow, `{"states": [], "transitions": []}`, "behavior Workflow config: "},
		{workflow, `{"states": ["draft", "draft"], "transitions": []}`, "behavior Workflow config: "},
		{workflow, `{"states": ["in review"], "transitions": []}`, "behavior Workflow config: "},
		{workflow, `{"states": ["draft"]}`, "behavior Workflow config: "},
		{workflow, `{"states": ["draft"], "transitions": [], "terminal": ["draft"]}`, "behavior Workflow config: "},
		{workflow, `{"states": ["a", "b"], "transitions": [{"from": "a", "to": "b", "guard": "x"}]}`, "behavior Workflow config: "},
		{workflow, `{"states": ["a", "b"], "transitions": [{"from": "a", "to": "b", "permission": ""}]}`, "behavior Workflow config: "},
		{workflow, `{"states": ["running", "passed", "failed", "skipped"], "transitions": [{"from": "running", "to": "passed"}, {"from": "running", "to": "failed"}, {"from": "running", "to": "skipped"}],
			"outcomes": {"failed": "failure", "skipped": "neutral", "passed": "success"}}`, ""},
		{workflow, `{"states": ["a", "b"], "transitions": [{"from": "a", "to": "b"}], "outcomes": {}}`, "behavior Workflow config: "},
		{workflow, `{"states": ["a", "b"], "transitions": [{"from": "a", "to": "b"}], "outcomes": {"b": "aborted"}}`, "behavior Workflow config: "},
		{workflow, `{"states": ["a", "b"], "transitions": [{"from": "a", "to": "b"}], "outcomes": {"in review": "failure"}}`, "behavior Workflow config: "},
		{comments, ``, ""},
		{comments, `{}`, ""},
		{comments, `{"maxLength": 10}`, "behavior Comments takes no config"},
		{revisions, ``, ""},
		{revisions, `{"review": {"permission": "documents.review"}}`, ""},
		{revisions, `{"review": {}}`, "behavior Revisions config: "},
		{revisions, `{"review": {"permission": "documents.review", "quorum": 2}}`, "behavior Revisions config: "},
		{revisions, `{"reviewers": ["alice"]}`, "behavior Revisions config: "},
		{dependencies, ``, ""},
		{dependencies, `{"schemas": ["tasks", "milestones"], "gatedStates": ["done"]}`, ""},
		{dependencies, `{"schemas": []}`, "behavior Dependencies config: "},
		{dependencies, `{"schemas": ["task list"]}`, "behavior Dependencies config: "},
		{dependencies, `{"schemas": ["tasks", "tasks"]}`, "behavior Dependencies config: "},
		{dependencies, `{"gatedStates": []}`, "behavior Dependencies config: "},
		{dependencies, `{"gatedStates": [""]}`, "behavior Dependencies config: "},
		{dependencies, `{"blockers": ["tasks"]}`, "behavior Dependencies config: "},
		{dependencies, `{"gatedStates": ["doing", "done"], "satisfiedBy": ["success", "neutral"]}`, ""},
		{dependencies, `{"satisfiedBy": []}`, "behavior Dependencies config: "},
		{dependencies, `{"satisfiedBy": ["done"]}`, "behavior Dependencies config: "},
		{dependencies, `{"satisfiedBy": ["success", "success"]}`, "behavior Dependencies config: "},
		{links, `{"links": {"spec": {"schema": "documents", "pinned": true}, "parent": {"schema": "tasks", "required": true}}}`, ""},
		{links, `{"links": {"spec": {"schema": "documents", "pinned": "revision"}, "recipe": {"schema": "recipes", "pinned": "release"}, "note": {"schema": "notes", "pinned": false}}}`, ""},
		{links, `{"links": {"spec": {"schema": "documents", "pinned": "tag"}}}`, "behavior Links config: "},
		{links, ``, "behavior Links config: "},
		{links, `{"links": {}}`, "behavior Links config: "},
		{links, `{"links": {"Spec": {"schema": "documents"}}}`, "behavior Links config: "},
		{links, `{"links": {"spec_doc": {"schema": "documents"}}}`, "behavior Links config: "},
		{links, `{"links": {"spec": {}}}`, "behavior Links config: "},
		{links, `{"links": {"spec": {"schema": "the documents"}}}`, "behavior Links config: "},
		{links, `{"links": {"spec": {"schema": "documents", "weak": true}}}`, "behavior Links config: "},
		{links, `{"links": {"spec": {"schema": "documents"}}, "cascade": true}`, "behavior Links config: "},
		{links, `{"links": {"reviewers": {"schema": "people", "many": true, "max": 5, "required": true}, "watchers": {"schema": "people", "many": false}}}`, ""},
		{links, `{"links": {"reviewers": {"schema": "people", "many": true}}}`, "behavior Links config: "},
		{links, `{"links": {"reviewers": {"schema": "people", "many": true, "max": 0}}}`, "behavior Links config: "},
		{links, `{"links": {"reviewers": {"schema": "people", "many": true, "max": 101}}}`, "behavior Links config: "},
		{links, `{"links": {"reviewers": {"schema": "people", "max": 5}}}`, "behavior Links config: "},
		{links, `{"links": {"reviewers": {"schema": "people", "many": false, "max": 5}}}`, "behavior Links config: "},
		{rollups, `{"rollups": {"tasks": {"schema": "tasks", "link": "project", "function": "count"}}}`, ""},
		{rollups, `{"rollups": {"byStatus": {"schema": "tasks", "link": "project", "function": "countBy", "field": "Workflow.status"},
			"estimate": {"schema": "tasks", "link": "project", "function": "sum", "field": "estimate"},
			"smallest": {"schema": "tasks", "link": "project", "function": "min", "field": "estimate"},
			"largest": {"schema": "tasks", "link": "project", "function": "max", "field": "estimate"},
			"finished": {"schema": "tasks", "link": "project", "function": "all", "gatedStates": ["done"]},
			"started": {"schema": "tasks", "link": "project", "function": "any"},
			"succeeded": {"schema": "tasks", "link": "project", "function": "all", "outcomes": ["success", "neutral"], "gatedStates": ["done"]},
			"failed": {"schema": "tasks", "link": "project", "function": "any", "outcomes": ["failure"]},
			"lastResult": {"schema": "tasks", "link": "project", "function": "latest", "field": "result"}}}`, ""},
		{rollups, `{"rollups": {"tasks": {"schema": "tasks", "link": "project", "function": "latest"}}}`, "behavior Rollups config: "},
		{rollups, ``, "behavior Rollups config: "},
		{rollups, `{"rollups": {}}`, "behavior Rollups config: "},
		{rollups, `{"rollups": {"Tasks": {"schema": "tasks", "link": "project", "function": "count"}}}`, "behavior Rollups config: "},
		{rollups, `{"rollups": {"tasks": {"schema": "tasks", "function": "count"}}}`, "behavior Rollups config: "},
		{rollups, `{"rollups": {"tasks": {"schema": "the tasks", "link": "project", "function": "count"}}}`, "behavior Rollups config: "},
		{rollups, `{"rollups": {"tasks": {"schema": "tasks", "link": "Project", "function": "count"}}}`, "behavior Rollups config: "},
		{rollups, `{"rollups": {"tasks": {"schema": "tasks", "link": "project", "function": "average", "field": "estimate"}}}`, "behavior Rollups config: "},
		{rollups, `{"rollups": {"tasks": {"schema": "tasks", "link": "project", "function": "sum"}}}`, "behavior Rollups config: "},
		{rollups, `{"rollups": {"tasks": {"schema": "tasks", "link": "project", "function": "countBy"}}}`, "behavior Rollups config: "},
		{rollups, `{"rollups": {"tasks": {"schema": "tasks", "link": "project", "function": "count", "field": "Workflow.status"}}}`, "behavior Rollups config: "},
		{rollups, `{"rollups": {"tasks": {"schema": "tasks", "link": "project", "function": "all", "field": "Workflow.status"}}}`, "behavior Rollups config: "},
		{rollups, `{"rollups": {"tasks": {"schema": "tasks", "link": "project", "function": "count", "gatedStates": ["done"]}}}`, "behavior Rollups config: "},
		{rollups, `{"rollups": {"tasks": {"schema": "tasks", "link": "project", "function": "all", "gatedStates": []}}}`, "behavior Rollups config: "},
		{rollups, `{"rollups": {"tasks": {"schema": "tasks", "link": "project", "function": "count", "filter": "open"}}}`, "behavior Rollups config: "},
		{rollups, `{"rollups": {"tasks": {"schema": "tasks", "link": "project", "function": "count", "outcomes": ["success"]}}}`, "behavior Rollups config: "},
		{rollups, `{"rollups": {"tasks": {"schema": "tasks", "link": "project", "function": "all", "outcomes": []}}}`, "behavior Rollups config: "},
		{rollups, `{"rollups": {"tasks": {"schema": "tasks", "link": "project", "function": "any", "outcomes": ["done"]}}}`, "behavior Rollups config: "},
		{search, `{"fields": ["title", "body"], "weights": {"title": 3, "body": 0.5}}`, ""},
		{search, `{"fields": ["title"]}`, ""},
		{search, ``, "behavior Search config: "},
		{search, `{"fields": []}`, "behavior Search config: "},
		{search, `{"fields": ["title", "title"]}`, "behavior Search config: "},
		{search, `{"fields": [""]}`, "behavior Search config: "},
		{search, `{"fields": ["f1", "f2", "f3", "f4", "f5", "f6", "f7", "f8", "f9", "f10", "f11", "f12", "f13", "f14", "f15", "f16", "f17"]}`, "behavior Search config: "},
		{search, `{"fields": ["title"], "weights": {"title": 0}}`, "behavior Search config: "},
		{search, `{"fields": ["title"], "weights": {"title": "high"}}`, "behavior Search config: "},
		{search, `{"fields": ["title"], "vectors": true}`, "behavior Search config: "},
		{search, `{"fields": ["title"], "vectors": {"dimensions": 384, "model": "minilm-l6", "permission": "notes.embed"}}`, ""},
		{search, `{"fields": ["title"], "vectors": {"dimensions": 384, "model": "minilm-l6"}}`, "behavior Search config: "},
		{search, `{"fields": ["title"], "vectors": {"dimensions": 0, "model": "minilm-l6", "permission": "notes.embed"}}`, "behavior Search config: "},
		{search, `{"fields": ["title"], "vectors": {"dimensions": 4097, "model": "minilm-l6", "permission": "notes.embed"}}`, "behavior Search config: "},
		{search, `{"fields": ["title"], "vectors": {"dimensions": 384, "model": "", "permission": "notes.embed"}}`, "behavior Search config: "},
		{search, `{"fields": ["title"], "vectors": {"dimensions": 384, "model": "minilm-l6", "permission": "notes.embed", "provider": "x"}}`, "behavior Search config: "},
		{reactions, `{"rules": [{"when": {"enters": "doing"}, "then": {"link": "project", "transition": "active"}}, {"when": {"allTerminal": {"schema": "tasks", "link": "project"}}, "then": {"transition": "done"}}]}`, ""},
		{reactions, ``, "behavior Reactions config: "},
		{reactions, `{"rules": []}`, "behavior Reactions config: "},
		{reactions, `{"rules": [{"when": {}, "then": {"transition": "done"}}]}`, "behavior Reactions config: "},
		{reactions, `{"rules": [{"when": {"enters": "doing", "allTerminal": {"schema": "tasks", "link": "project"}}, "then": {"transition": "done"}}]}`, "behavior Reactions config: "},
		{reactions, `{"rules": [{"when": {"enters": "doing"}}]}`, "behavior Reactions config: "},
		{reactions, `{"rules": [{"when": {"enters": "doing"}, "then": {"link": "project"}}]}`, "behavior Reactions config: "},
		{reactions, `{"rules": [{"when": {"leaves": "doing"}, "then": {"transition": "done"}}]}`, "behavior Reactions config: "},
		{reactions, `{"rules": [{"when": {"changes": {"field": "priority", "to": "urgent"}}, "then": {"transition": "escalated"}},
			{"when": {"changes": {"field": "Lease.ended.reason", "from": "ttl"}}, "then": {"transition": "queued"}},
			{"when": {"changes": {"field": "Workflow.status", "to": {"any": "JSON"}}}, "then": {"link": "project", "transition": "active"}}]}`, ""},
		{reactions, `{"rules": [{"when": {"changes": {"to": "urgent"}}, "then": {"transition": "escalated"}}]}`, "behavior Reactions config: "},
		{reactions, `{"rules": [{"when": {"changes": {"field": "priority", "into": "urgent"}}, "then": {"transition": "escalated"}}]}`, "behavior Reactions config: "},
		{reactions, `{"rules": [{"when": {"changes": {"field": "Workflow..status"}}, "then": {"transition": "escalated"}}]}`, "behavior Reactions config: "},
		{reactions, `{"rules": [{"when": {"changes": {"field": "priority"}, "enters": "doing"}, "then": {"transition": "escalated"}}]}`, "behavior Reactions config: "},
		{reactions, `{"rules": [{"when": {"allTerminal": {"schema": "the tasks", "link": "project"}}, "then": {"transition": "done"}}]}`, "behavior Reactions config: "},
		{reactions, `{"rules": [{"when": {"allTerminal": {"schema": "tasks", "link": "Project"}}, "then": {"transition": "done"}}]}`, "behavior Reactions config: "},
		{reactions, `{"rules": [{"when": {"enters": "doing"}, "then": {"transition": "done", "invoke": "comment"}}]}`, "behavior Reactions config: "},
		{reactions, `{"rules": [{"when": {"allTerminal": {"schema": "steps", "link": "run", "outcomes": ["success"]}}, "then": {"transition": "completed"}},
			{"when": {"anyTerminal": {"schema": "steps", "link": "run", "outcomes": ["failure"]}}, "then": {"transition": "failed"}}]}`, ""},
		{reactions, `{"rules": [{"when": {"anyTerminal": {"schema": "steps", "link": "run"}}, "then": {"transition": "failed"}}]}`, "behavior Reactions config: "},
		{reactions, `{"rules": [{"when": {"anyTerminal": {"schema": "steps", "link": "run", "outcomes": []}}, "then": {"transition": "failed"}}]}`, "behavior Reactions config: "},
		{reactions, `{"rules": [{"when": {"allTerminal": {"schema": "steps", "link": "run", "outcomes": ["passed"]}}, "then": {"transition": "completed"}}]}`, "behavior Reactions config: "},
		{reactions, `{"rules": [{"when": {"allTerminal": {"schema": "steps", "link": "run"}, "anyTerminal": {"schema": "steps", "link": "run", "outcomes": ["failure"]}}, "then": {"transition": "done"}}]}`, "behavior Reactions config: "},
		{reactions, `{"rules": [{"when": {"holds": "stepsPassed"}, "then": {"transition": "completed"}}, {"when": {"revised": {"link": "spec"}}, "then": {"transition": "review"}}]}`, ""},
		{reactions, `{"rules": [{"when": {"holds": "Steps"}, "then": {"transition": "completed"}}]}`, "behavior Reactions config: "},
		{reactions, `{"rules": [{"when": {"holds": ""}, "then": {"transition": "completed"}}]}`, "behavior Reactions config: "},
		{reactions, `{"rules": [{"when": {"revised": {}}, "then": {"transition": "review"}}]}`, "behavior Reactions config: "},
		{reactions, `{"rules": [{"when": {"revised": {"link": "spec", "release": true}}, "then": {"transition": "review"}}]}`, "behavior Reactions config: "},
		{constants, `{"fields": ["kind", "key"]}`, ""},
		{constants, `{"fields": ["kind"], "permission": "steps.rename"}`, ""},
		{constants, ``, "behavior Constants config: "},
		{constants, `{"fields": []}`, "behavior Constants config: "},
		{constants, `{"fields": ["kind", "kind"]}`, "behavior Constants config: "},
		{constants, `{"fields": [""]}`, "behavior Constants config: "},
		{constants, `{"fields": ["kind"], "permission": ""}`, "behavior Constants config: "},
		{constants, `{"fields": ["kind"], "roles": ["admin"]}`, "behavior Constants config: "},
		{variants, `{"field": "result", "by": "kind", "types": {"verify": "VerifyResult", "review": "ReviewResult"}}`, ""},
		{variants, ``, "behavior Variants config: "},
		{variants, `{"field": "result", "by": "kind"}`, "behavior Variants config: "},
		{variants, `{"field": "result", "by": "kind", "types": {}}`, "behavior Variants config: "},
		{variants, `{"field": "result", "by": "kind", "types": {"": "VerifyResult"}}`, "behavior Variants config: "},
		{variants, `{"field": "result", "by": "kind", "types": {"verify": ""}}`, "behavior Variants config: "},
		{variants, `{"field": "result", "by": "kind", "types": {"verify": ["VerifyResult"]}}`, "behavior Variants config: "},
		{variants, `{"field": "", "by": "kind", "types": {"verify": "VerifyResult"}}`, "behavior Variants config: "},
		{variants, `{"field": "result", "by": "kind", "types": {"verify": "VerifyResult"}, "otherwise": "AnyResult"}`, "behavior Variants config: "},
		{branches, `{"kinds": {"step": {"type": "Step"}}}`, ""},
		{branches, `{"kinds": {"step": {"type": "Step", "order": "position", "units": {"timings": "keyed", "notes": "excluded"}, "retentionDays": 365},
			"ingredient": {"type": "Ingredient", "parent": {"key": "stepKey", "of": "step"}}, "cover": {"type": "Cover", "singleton": true}},
			"primary": "trunk", "snapshotEvery": 16, "sweep": {"intervalMs": 60000, "discardGrace": 86400000, "pruneBatch": 500, "abandonAfter": 604800000}}`, ""},
		{branches, ``, "behavior Branches config: "},
		{branches, `{"kinds": {}}`, "behavior Branches config: "},
		{branches, `{"kinds": {"Step": {"type": "Step"}}}`, "behavior Branches config: "},
		{branches, `{"kinds": {"step_row": {"type": "Step"}}}`, "behavior Branches config: "},
		{branches, `{"kinds": {"step": {}}}`, "behavior Branches config: "},
		{branches, `{"kinds": {"step": {"type": ""}}}`, "behavior Branches config: "},
		{branches, `{"kinds": {"step": {"type": "Step", "parent": {"key": "stepKey"}}}}`, "behavior Branches config: "},
		{branches, `{"kinds": {"step": {"type": "Step", "units": {"timings": "merged"}}}}`, "behavior Branches config: "},
		{branches, `{"kinds": {"step": {"type": "Step", "retentionDays": 0}}}`, "behavior Branches config: "},
		{branches, `{"kinds": {"step": {"type": "Step", "author": "editor"}}}`, "behavior Branches config: "},
		{branches, `{"kinds": {"step": {"type": "Step"}}, "primary": ""}`, "behavior Branches config: "},
		{branches, `{"kinds": {"step": {"type": "Step"}}, "snapshotEvery": 0}`, "behavior Branches config: "},
		{branches, `{"kinds": {"step": {"type": "Step"}}, "sweep": {}}`, "behavior Branches config: "},
		{branches, `{"kinds": {"step": {"type": "Step"}}, "sweep": {"intervalMs": 500}}`, "behavior Branches config: "},
		{branches, `{"kinds": {"step": {"type": "Step"}}, "sweep": {"intervalMs": 60000, "abandonAfter": 0}}`, "behavior Branches config: "},
		{branches, `{"kinds": {"step": {"type": "Step"}}, "schemaEpoch": 1}`, "behavior Branches config: "},
		{stateRules, `{"states": {"sent": {"required": ["customer", "amount"], "frozen": ["amount"]}, "paid": {"frozen": "all"}}, "permission": "invoices.amend"}`, ""},
		{stateRules, `{"states": {"draft": {"required": ["title"]}}}`, ""},
		{stateRules, ``, "behavior StateRules config: "},
		{stateRules, `{"states": {}}`, "behavior StateRules config: "},
		{stateRules, `{"states": {"sent": {}}}`, "behavior StateRules config: "},
		{stateRules, `{"states": {"sent": {"required": []}}}`, "behavior StateRules config: "},
		{stateRules, `{"states": {"sent": {"required": ["amount", "amount"]}}}`, "behavior StateRules config: "},
		{stateRules, `{"states": {"sent": {"frozen": "some"}}}`, "behavior StateRules config: "},
		{stateRules, `{"states": {"sent": {"frozen": []}}}`, "behavior StateRules config: "},
		{stateRules, `{"states": {"in review": {"frozen": "all"}}}`, "behavior StateRules config: "},
		{stateRules, `{"states": {"sent": {"optional": ["notes"]}}}`, "behavior StateRules config: "},
		{stateRules, `{"states": {"sent": {"frozen": "all"}}, "permission": ""}`, "behavior StateRules config: "},
		{stateRules, `{"states": {"sent": {"frozen": "all"}}, "roles": ["admin"]}`, "behavior StateRules config: "},
		{lease, ``, ""},
		{lease, `{"ttlMs": 30000, "heartbeatMs": 10000, "sweepMs": 2000, "maxHoldMs": 3600000, "maxHoldField": "timeLimitMs",
			"onExpiry": {"transition": "queued", "from": ["running"]}, "maxExpiries": 3, "escalate": {"transition": "failed", "from": ["running"]},
			"exempt": ["Comments.comment", "acme.Rating.rate"], "acquirePermission": "jobs.work", "overridePermission": "jobs.admin", "directPermission": "jobs.direct"}`, ""},
		{lease, `{"ttlMs": 999}`, "behavior Lease config: "},
		{lease, `{"sweepMs": 500}`, "behavior Lease config: "},
		{lease, `{"heartbeatMs": 0}`, "behavior Lease config: "},
		{lease, `{"ttlMs": 1500.5}`, "behavior Lease config: "},
		{lease, `{"maxHoldMs": 10}`, "behavior Lease config: "},
		{lease, `{"onExpiry": {"transition": "queued"}}`, "behavior Lease config: "},
		{lease, `{"onExpiry": {"transition": "queued", "from": []}}`, "behavior Lease config: "},
		{lease, `{"onExpiry": {"transition": "queued", "from": ["running"], "release": true}}`, "behavior Lease config: "},
		{lease, `{"maxExpiries": 0}`, "behavior Lease config: "},
		{lease, `{"exempt": ["comment"]}`, "behavior Lease config: "},
		{lease, `{"exempt": ["Comments.Comment"]}`, "behavior Lease config: "},
		{lease, `{"overridePermission": ""}`, "behavior Lease config: "},
		{lease, `{"fenceExemptOps": ["comment"]}`, "behavior Lease config: "},
		{lease, `{"directPermission": "jobs.direct", "directOn": [{"revised": {"link": "plan"}, "name": "rebase", "data": {"why": "moved"}}]}`, ""},
		{lease, `{"directOn": []}`, "behavior Lease config: "},
		{lease, `{"directOn": [{"revised": {"link": "plan"}}]}`, "behavior Lease config: "},
		{lease, `{"directOn": [{"revised": {}, "name": "rebase"}]}`, "behavior Lease config: "},
		{lease, `{"directOn": [{"revised": {"link": "Plan"}, "name": "rebase"}]}`, "behavior Lease config: "},
		{lease, `{"directOn": [{"revised": {"link": "plan"}, "name": "re base"}]}`, "behavior Lease config: "},
		{lease, `{"directOn": [{"on": {"link": "plan"}, "name": "rebase"}]}`, "behavior Lease config: "},
		{assignment, ``, ""},
		{assignment, `{"permission": "jobs.assign"}`, ""},
		{assignment, `{"permission": ""}`, "behavior Assignment config: "},
		{assignment, `{"actorKinds": ["person"]}`, "behavior Assignment config: "},
		{queue, `{"claim": {"from": ["queued"], "to": "running"}}`, ""},
		{queue, `{"claim": {"from": ["queued", "paused"], "to": "running"}, "priorityField": "priority", "match": ["topic"], "maxCandidates": 50}`, ""},
		{queue, ``, "behavior Queue config: "},
		{queue, `{"claim": {"from": [], "to": "running"}}`, "behavior Queue config: "},
		{queue, `{"claim": {"from": ["queued"]}}`, "behavior Queue config: "},
		{queue, `{"claim": {"from": ["queued"], "to": "running"}, "maxCandidates": 0}`, "behavior Queue config: "},
		{queue, `{"claim": {"from": ["queued"], "to": "running"}, "maxCandidates": 1001}`, "behavior Queue config: "},
		{queue, `{"claim": {"from": ["queued"], "to": "running"}, "kinds": ["build"]}`, "behavior Queue config: "},
		{queue, `{"claim": {"from": ["queued"], "to": "running"}, "excludeStale": ["spec"]}`, ""},
		{queue, `{"claim": {"from": ["queued"], "to": "running"}, "excludeStale": []}`, "behavior Queue config: "},
		{queue, `{"claim": {"from": ["queued"], "to": "running"}, "excludeStale": ["spec", "spec"]}`, "behavior Queue config: "},
		{queue, `{"claim": {"from": ["queued"], "to": "running"}, "excludeStale": ["Spec"]}`, "behavior Queue config: "},
		{presence, `{"ttlMs": 30000, "principalField": "subject"}`, ""},
		{presence, `{"ttlMs": 30000, "principalField": "subject", "onMissed": {"transition": "missing", "from": ["idle"]},
			"onBeat": {"transition": "idle", "from": ["missing"]}, "releaseLeases": ["jobs"], "sweepMs": 1000}`, ""},
		{presence, ``, "behavior Presence config: "},
		{presence, `{"ttlMs": 999, "principalField": "subject"}`, "behavior Presence config: "},
		{presence, `{"ttlMs": 30000}`, "behavior Presence config: "},
		{presence, `{"ttlMs": 30000, "principalField": "subject", "onMissed": {"transition": "missing"}}`, "behavior Presence config: "},
		{presence, `{"ttlMs": 30000, "principalField": "subject", "sweepMs": 500}`, "behavior Presence config: "},
		{presence, `{"ttlMs": 30000, "principalField": "subject", "releaseLeases": ["the jobs"]}`, "behavior Presence config: "},
		{presence, `{"ttlMs": 30000, "actorField": "subject"}`, "behavior Presence config: "},
		{blueprint, `{"schema": "steps", "parentLink": "run", "keyField": "step", "steps": {"a": {}, "b": {"after": ["a"], "when": {"field": "flags", "includes": "x"}, "data": {"title": "B"}}},
			"copyFields": ["topic"]}`, ""},
		{blueprint, `{"schema": "steps", "parentLink": "run", "keyField": "step", "from": {"link": "plan", "field": "steps"}, "copyLinks": ["area"]}`, ""},
		{blueprint, ``, "behavior Blueprint config: "},
		{blueprint, `{"schema": "steps", "parentLink": "run", "keyField": "step"}`, "behavior Blueprint config: "},
		{blueprint, `{"schema": "steps", "parentLink": "run", "keyField": "step", "steps": {"a": {}}, "from": {"link": "plan", "field": "steps"}}`, "behavior Blueprint config: "},
		{blueprint, `{"schema": "steps", "parentLink": "run", "keyField": "step", "steps": {}}`, "behavior Blueprint config: "},
		{blueprint, `{"schema": "steps", "parentLink": "run", "keyField": "step", "steps": {"a": {"when": {"field": "x", "equals": 1, "includes": 1}}}}`, "behavior Blueprint config: "},
		{blueprint, `{"schema": "steps", "parentLink": "run", "keyField": "step", "steps": {"a": {"kind": "build"}}}`, "behavior Blueprint config: "},
		{blueprint, `{"schema": "steps", "parentLink": "Run", "keyField": "step", "steps": {"a": {}}}`, "behavior Blueprint config: "},
		{blueprint, `{"schema": "steps", "parentLink": "run", "keyField": "step", "steps": {"a": {}}, "stamp": "steps"}`, "behavior Blueprint config: "},
		{budget, `{"meters": {"cpuSeconds": {"limit": 3600, "reserve": 600, "reset": "daily"}}}`, ""},
		{budget, `{"meters": {"cpuSeconds": {"limitField": "cpuLimit", "reserveField": "cpuEstimate", "scope": "pool"}, "requests": {}},
			"limitPermission": "jobs.budget", "onExceeded": {"direct": "budgetExceeded"}, "escalate": {"transition": "paused", "from": ["running"]}}`, ""},
		{budget, `{"meters": {"cpuSeconds": {"reserve": 5, "reserveField": "cpuEstimate"}}}`, ""},
		{budget, ``, "behavior Budget config: "},
		{budget, `{"meters": {}}`, "behavior Budget config: "},
		{budget, `{"meters": {"CpuSeconds": {}}}`, "behavior Budget config: "},
		{budget, `{"meters": {"cpuSeconds": {"limit": 0}}}`, "behavior Budget config: "},
		{budget, `{"meters": {"cpuSeconds": {"limit": 10, "limitField": "cpuLimit"}}}`, ""},
		{budget, `{"meters": {"cpuSeconds": {}}, "escalate": {"transition": "paused"}}`, "behavior Budget config: "},
		{budget, `{"meters": {"cpuSeconds": {}}, "escalate": {"transition": "paused", "from": []}}`, "behavior Budget config: "},
		{budget, `{"meters": {"cpuSeconds": {"reset": "weekly"}}}`, "behavior Budget config: "},
		{budget, `{"meters": {"cpuSeconds": {"scope": "Pool"}}}`, "behavior Budget config: "},
		{budget, `{"meters": {"cpuSeconds": {}}, "onExceeded": {}}`, "behavior Budget config: "},
		{budget, `{"meters": {"cpuSeconds": {}}, "limitPermission": ""}`, "behavior Budget config: "},
		{budget, `{"meters": {"cpuSeconds": {}}, "raiseLimitKinds": ["person"]}`, "behavior Budget config: "},
		{retries, `{"classes": {"timeout": {"attempts": 3}, "rejected": "terminal"}, "totalAttempts": 4, "exhaustedState": "failed"}`, ""},
		{retries, `{"classes": {"timeout": {"attempts": 3, "hint": "Give it more time."}}, "totalAttempts": 4, "limitsField": "caps", "limitsPermission": "jobs.caps",
			"keepBest": {"minDelta": 0.5, "neverRegress": ["compiles"]}, "stuckAfter": 2, "resultField": "result", "exhaustedState": "failed", "from": ["running"],
			"permission": "jobs.work"}`, ""},
		{retries, ``, "behavior Retries config: "},
		{retries, `{"classes": {}, "totalAttempts": 4, "exhaustedState": "failed"}`, "behavior Retries config: "},
		{retries, `{"classes": {"timeout": {"attempts": 3}}, "exhaustedState": "failed"}`, "behavior Retries config: "},
		{retries, `{"classes": {"timeout": {"attempts": 3}}, "totalAttempts": 4}`, "behavior Retries config: "},
		{retries, `{"classes": {"timeout": {"attempts": 0}}, "totalAttempts": 4, "exhaustedState": "failed"}`, "behavior Retries config: "},
		{retries, `{"classes": {"timeout": "fatal"}, "totalAttempts": 4, "exhaustedState": "failed"}`, "behavior Retries config: "},
		{retries, `{"classes": {"timeout": {"attempts": 3, "delay": "wait"}}, "totalAttempts": 4, "exhaustedState": "failed"}`, "behavior Retries config: "},
		{retries, `{"classes": {"timeout": {"attempts": 3, "hint": ""}}, "totalAttempts": 4, "exhaustedState": "failed"}`, "behavior Retries config: "},
		{retries, `{"classes": {"totalAttempts": {"attempts": 3}}, "totalAttempts": 4, "exhaustedState": "failed"}`, "behavior Retries config: "},
		{retries, `{"classes": {"timeout": {"attempts": 3}}, "totalAttempts": 4, "exhaustedState": "failed", "stuckAfter": 0}`, "behavior Retries config: "},
		{retries, `{"classes": {"timeout": {"attempts": 3}}, "totalAttempts": 4, "exhaustedState": "failed", "from": []}`, "behavior Retries config: "},
		{retries, `{"classes": {"timeout": {"attempts": 3}}, "totalAttempts": 4, "exhaustedState": "failed", "bestSoFar": true}`, "behavior Retries config: "},
		{stock, `{"permission": "stock.adjust"}`, ""},
		{stock, `{"price": {"field": "priceCents", "currency": "GBP"}, "lowAt": 5, "maxPerHold": 99, "backorder": false, "permission": "stock.adjust"}`, ""},
		{stock, `{"price": {"field": "priceCents", "currency": {"field": "currency"}}, "lowAt": {"field": "reorderAt"}, "permission": "stock.adjust"}`, ""},
		{stock, ``, "behavior Stock config: "},
		{stock, `{"permission": ""}`, "behavior Stock config: "},
		{stock, `{"price": {"field": "priceCents"}, "permission": "stock.adjust"}`, "behavior Stock config: "},
		{stock, `{"price": {"field": "priceCents", "currency": "gbp"}, "permission": "stock.adjust"}`, "behavior Stock config: "},
		{stock, `{"price": {"field": "priceCents", "currency": "POUND"}, "permission": "stock.adjust"}`, "behavior Stock config: "},
		{stock, `{"lowAt": -1, "permission": "stock.adjust"}`, "behavior Stock config: "},
		{stock, `{"maxPerHold": 0, "permission": "stock.adjust"}`, "behavior Stock config: "},
		{stock, `{"permission": "stock.adjust", "reserveOn": ["held"]}`, "behavior Stock config: "},
		{calendar, `{"timeZone": "Europe/London", "hours": {"mon": [["09:00", "12:30"], ["13:30", "17:30"]], "sat": [["10:00", "16:00"]]}}`, ""},
		{calendar, `{"timeZone": "Australia/Lord_Howe", "hours": {"sun": [["00:00", "24:00"]]}, "capacity": {"field": "chairs"}, "slotMs": 900000, "minMs": 900000,
			"maxMs": 14400000, "bufferMs": {"before": 0, "after": 600000}, "leadMs": 7200000, "horizonMs": 5184000000, "permission": "calendar.manage"}`, ""},
		{calendar, ``, "behavior Calendar config: "},
		{calendar, `{"timeZone": "Europe/London"}`, "behavior Calendar config: "},
		{calendar, `{"timeZone": "Europe/London", "hours": {"mon": [["9:00", "12:30"]]}}`, "behavior Calendar config: "},
		{calendar, `{"timeZone": "Europe/London", "hours": {"mon": [["09:00", "24:30"]]}}`, "behavior Calendar config: "},
		{calendar, `{"timeZone": "Europe/London", "hours": {"mon": [["09:00"]]}}`, "behavior Calendar config: "},
		{calendar, `{"timeZone": "Europe/London", "hours": {"monday": [["09:00", "17:00"]]}}`, "behavior Calendar config: "},
		{calendar, `{"timeZone": "Europe/London", "hours": {}, "capacity": 0}`, "behavior Calendar config: "},
		{calendar, `{"timeZone": "Europe/London", "hours": {}, "slotMs": 1000}`, "behavior Calendar config: "},
		{calendar, `{"timeZone": "Europe/London", "hours": {}, "bufferMs": {"after": -1}}`, "behavior Calendar config: "},
		{calendar, `{"timeZone": "", "hours": {}}`, "behavior Calendar config: "},
		{holds, `{"holds": {"lines": {"each": {"field": "lines", "target": "inventoryId"}, "schema": "Inventory", "quantity": {"field": "quantity"}},
			"pickup": {"link": "counter", "interval": {"start": "pickupAt", "durationMs": 900000}}},
			"takeOn": ["held"], "commitOn": ["placed"], "releaseOn": ["expired", "cancelled"], "returnOn": ["returned"], "maxItems": 50}`, ""},
		{holds, `{"holds": {"seat": {"link": "show", "quantity": 1}}}`, ""},
		{holds, `{"holds": {"room": {"link": "room", "interval": {"start": "arrives", "end": "leaves"}}}}`, ""},
		{holds, ``, "behavior Holds config: "},
		{holds, `{"holds": {}}`, "behavior Holds config: "},
		{holds, `{"holds": {"seat": {"link": "show"}}}`, "behavior Holds config: "},
		{holds, `{"holds": {"seat": {"link": "show", "quantity": 1, "interval": {"start": "at", "durationMs": 60000}}}}`, "behavior Holds config: "},
		{holds, `{"holds": {"seat": {"link": "show", "each": {"field": "lines", "target": "id"}, "schema": "Show", "quantity": 1}}}`, "behavior Holds config: "},
		{holds, `{"holds": {"lines": {"each": {"field": "lines", "target": "id"}, "quantity": 1}}}`, "behavior Holds config: "},
		{holds, `{"holds": {"room": {"link": "room", "interval": {"start": "arrives"}}}}`, "behavior Holds config: "},
		{holds, `{"holds": {"room": {"link": "room", "interval": {"start": "arrives", "end": "leaves", "durationMs": 60000}}}}`, "behavior Holds config: "},
		{holds, `{"holds": {"Seat": {"link": "show", "quantity": 1}}}`, "behavior Holds config: "},
		{holds, `{"holds": {"seat": {"link": "show", "quantity": 0}}}`, "behavior Holds config: "},
		{holds, `{"holds": {"seat": {"link": "show", "quantity": 1}}, "takeOn": []}`, "behavior Holds config: "},
		{holds, `{"holds": {"seat": {"link": "show", "quantity": 1}}, "ttlMs": 900000}`, "behavior Holds config: "},
		{payments, `{"amount": {"holds": true}, "record": {"permission": "payments.record"}}`, ""},
		{payments, `{"amount": {"field": "totalCents"}, "currency": "GBP", "capture": "manual", "captureOn": ["shipped"], "require": {"placed": "authorized"},
			"voidOn": ["expired", "cancelled"], "afterRelease": "refund", "refunds": {"permission": "payments.refund"}, "record": {"permission": "payments.record"}}`, ""},
		{payments, `{"amount": {"field": "amountCents"}, "currency": {"field": "currency"}, "refundOf": {"link": "order"}, "issueOn": ["approved"],
			"record": {"permission": "payments.record"}}`, ""},
		{payments, ``, "behavior Payments config: "},
		{payments, `{"amount": {"holds": true}}`, "behavior Payments config: "},
		{payments, `{"amount": {"field": "totalCents"}, "record": {"permission": "payments.record"}}`, "behavior Payments config: "},
		{payments, `{"amount": {"holds": false}, "record": {"permission": "payments.record"}}`, "behavior Payments config: "},
		{payments, `{"amount": {"holds": true}, "currency": "EURO", "record": {"permission": "payments.record"}}`, "behavior Payments config: "},
		{payments, `{"amount": {"holds": true}, "require": {"placed": "captured"}, "record": {"permission": "payments.record"}}`, "behavior Payments config: "},
		{payments, `{"amount": {"holds": true}, "captureOn": ["shipped"], "record": {"permission": "payments.record"}}`, "behavior Payments config: "},
		{payments, `{"amount": {"holds": true}, "capture": "automatic", "captureOn": ["shipped"], "record": {"permission": "payments.record"}}`, "behavior Payments config: "},
		{payments, `{"amount": {"field": "amountCents"}, "currency": "GBP", "refundOf": {"link": "order"}, "record": {"permission": "payments.record"}}`, "behavior Payments config: "},
		{payments, `{"amount": {"holds": true}, "issueOn": ["approved"], "record": {"permission": "payments.record"}}`, "behavior Payments config: "},
		{payments, `{"amount": {"holds": true}, "record": {"permission": "payments.record"}, "card": {"store": true}}`, "behavior Payments config: "},
	} {
		err := test.behavior.ValidateConfig(json.RawMessage(test.config))
		switch {
		case test.want == "" && err != nil:
			t.Errorf("%s %s: %v", test.behavior.Name, test.config, err)
		case test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)):
			t.Errorf("%s %s: err = %v, want %q", test.behavior.Name, test.config, err, test.want)
		}
	}
}

// The commerce behaviors (D70). Stock and Calendar are supplies: each
// declares the supply contract's operations under the same names, with the
// same parameters but for what an item asks (a quantity of counted supply,
// an interval of timed supply), so the two conflict, and Holds invokes
// either by name. Holds requires Workflow; Payments' schema-level
// listPaymentActions is the adapter's pull. Every config is required.
func TestCommerceBehaviors(t *testing.T) {
	reg := New(naming.Default())
	finalizeWithCoreGenerators(t, reg)
	stock, _ := reg.Behavior("Stock")
	calendar, _ := reg.Behavior("Calendar")
	holds, _ := reg.Behavior("Holds")
	payments, _ := reg.Behavior("Payments")
	for _, b := range []Behavior{stock, calendar, holds, payments} {
		if b.Package != CommercePackage || !b.ConfigRequired() || len(b.PreconditionSchema) != 0 || len(b.CreateParamsSchema) != 0 {
			t.Errorf("%s: package %q, config required %v, precondition %s, create params %s; want %s, a config, and neither",
				b.Name, b.Package, b.ConfigRequired(), b.PreconditionSchema, b.CreateParamsSchema, CommercePackage)
		}
	}
	if !slices.Equal(stock.Conflicts, []string{"Calendar"}) || !slices.Equal(calendar.Conflicts, []string{"Stock"}) || len(stock.Requires) != 0 || len(calendar.Requires) != 0 {
		t.Errorf("Stock conflicts %v requires %v, Calendar conflicts %v requires %v; want each conflicting with the other, and no requirement",
			stock.Conflicts, stock.Requires, calendar.Conflicts, calendar.Requires)
	}
	if !slices.Equal(holds.Requires, []string{"Workflow"}) || len(payments.Requires) != 0 || len(holds.Conflicts) != 0 || len(payments.Conflicts) != 0 {
		t.Errorf("Holds requires %v, Payments requires %v; want [Workflow] and none", holds.Requires, payments.Requires)
	}
	for _, want := range []struct {
		behavior Behavior
		fields   []string
		ops      []string
	}{
		{stock, []string{"onHand", "held", "available", "low"}, []string{"receiveStock::true", "adjustStock::true", "countStock::true", "holdFor::true", "commitFor::true",
			"releaseFor::true", "returnFor::true", "checkHold::false", "listStockMovements::false"}},
		{calendar, []string{"timeZone", "hours", "closures"}, []string{"setOpeningHours::true", "addClosure::true", "removeClosure::true", "holdFor::true", "commitFor::true",
			"releaseFor::true", "checkHold::false", "listBookings::false", "findAvailability:schema:false"}},
		{holds, []string{"items", "state", "total"}, []string{"retakeHolds::true", "listHolds::false"}},
		{payments, []string{"due", "authorized", "captured", "refunded", "outstanding", "currency", "state", "attempts"}, []string{"openPayment::true", "recordPaymentEvent::true",
			"capturePayment::true", "voidPayment::true", "refundPayment::true", "listPaymentActions:schema:false", "refundFor::true", "settleRefundFor::true"}},
	} {
		var ops []string
		for _, op := range want.behavior.Operations {
			ops = append(ops, fmt.Sprintf("%s:%s:%v", op.Name, op.Scope, op.Writes))
			if op.InvocationPolicy != "" {
				t.Errorf("%s.%s names invocation policy %q; the core's behaviors name none", want.behavior.Name, op.Name, op.InvocationPolicy)
			}
		}
		if got := fieldNames(want.behavior.Fields); !slices.Equal(got, want.fields) {
			t.Errorf("%s fields = %v, want %v", want.behavior.Name, got, want.fields)
		}
		if !slices.Equal(ops, want.ops) {
			t.Errorf("%s operations = %v, want %v", want.behavior.Name, ops, want.ops)
		}
	}

	// The contract: what each supply's operation takes and returns is the
	// same, but for the member that says what an item asks, which
	// checkHold's also lists for the claim's other items.
	operation := func(b Behavior, name string) BehaviorOperation {
		for _, op := range b.Operations {
			if op.Name == name {
				return op
			}
		}
		t.Fatalf("%s declares no %s", b.Name, name)
		return BehaviorOperation{}
	}
	shape := func(raw json.RawMessage, drop string) string {
		var schema map[string]any
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		if properties, ok := schema["properties"].(map[string]any); ok && drop != "" {
			if also, ok := properties["also"].(map[string]any); ok {
				items := also["items"].(map[string]any)
				listed, _ := json.Marshal(items["properties"].(map[string]any)[drop])
				asked, _ := json.Marshal(properties[drop])
				if string(listed) != string(asked) {
					t.Errorf("also lists %s as %s, not as the call asks it, %s", drop, listed, asked)
				}
				delete(items["properties"].(map[string]any), drop)
				items["required"] = []any{}
			}
			delete(properties, drop)
			var required []any
			for _, name := range schema["required"].([]any) {
				if name != drop {
					required = append(required, name)
				}
			}
			schema["required"] = required
		}
		out, _ := json.Marshal(schema)
		return string(out)
	}
	for _, name := range []string{"holdFor", "commitFor", "releaseFor", "checkHold"} {
		counted, timed := operation(stock, name), operation(calendar, name)
		if counted.Description != timed.Description || string(counted.ResultSchema) != string(timed.ResultSchema) {
			t.Errorf("%s: Stock's and Calendar's description or result differ", name)
		}
		if shape(counted.ParamsSchema, "quantity") != shape(timed.ParamsSchema, "interval") {
			t.Errorf("%s: Stock's params %s and Calendar's %s differ but for quantity and interval", name, counted.ParamsSchema, timed.ParamsSchema)
		}
		var params struct {
			Required []string `json:"required"`
		}
		if err := json.Unmarshal(counted.ParamsSchema, &params); err != nil || !slices.Equal(params.Required, []string{"holder", "hold", "item", "quantity"}) {
			t.Errorf("%s: Stock's params require %v, want holder, hold, item and quantity", name, params.Required)
		}
		if err := json.Unmarshal(timed.ParamsSchema, &params); err != nil || !slices.Equal(params.Required, []string{"holder", "hold", "item", "interval"}) {
			t.Errorf("%s: Calendar's params require %v, want holder, hold, item and interval", name, params.Required)
		}
	}
	if string(operation(stock, "returnFor").ParamsSchema) != string(operation(stock, "holdFor").ParamsSchema) {
		t.Errorf("Stock's returnFor takes other parameters than holdFor")
	}
	for _, supply := range []Behavior{stock, calendar} {
		if !strings.Contains(string(operation(supply, "checkHold").ParamsSchema), `"also"`) {
			t.Errorf("%s's checkHold takes no also", supply.Name)
		}
		if strings.Contains(string(operation(supply, "holdFor").ParamsSchema), `"also"`) {
			t.Errorf("%s's holdFor takes also, which only checkHold reads", supply.Name)
		}
	}
	for _, code := range []string{"not_requested", "unavailable", "held"} {
		var inStock, inCalendar string
		for _, veto := range stock.Vetoes {
			if veto.Code == code {
				inStock = veto.Description
			}
		}
		for _, veto := range calendar.Vetoes {
			if veto.Code == code {
				inCalendar = veto.Description
			}
		}
		if inStock == "" || inStock != inCalendar {
			t.Errorf("veto %s: Stock's %q, Calendar's %q; want one description in both", code, inStock, inCalendar)
		}
	}
}

// The core's behaviors name no invocation policy, so a distribution whose
// policy has none of the core's values still finalizes with them: each
// operation takes that policy's default.
func TestCoreBehaviorsUnderAnotherInvocationPolicy(t *testing.T) {
	reg := New(naming.Default())
	if err := reg.Use(policyExtension{name: "vendor", policy: reviewPolicy}); err != nil {
		t.Fatal(err)
	}
	finalizeWithCoreGenerators(t, reg)
	if got := reg.BehaviorNames(); !slices.Equal(got, coreBehaviorNames) {
		t.Fatalf("BehaviorNames() = %v, want %v", got, coreBehaviorNames)
	}
}

// An extension cannot declare a core behavior's name, bare or under its
// own prefix's rule, and a second core registration of one is refused.
func TestCoreBehaviorNamesAreTaken(t *testing.T) {
	reg := New(naming.Default())
	err := reg.RegisterBehavior(BehaviorSpec{Declaration: declaration("Workflow", nil)})
	if err == nil || !strings.Contains(err.Error(), `behavior "Workflow" is already registered`) {
		t.Fatalf("a second Workflow: err = %v", err)
	}
	err = New(naming.Default()).Use(fakeExtension{name: "acme", register: func(r *Registry) error {
		return r.RegisterBehavior(BehaviorSpec{Extension: "acme", Declaration: declaration("Workflow", nil)})
	}})
	if err == nil || !strings.Contains(err.Error(), `behavior name "Workflow" of extension acme must be acme.<Name>`) {
		t.Fatalf("an extension's bare Workflow: err = %v", err)
	}
}

// fieldNames lists the names of a behavior's fields, in order.
func fieldNames(fields []BehaviorField) []string {
	names := make([]string, len(fields))
	for i, field := range fields {
		names[i] = field.Name
	}
	return names
}
