package registry

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
)

// coreBehaviorNames are the behaviors New registers, sorted.
var coreBehaviorNames = []string{"Assignment", "Blueprint", "Budget", "Comments", "Dependencies", "Lease", "Links", "Presence", "Queue", "Reactions", "Retries", "Revisions", "Rollups", "Search", "Workflow"}

// workQueueBehaviorNames are the core behaviors @superschematic/engine-workqueue
// implements; the engine implements the rest.
var workQueueBehaviorNames = []string{"Assignment", "Blueprint", "Budget", "Lease", "Presence", "Queue", "Retries"}

// A registry with no extension declares the behaviors the engine and the
// work-queue package implement, as the core's, under bare names, each with
// the package that implements it, and holds each config to its schema.
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
		if slices.Contains(workQueueBehaviorNames, name) {
			want = WorkQueuePackage
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
	if len(rollups.Operations) != 0 || len(rollups.Fields) != 1 || rollups.Fields[0].Name != "rollups" {
		t.Errorf("Rollups: operations %v, fields %v; want none and rollups", rollups.Operations, rollups.Fields)
	}
	// Reactions adds no field and no operation: the engine's runner runs it.
	if len(reactions.Fields) != 0 || len(reactions.Operations) != 0 {
		t.Errorf("Reactions fields %v and operations %v, want none", reactions.Fields, reactions.Operations)
	}
	var scopes []string
	for _, op := range links.Operations {
		scopes = append(scopes, op.Name+":"+op.Scope)
	}
	if want := []string{"link:", "unlink:", "listLinked:schema"}; !slices.Equal(scopes, want) {
		t.Errorf("Links operations and scopes = %v, want %v", scopes, want)
	}
	if len(search.Operations) != 1 || search.Operations[0].Name != "search" || search.Operations[0].Scope != OperationScopeSchema ||
		search.Operations[0].Writes || len(search.Fields) != 0 {
		t.Errorf("Search = %+v, want one read-only schema-level operation, search, and no field", search)
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
	if len(blueprint.Operations) != 0 || len(blueprint.Fields) != 1 || blueprint.Fields[0].Name != "blueprint" {
		t.Errorf("Blueprint = %+v, want no operation and one field, blueprint", blueprint)
	}
	budget, _ := reg.Behavior("Budget")
	retries, _ := reg.Behavior("Retries")
	if !budget.ConfigRequired() || !retries.ConfigRequired() || len(budget.Requires) != 0 || !slices.Equal(retries.Requires, []string{"Workflow"}) {
		t.Errorf("Budget: config required %v, requires %v; Retries: config required %v, requires %v; want true, none, true, [Workflow]",
			budget.ConfigRequired(), budget.Requires, retries.ConfigRequired(), retries.Requires)
	}
	// Every work-queue operation writes; claimNext and expireHolder are schema-level.
	for _, b := range []Behavior{lease, assignment, queue, presence, budget, retries} {
		for _, op := range b.Operations {
			if !op.Writes || (op.Scope == OperationScopeSchema) != (op.Name == "claimNext" || op.Name == "expireHolder") {
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
		{retries, []string{"exhausted"}},
		{lease, []string{"held_by_another", "held_by_caller", "not_leased", "not_holder", "lapsed", "token_stale", "token_required", "max_expiries", "hold_limit_fixed", "not_configured"}},
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
		{links, `{"links": {"spec": {"schema": "documents", "pinned": true}, "parent": {"schema": "tasks", "required": true}}}`, ""},
		{links, ``, "behavior Links config: "},
		{links, `{"links": {}}`, "behavior Links config: "},
		{links, `{"links": {"Spec": {"schema": "documents"}}}`, "behavior Links config: "},
		{links, `{"links": {"spec_doc": {"schema": "documents"}}}`, "behavior Links config: "},
		{links, `{"links": {"spec": {}}}`, "behavior Links config: "},
		{links, `{"links": {"spec": {"schema": "the documents"}}}`, "behavior Links config: "},
		{links, `{"links": {"spec": {"schema": "documents", "weak": true}}}`, "behavior Links config: "},
		{links, `{"links": {"spec": {"schema": "documents"}}, "cascade": true}`, "behavior Links config: "},
		{rollups, `{"rollups": {"tasks": {"schema": "tasks", "link": "project", "function": "count"}}}`, ""},
		{rollups, `{"rollups": {"byStatus": {"schema": "tasks", "link": "project", "function": "countBy", "field": "status"},
			"estimate": {"schema": "tasks", "link": "project", "function": "sum", "field": "estimate"},
			"smallest": {"schema": "tasks", "link": "project", "function": "min", "field": "estimate"},
			"largest": {"schema": "tasks", "link": "project", "function": "max", "field": "estimate"},
			"finished": {"schema": "tasks", "link": "project", "function": "all", "gatedStates": ["done"]},
			"started": {"schema": "tasks", "link": "project", "function": "any"}}}`, ""},
		{rollups, ``, "behavior Rollups config: "},
		{rollups, `{"rollups": {}}`, "behavior Rollups config: "},
		{rollups, `{"rollups": {"Tasks": {"schema": "tasks", "link": "project", "function": "count"}}}`, "behavior Rollups config: "},
		{rollups, `{"rollups": {"tasks": {"schema": "tasks", "function": "count"}}}`, "behavior Rollups config: "},
		{rollups, `{"rollups": {"tasks": {"schema": "the tasks", "link": "project", "function": "count"}}}`, "behavior Rollups config: "},
		{rollups, `{"rollups": {"tasks": {"schema": "tasks", "link": "Project", "function": "count"}}}`, "behavior Rollups config: "},
		{rollups, `{"rollups": {"tasks": {"schema": "tasks", "link": "project", "function": "average", "field": "estimate"}}}`, "behavior Rollups config: "},
		{rollups, `{"rollups": {"tasks": {"schema": "tasks", "link": "project", "function": "sum"}}}`, "behavior Rollups config: "},
		{rollups, `{"rollups": {"tasks": {"schema": "tasks", "link": "project", "function": "countBy"}}}`, "behavior Rollups config: "},
		{rollups, `{"rollups": {"tasks": {"schema": "tasks", "link": "project", "function": "count", "field": "status"}}}`, "behavior Rollups config: "},
		{rollups, `{"rollups": {"tasks": {"schema": "tasks", "link": "project", "function": "all", "field": "status"}}}`, "behavior Rollups config: "},
		{rollups, `{"rollups": {"tasks": {"schema": "tasks", "link": "project", "function": "count", "gatedStates": ["done"]}}}`, "behavior Rollups config: "},
		{rollups, `{"rollups": {"tasks": {"schema": "tasks", "link": "project", "function": "all", "gatedStates": []}}}`, "behavior Rollups config: "},
		{rollups, `{"rollups": {"tasks": {"schema": "tasks", "link": "project", "function": "count", "filter": "open"}}}`, "behavior Rollups config: "},
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
		{reactions, `{"rules": [{"when": {"enters": "doing"}, "then": {"link": "project", "transition": "active"}}, {"when": {"allTerminal": {"schema": "tasks", "link": "project"}}, "then": {"transition": "done"}}]}`, ""},
		{reactions, ``, "behavior Reactions config: "},
		{reactions, `{"rules": []}`, "behavior Reactions config: "},
		{reactions, `{"rules": [{"when": {}, "then": {"transition": "done"}}]}`, "behavior Reactions config: "},
		{reactions, `{"rules": [{"when": {"enters": "doing", "allTerminal": {"schema": "tasks", "link": "project"}}, "then": {"transition": "done"}}]}`, "behavior Reactions config: "},
		{reactions, `{"rules": [{"when": {"enters": "doing"}}]}`, "behavior Reactions config: "},
		{reactions, `{"rules": [{"when": {"enters": "doing"}, "then": {"link": "project"}}]}`, "behavior Reactions config: "},
		{reactions, `{"rules": [{"when": {"leaves": "doing"}, "then": {"transition": "done"}}]}`, "behavior Reactions config: "},
		{reactions, `{"rules": [{"when": {"allTerminal": {"schema": "the tasks", "link": "project"}}, "then": {"transition": "done"}}]}`, "behavior Reactions config: "},
		{reactions, `{"rules": [{"when": {"allTerminal": {"schema": "tasks", "link": "Project"}}, "then": {"transition": "done"}}]}`, "behavior Reactions config: "},
		{reactions, `{"rules": [{"when": {"enters": "doing"}, "then": {"transition": "done", "invoke": "comment"}}]}`, "behavior Reactions config: "},
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
			"limitPermission": "jobs.budget", "onExceeded": {"direct": "budgetExceeded"}}`, ""},
		{budget, ``, "behavior Budget config: "},
		{budget, `{"meters": {}}`, "behavior Budget config: "},
		{budget, `{"meters": {"CpuSeconds": {}}}`, "behavior Budget config: "},
		{budget, `{"meters": {"cpuSeconds": {"limit": 0}}}`, "behavior Budget config: "},
		{budget, `{"meters": {"cpuSeconds": {"limit": 10, "limitField": "cpuLimit"}}}`, "behavior Budget config: "},
		{budget, `{"meters": {"cpuSeconds": {"reserve": 5, "reserveField": "cpuEstimate"}}}`, "behavior Budget config: "},
		{budget, `{"meters": {"cpuSeconds": {"reset": "weekly"}}}`, "behavior Budget config: "},
		{budget, `{"meters": {"cpuSeconds": {"scope": "Pool"}}}`, "behavior Budget config: "},
		{budget, `{"meters": {"cpuSeconds": {}}, "onExceeded": {}}`, "behavior Budget config: "},
		{budget, `{"meters": {"cpuSeconds": {}}, "limitPermission": ""}`, "behavior Budget config: "},
		{budget, `{"meters": {"cpuSeconds": {}}, "raiseLimitKinds": ["person"]}`, "behavior Budget config: "},
		{retries, `{"classes": {"timeout": {"attempts": 3}, "rejected": "terminal"}, "totalAttempts": 4, "exhaustedState": "failed"}`, ""},
		{retries, `{"classes": {"timeout": {"attempts": 3}}, "totalAttempts": 4, "limitsField": "caps", "keepBest": {"minDelta": 0.5, "neverRegress": ["compiles"]},
			"stuckAfter": 2, "resultField": "result", "exhaustedState": "failed", "from": ["running"], "permission": "jobs.work"}`, ""},
		{retries, ``, "behavior Retries config: "},
		{retries, `{"classes": {}, "totalAttempts": 4, "exhaustedState": "failed"}`, "behavior Retries config: "},
		{retries, `{"classes": {"timeout": {"attempts": 3}}, "exhaustedState": "failed"}`, "behavior Retries config: "},
		{retries, `{"classes": {"timeout": {"attempts": 3}}, "totalAttempts": 4}`, "behavior Retries config: "},
		{retries, `{"classes": {"timeout": {"attempts": 0}}, "totalAttempts": 4, "exhaustedState": "failed"}`, "behavior Retries config: "},
		{retries, `{"classes": {"timeout": "fatal"}, "totalAttempts": 4, "exhaustedState": "failed"}`, "behavior Retries config: "},
		{retries, `{"classes": {"timeout": {"attempts": 3, "hint": "wait"}}, "totalAttempts": 4, "exhaustedState": "failed"}`, "behavior Retries config: "},
		{retries, `{"classes": {"totalAttempts": {"attempts": 3}}, "totalAttempts": 4, "exhaustedState": "failed"}`, "behavior Retries config: "},
		{retries, `{"classes": {"timeout": {"attempts": 3}}, "totalAttempts": 4, "exhaustedState": "failed", "stuckAfter": 0}`, "behavior Retries config: "},
		{retries, `{"classes": {"timeout": {"attempts": 3}}, "totalAttempts": 4, "exhaustedState": "failed", "from": []}`, "behavior Retries config: "},
		{retries, `{"classes": {"timeout": {"attempts": 3}}, "totalAttempts": 4, "exhaustedState": "failed", "bestSoFar": true}`, "behavior Retries config: "},
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
