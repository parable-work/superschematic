package verify_test

import (
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/verify"
)

// TestStateRulesConfigIsHeldToTheTable (D104): a table's StateRules reads
// the Workflow its status names, or the table's one Workflow; each state is
// one of that Workflow's; each field a state requires or freezes is an own
// field of the table; and a state's by, in a state that freezes a field,
// names own fields that hold a subject, createdBy among them, as a
// Workflow transition's by does: the core's words for these, the build's
// for a field a behavior owns, a relation by its name, an audit field but
// createdBy, and a field the core reads no value of (encrypted at rest, a
// list of date-times). The first problem alone is reported: the status,
// then each state in the canonical config's order, its required, its
// frozen, its by. The configSchema refuses an empty by, in the registry's
// words.
func TestStateRulesConfigIsHeldToTheTable(t *testing.T) {
	const prefix = "type Task: behavior StateRules config: "
	states := func(task map[string]any) map[string]any { return config(task, "StateRules")["states"].(map[string]any) }
	rule := func(state string, rules map[string]any) func(_, task map[string]any) {
		return func(_, task map[string]any) { states(task)[state] = rules }
	}
	holdRefusals(t, "StateRules", []refusalCase{
		{"a status that is no Workflow", func(_, task map[string]any) { config(task, "StateRules")["status"] = "phase" },
			prefix + `status "phase" is not a Workflow of the type (its Workflows: status)`},
		{"two Workflows and no status", func(_, task map[string]any) {
			review(task)
			config(task, "Approvals")["status"] = "status"
		}, prefix + "it reads the status of Workflow, and the type has several (status, review): status names the field whose Workflow it reads"},
		{"a status naming the other Workflow", func(_, task map[string]any) {
			review(task)
			config(task, "StateRules")["status"] = "review"
			config(task, "Approvals")["status"] = "status"
		}, prefix + `states: "planned" is not a state of the type's Workflow (pending, reviewed)`},
		{"a state the Workflow lacks", rule("archived", map[string]any{"frozen": "all"}),
			prefix + `states: "archived" is not a state of the type's Workflow (planned, working, proving, done, cancelled)`},
		{"a required field the table lacks", rule("proving", map[string]any{"required": []any{"proof", "evidence"}}),
			prefix + "states.proving.required: evidence is not a field of Task (its fields: " + taskFields + ")"},
		{"a frozen field the table lacks", rule("planned", map[string]any{"frozen": []any{"scope"}}),
			prefix + "states.planned.frozen: scope is not a field of Task (its fields: " + taskFields + ")"},
		{"a frozen field Workflow owns", rule("planned", map[string]any{"frozen": []any{"statusEnteredAt"}}),
			prefix + "states.planned.frozen: statusEnteredAt is a field Workflow owns, which only its operations change: StateRules requires and freezes the table's own fields"},
		{"a required relation by its name", func(doc, task map[string]any) {
			person(doc)
			addField(task, taskField("sponsor", "Person", map[string]any{"relation": map[string]any{"type": "Person"}}))
			states(task)["proving"] = map[string]any{"required": []any{"sponsor"}}
		}, prefix + "states.proving.required: sponsor is a relation: name the field that holds its key, sponsorId"},
		{"an audit field", func(_, task map[string]any) {
			addField(task, taskField("updatedAt", "Temporal.DateTime", nil))
			states(task)["planned"] = map[string]any{"frozen": []any{"updatedAt"}}
		}, prefix + "states.planned.frozen: updatedAt is an audit field the writes stamp, which no update changes"},
		{"a required field encrypted at rest", func(_, task map[string]any) {
			addField(task, taskField("secret", "string", map[string]any{"encryptedAtRest": true}))
			states(task)["proving"] = map[string]any{"required": []any{"proof", "secret"}}
		}, prefix + "states.proving.required: secret is encrypted at rest: the core reads no value of it"},
		{"a frozen field encrypted at rest", func(_, task map[string]any) {
			addField(task, taskField("secret", "string", map[string]any{"encryptedAtRest": true}))
			states(task)["planned"] = map[string]any{"frozen": []any{"plan", "secret"}, "by": []any{"createdBy"}}
		}, prefix + "states.planned.frozen: secret is encrypted at rest: the core reads no value of it"},
		{"a frozen map of date-times", func(_, task map[string]any) {
			addField(task, map[string]any{"name": "visits", "typeRef": map[string]any{"name": "Temporal.DateTime", "isMap": true}})
			states(task)["planned"] = map[string]any{"frozen": []any{"visits"}}
		}, prefix + "states.planned.frozen: visits is a map of date-times, which no adapter converts: the core reads no value of it"},
		{"by in a state that freezes nothing", rule("proving", map[string]any{"required": []any{"proof"}, "by": []any{"reviewer"}}),
			prefix + "states.proving.by lists who may change a field proving freezes, and proving freezes none"},
		{"a seat", rule("planned", map[string]any{"frozen": []any{"plan"}, "by": []any{"seat:owner"}}),
			prefix + `states.planned.by names "seat:owner", a seat of Seats, which the core does not run yet (D104, step 3)`},
		{"a by field the table lacks", rule("planned", map[string]any{"frozen": []any{"plan"}, "by": []any{"assignee"}}),
			prefix + `states.planned.by names "assignee", which is not a field of Task (its fields: ` + taskFields + ")"},
		{"a by field that holds no subject", rule("planned", map[string]any{"frozen": []any{"plan"}, "by": []any{"urgent"}}),
			prefix + `states.planned.by names "urgent", a boolean field of Task: by names a field that holds a subject (string, Identity.UUID or Identity.UserID)`},
		{"a by relation by its name", func(doc, task map[string]any) {
			person(doc)
			addField(task, taskField("sponsor", "Person", map[string]any{"relation": map[string]any{"type": "Person"}}))
			states(task)["planned"] = map[string]any{"frozen": []any{"plan"}, "by": []any{"sponsor"}}
		}, prefix + `states.planned.by names "sponsor", a relation: name the field that holds its key, sponsorId, when its target's key is a subject (D104)`},
		{"the first state alone", func(_, task map[string]any) {
			states(task)["working"] = map[string]any{"frozen": []any{"plan"}, "by": []any{"assignee"}}
			states(task)["done"] = map[string]any{"frozen": []any{"scope"}}
		}, prefix + "states.done.frozen: scope is not a field of Task (its fields: " + taskFields + ")"},
		{"an empty by", rule("planned", map[string]any{"frozen": []any{"plan"}, "by": []any{}}),
			`type "Task": behavior StateRules config: jsonschema validation failed with 'superschematic://behaviors/StateRules/config.json#'
- at '/states/planned/by': minItems: got 0, want 1`},
	})
	holdAccepted(t, map[string]func(doc, task map[string]any){
		"the fixture's by on createdBy and reviewer": nil,
		"createdBy required":                         rule("proving", map[string]any{"required": []any{"proof", "createdBy"}}),
		"every field frozen, with by":                rule("done", map[string]any{"frozen": "all", "by": []any{"owner", "createdBy"}}),
		"a status naming the one Workflow":           func(_, task map[string]any) { config(task, "StateRules")["status"] = "status" },
		"a status naming one of two": func(_, task map[string]any) {
			review(task)
			config(task, "StateRules")["status"] = "status"
			config(task, "Approvals")["status"] = "status"
		},
		"a by on an Identity.UserID and a relation's key": func(doc, task map[string]any) {
			person(doc)
			addField(task, taskField("delegate", "Identity.UserID", nil))
			addField(task, taskField("sponsor", "Person", map[string]any{"relation": map[string]any{"type": "Person"}}))
			states(task)["working"] = map[string]any{"frozen": []any{"plan"}, "by": []any{"delegate", "sponsorId"}}
		},
	})
}

// TestFrozenAllWarnsOfTheFieldsTheCoreCannotRead (D104): a state that
// freezes "all" on a table with a field the core reads no value of (one
// encrypted at rest, a list or a map of date-times) loads, and is warned
// of, naming each such field in the spec's order: the state freezes every
// field the core sees change, and leaves those unfrozen. A table with none
// is not warned of.
func TestFrozenAllWarnsOfTheFieldsTheCoreCannotRead(t *testing.T) {
	warned := func(edit func(doc, task map[string]any)) []string {
		t.Helper()
		var warnings []string
		if _, err := loader.LoadService(writeTasks(t, edit), loader.WithWarnings(func(d *verify.Diagnostic) {
			if strings.Contains(d.Msg, `frozen is "all"`) {
				warnings = append(warnings, d.Msg)
			}
		})); err != nil {
			t.Fatalf("LoadService: %v", err)
		}
		return warnings
	}
	frozenAll := func(_, task map[string]any) {
		config(task, "StateRules")["states"].(map[string]any)["done"] = map[string]any{"frozen": "all"}
	}
	if got := warned(frozenAll); len(got) != 0 {
		t.Errorf("a table with no field the core cannot read warns %q", got)
	}
	got := warned(func(doc, task map[string]any) {
		addField(task, taskField("secret", "string", map[string]any{"encryptedAtRest": true}))
		addField(task, map[string]any{"name": "slots", "typeRef": map[string]any{"name": "Temporal.DateTime", "isArray": true}})
		frozenAll(doc, task)
	})
	want := `type Task: behavior StateRules config: states.done.frozen is "all", which leaves secret (encrypted at rest), slots (a list of date-times) unfrozen, since the core reads no value of them: name the fields done freezes, or keep them out of the updates a caller may make while the row is done (D104)`
	if len(got) != 1 || got[0] != want {
		t.Errorf("warnings = %q, want [%q]", got, want)
	}
}
