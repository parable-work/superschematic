package verify_test

import "testing"

// TestApprovalsConfigIsHeldToTheTable (D104): a table's Approvals reads the
// Workflow its status names, or the table's one Workflow; each gate and
// each state its from names is one of that Workflow's states, from not
// naming the gate's own; each field a gate's resetOn names is an own field
// of the table; and a gate that excludes the creator is on a table with a
// createdBy: the core's words for these, the build's for a resetOn field a
// behavior owns, a relation by its name, an audit field and a field the
// core reads no value of (encrypted at rest, a list of date-times). The first
// problem alone is reported: the status, then each gate in the canonical
// config's order. The configSchema refuses a pinned that is no boolean and
// a resetOn that is no list, in the registry's words.
func TestApprovalsConfigIsHeldToTheTable(t *testing.T) {
	const prefix = "type Task: behavior Approvals config: "
	gates := func(task map[string]any) map[string]any { return config(task, "Approvals")["gates"].(map[string]any) }
	gate := func(state string, members map[string]any) func(_, task map[string]any) {
		return func(_, task map[string]any) {
			g := map[string]any{"count": 1, "permission": "tasks.review"}
			for k, v := range members {
				g[k] = v
			}
			gates(task)[state] = g
		}
	}
	// noCreatedBy drops Task's createdBy, which StateRules' by then no
	// longer names.
	noCreatedBy := func(task map[string]any) {
		dropField(task, "createdBy")
		config(task, "StateRules")["states"].(map[string]any)["planned"] = map[string]any{"frozen": []any{"plan"}, "by": []any{"reviewer"}}
	}
	holdRefusals(t, "Approvals", []refusalCase{
		{"a status that is no Workflow", func(_, task map[string]any) { config(task, "Approvals")["status"] = "phase" },
			prefix + `status "phase" is not a Workflow of the type (its Workflows: status)`},
		{"two Workflows and no status", func(_, task map[string]any) {
			review(task)
			config(task, "StateRules")["status"] = "status"
		}, prefix + "it reads the status of Workflow, and the type has several (status, review): status names the field whose Workflow it reads"},
		{"a status naming the other Workflow", func(_, task map[string]any) {
			review(task)
			config(task, "StateRules")["status"] = "status"
			config(task, "Approvals")["status"] = "review"
		}, prefix + `gate "done" is not a state of the type's Workflow (pending, reviewed)`},
		{"a gate the Workflow lacks", gate("archived", nil),
			prefix + `gate "archived" is not a state of the type's Workflow (planned, working, proving, done, cancelled)`},
		{"a from naming its gate", gate("done", map[string]any{"from": []any{"proving", "done"}}),
			prefix + `gate "done": from names "done" itself, and no transition leads from a state to itself`},
		{"a from the Workflow lacks", gate("done", map[string]any{"from": []any{"reviewing"}}),
			prefix + `gate "done": from state "reviewing" is not a state of the type's Workflow (planned, working, proving, done, cancelled)`},
		{"a resetOn field the table lacks", gate("done", map[string]any{"resetOn": []any{"proof", "evidence"}}),
			prefix + `gate "done": resetOn names "evidence", which is not a field of Task (its fields: ` + taskFields + ")"},
		{"a resetOn field Workflow owns", gate("done", map[string]any{"resetOn": []any{"statusEnteredAt"}}),
			prefix + `gate "done": resetOn names "statusEnteredAt", a field Workflow owns: resetOn names the table's own fields, whose updates the core sees`},
		{"a resetOn relation by its name", func(doc, task map[string]any) {
			person(doc)
			addField(task, taskField("sponsor", "Person", map[string]any{"relation": map[string]any{"type": "Person"}}))
			gate("done", map[string]any{"resetOn": []any{"sponsor"}})(doc, task)
		}, prefix + `gate "done": resetOn names "sponsor", a relation: name the field that holds its key, sponsorId`},
		{"a resetOn audit field", gate("done", map[string]any{"resetOn": []any{"createdBy"}}),
			prefix + `gate "done": resetOn names "createdBy", an audit field the writes stamp, which no update changes`},
		{"a resetOn field encrypted at rest", func(doc, task map[string]any) {
			addField(task, taskField("secret", "string", map[string]any{"encryptedAtRest": true}))
			gate("done", map[string]any{"resetOn": []any{"proof", "secret"}})(doc, task)
		}, prefix + `gate "done": resetOn names "secret", which is encrypted at rest: the core reads no value of it`},
		{"a resetOn list of date-times", func(doc, task map[string]any) {
			addField(task, map[string]any{"name": "slots", "typeRef": map[string]any{"name": "Temporal.DateTime", "isArray": true}})
			gate("done", map[string]any{"resetOn": []any{"slots"}})(doc, task)
		}, prefix + `gate "done": resetOn names "slots", which is a list of date-times, which no adapter converts: the core reads no value of it`},
		{"a gate that excludes the creator, with no createdBy", func(_, task map[string]any) { noCreatedBy(task) },
			prefix + `gate "done" excludes the creator (excludeCreator is true when absent), and Task has no field createdBy to read the creator from: give Task a createdBy field, or set excludeCreator to false`},
		{"a gate that names excludeCreator true, with no createdBy", func(doc, task map[string]any) {
			noCreatedBy(task)
			gate("done", map[string]any{"excludeCreator": false})(doc, task)
			gate("proving", map[string]any{"excludeCreator": true})(doc, task)
		}, prefix + `gate "proving" excludes the creator (excludeCreator is true when absent), and Task has no field createdBy to read the creator from: give Task a createdBy field, or set excludeCreator to false`},
		{"the first gate alone", func(doc, task map[string]any) {
			gate("done", map[string]any{"resetOn": []any{"evidence"}})(doc, task)
			gate("proving", map[string]any{"from": []any{"reviewing"}})(doc, task)
		}, prefix + `gate "done": resetOn names "evidence", which is not a field of Task (its fields: ` + taskFields + ")"},
		{"a pinned that is no boolean", gate("done", map[string]any{"pinned": "yes"}),
			`type "Task": behavior Approvals config: jsonschema validation failed with 'superschematic://behaviors/Approvals/config.json#'
- at '/gates/done/pinned': got string, want boolean`},
		{"a resetOn that is no list", gate("done", map[string]any{"resetOn": "proof"}),
			`type "Task": behavior Approvals config: jsonschema validation failed with 'superschematic://behaviors/Approvals/config.json#'
- at '/gates/done/resetOn': got string, want array`},
	})
	holdAccepted(t, map[string]func(doc, task map[string]any){
		"the fixture's gates": nil,
		"gates that take the creator's sign-off, with no createdBy": func(doc, task map[string]any) {
			noCreatedBy(task)
			gate("done", map[string]any{"excludeCreator": false, "pinned": true})(doc, task)
			gate("proving", map[string]any{"excludeCreator": false, "resetOn": []any{}})(doc, task)
		},
		"a resetOn of a list field and a relation's key": func(doc, task map[string]any) {
			person(doc)
			addField(task, map[string]any{"name": "tags", "typeRef": map[string]any{"name": "string", "isArray": true}})
			addField(task, taskField("sponsor", "Person", map[string]any{"relation": map[string]any{"type": "Person"}}))
			gate("done", map[string]any{"resetOn": []any{"tags", "sponsorId"}})(doc, task)
		},
		"a from of other states": gate("done", map[string]any{"from": []any{"proving", "working"}}),
		"a status naming one of two": func(_, task map[string]any) {
			review(task)
			config(task, "StateRules")["status"] = "status"
			config(task, "Approvals")["status"] = "status"
		},
	})
}
